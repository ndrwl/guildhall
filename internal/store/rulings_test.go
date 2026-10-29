package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/workflow"
)

// outcomeRef is a review outcome reference projecting, as a /1 outcome recorded before /2 did, a rule-cited
// finding and a rule-less one, which may still be deferred.
func outcomeRef(name string) DocumentRef {
	return DocumentRef{Repository: "app", TaskID: "t-1", Name: name, Path: "tasks/app/t-1/" + name + ".json", SHA256: strings.Repeat("a", 64),
		Review: &Review{Findings: []workflow.Finding{{ID: "f1", Rule: "R1", File: "a.go", Line: 3, Summary: "Unchecked error"}, {ID: "f2", Summary: "Rename"}}}}
}

func TestRuleFinding(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if _, err := s.PutDocument("m-2", outcomeRef("review-1-outcome")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument("m-3", DocumentRef{Repository: "app", TaskID: "t-1", Name: "plan-1-spec", Path: "plan.md", SHA256: strings.Repeat("b", 64)}); err != nil {
		t.Fatal(err)
	}
	r := Ruling{Repository: "app", TaskID: "t-1", Document: "review-1-outcome", Finding: "f1", Move: RulingOutOfScope, Reason: "Spec §2 excludes error handling."}
	for name, tc := range map[string]struct {
		mutate func(*Ruling)
		want   error
	}{
		"unknown move":         {func(r *Ruling) { r.Move = "resolved" }, ErrInvalid},
		"blank reason":         {func(r *Ruling) { r.Reason = " " }, ErrInvalid},
		"preset contest":       {func(r *Ruling) { r.ContestedBy = "review-2" }, ErrInvalid},
		"defer a rule finding": {func(r *Ruling) { r.Move = RulingDefer }, ErrInvalid},
		"unknown finding":      {func(r *Ruling) { r.Finding = "f9" }, ErrNotFound},
		"unknown document":     {func(r *Ruling) { r.Document = "review-2-outcome" }, ErrNotFound},
		"not an outcome":       {func(r *Ruling) { r.Document = "plan-1-spec" }, ErrNotFound},
		"unknown task":         {func(r *Ruling) { r.TaskID = "t-9" }, ErrNotFound},
	} {
		bad := r
		tc.mutate(&bad)
		if _, err := s.RuleFinding("bad-"+strings.ReplaceAll(name, " ", "-"), bad); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	rc, err := s.RuleFinding("rule-1", r)
	if err != nil || rc.Record != "ruling/review-1-outcome/f1" || rc.Operation != "rule-finding" {
		t.Fatalf("RuleFinding = %+v, %v", rc, err)
	}
	if _, err := s.RuleFinding("rule-1", r); err != nil {
		t.Fatalf("replay = %v", err)
	}
	again := r
	again.Move = RulingDuplicate
	if _, err := s.RuleFinding("rule-2", again); !errors.Is(err, ErrExists) {
		t.Fatalf("second ruling: err = %v, want ErrExists", err)
	}
	deferred := Ruling{Repository: "app", TaskID: "t-1", Document: "review-1-outcome", Finding: "f2", Move: RulingDefer, Reason: "Follow-up task."}
	if _, err := s.RuleFinding("rule-3", deferred); err != nil {
		t.Fatalf("defer a suggestion = %v", err)
	}
	got, err := s.Rulings("app", "t-1")
	if err != nil || !reflect.DeepEqual(got, []Ruling{r, deferred}) {
		t.Fatalf("Rulings = %+v, %v", got, err)
	}
}

// TestRuleSuggestion: a /2 projection keeps suggestions apart from findings; a ruling may name a suggestion, and
// defer one, while deferring a rule-cited finding is still refused and an id in neither list is not found.
func TestRuleSuggestion(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	d := DocumentRef{Repository: "app", TaskID: "t-1", Name: "review-1-outcome", Path: "tasks/app/t-1/review-1.json", SHA256: strings.Repeat("a", 64),
		Review: &Review{Summary: "One blocker and one suggestion.", Findings: []workflow.Finding{{ID: "f1", Rule: "R1", Summary: "Unchecked error"}},
			Suggestions: []workflow.Suggestion{{ID: "s1", File: "a.go", Line: 9, Summary: "Rename"}}}}
	if _, err := s.PutDocument("m-2", d); err != nil {
		t.Fatal(err)
	}
	r := Ruling{Repository: "app", TaskID: "t-1", Document: "review-1-outcome", Move: RulingDefer, Reason: "Follow-up task."}
	for name, tc := range map[string]struct {
		finding string
		want    error
	}{
		"defer a rule finding": {"f1", ErrInvalid},
		"unknown id":           {"s9", ErrNotFound},
	} {
		bad := r
		bad.Finding = tc.finding
		if _, err := s.RuleFinding("bad-"+strings.ReplaceAll(name, " ", "-"), bad); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	r.Finding = "s1"
	if _, err := s.RuleFinding("rule-1", r); err != nil {
		t.Fatalf("defer a suggestion = %v", err)
	}
	if got, err := s.Rulings("app", "t-1"); err != nil || !reflect.DeepEqual(got, []Ruling{r}) {
		t.Fatalf("Rulings = %+v, %v", got, err)
	}
}

// TestReviewProjection: the projection round-trips its summary and suggestions, an old projection with the /1
// state names still validates, and an unknown state, a bad suggestion id or a blank suggestion summary is refused.
func TestReviewProjection(t *testing.T) {
	if workflow.MaxSummaryBytes != MaxTextBytes {
		t.Fatalf("workflow.MaxSummaryBytes %d is not the store's text limit %d", workflow.MaxSummaryBytes, MaxTextBytes)
	}
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	ref := func(name string, r *Review) DocumentRef {
		return DocumentRef{Repository: "app", TaskID: "t-1", Name: name, Path: "tasks/app/t-1/" + name + ".json", SHA256: strings.Repeat("a", 64), Review: r}
	}
	v2 := &Review{Summary: "Approved with one suggestion.", Findings: []workflow.Finding{{ID: "f2", Rule: "S2", Summary: "Still no red run."}},
		Suggestions:  []workflow.Suggestion{{ID: "s1", File: "a.go", Line: 9, Summary: "Rename"}},
		Dispositions: []workflow.Disposition{{Finding: "f1", State: workflow.Resolved}, {Finding: "f2", State: workflow.Disputed, Reason: "Builder disagrees."}}}
	v1 := &Review{Findings: []workflow.Finding{{ID: "f3", Summary: "Rename"}},
		Dispositions: []workflow.Disposition{{Finding: "f1", State: "still-open"}, {Finding: "f2", State: "withdrawn"}}}
	for i, r := range []*Review{v2, v1} {
		name := fmt.Sprintf("review-%d-outcome", i+1)
		if _, err := s.PutDocument("put-"+name, ref(name, r)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	docs, err := s.Documents("app", "t-1")
	if err != nil || len(docs) != 2 || !reflect.DeepEqual(docs[0].Review, v2) || !reflect.DeepEqual(docs[1].Review, v1) {
		t.Fatalf("Documents = %+v, %v", docs, err)
	}
	for name, r := range map[string]*Review{
		"unknown state":            {Dispositions: []workflow.Disposition{{Finding: "f1", State: "fixed"}}},
		"bad suggestion id":        {Suggestions: []workflow.Suggestion{{ID: "S 1", Summary: "Rename"}}},
		"blank suggestion summary": {Suggestions: []workflow.Suggestion{{ID: "s1", Summary: " "}}},
		"blank summary":            {Summary: " "},
	} {
		if _, err := s.PutDocument("bad-"+strings.ReplaceAll(name, " ", "-"), ref("review-9-outcome", r)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestContestRulingOnce(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if _, err := s.PutDocument("m-2", outcomeRef("review-1-outcome")); err != nil {
		t.Fatal(err)
	}
	r := Ruling{Repository: "app", TaskID: "t-1", Document: "review-1-outcome", Finding: "f1", Move: RulingDuplicate, Reason: "Same as f2."}
	if _, err := s.RuleFinding("rule-1", r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ContestRuling("contest-0", "app", "t-1", "review-1-outcome", "f9", "review-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown ruling: err = %v, want ErrNotFound", err)
	}
	rc, err := s.ContestRuling("contest-1", "app", "t-1", "review-1-outcome", "f1", "review-2")
	if err != nil || rc.Record != "ruling/review-1-outcome/f1" || rc.Operation != "contest-ruling" {
		t.Fatalf("ContestRuling = %+v, %v", rc, err)
	}
	if _, err := s.ContestRuling("contest-1", "app", "t-1", "review-1-outcome", "f1", "review-2"); err != nil {
		t.Fatalf("replay = %v", err)
	}
	if _, err := s.ContestRuling("contest-2", "app", "t-1", "review-1-outcome", "f1", "review-3"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("second contest: err = %v, want ErrInvalid", err)
	}
	got, err := s.Rulings("app", "t-1")
	if err != nil || len(got) != 1 || got[0].ContestedBy != "review-2" {
		t.Fatalf("Rulings = %+v, %v", got, err)
	}
	h, err := s.TaskHistory("app", "t-1")
	if err != nil || !reflect.DeepEqual(h.Rulings, got) || !reflect.DeepEqual(h.Documents[0].Review, outcomeRef("").Review) {
		t.Fatalf("TaskHistory = %+v, %v", h, err)
	}
}

func TestDocumentReviewValidates(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	for name, review := range map[string]*Review{
		"bad finding id":  {Findings: []workflow.Finding{{ID: "F 1", Summary: "x"}}},
		"blank summary":   {Findings: []workflow.Finding{{ID: "f1", Summary: " "}}},
		"bad disposition": {Dispositions: []workflow.Disposition{{Finding: "f1", State: "fixed"}}},
	} {
		d := outcomeRef("review-1-outcome")
		d.Review = review
		if _, err := s.PutDocument("doc-"+strings.ReplaceAll(name, " ", "-"), d); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}
