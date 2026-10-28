package workflow

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

var revisions = Revisions{
	Subject:           Subject{CodeHead: strings.Repeat("a", 40)},
	SpecDigest:        "sha256:" + strings.Repeat("b", 64),
	StandardsRevision: "standards-1",
}

const summary = "The change does what the spec asks."

func TestReviewOutcomeContract(t *testing.T) {
	for _, reviewer := range []Reviewer{HumanReviewer, AutomatedReviewer} {
		o := ReviewOutcome{
			Schema:      ReviewOutcomeSchema,
			Reviewer:    reviewer,
			Verdict:     VerdictChangesRequested,
			Summary:     "One error goes unchecked.",
			Findings:    []Finding{{ID: "f1", Rule: "S2", File: "main.go", Line: 3, Summary: "missing error check"}},
			Suggestions: []Suggestion{{ID: "s1", File: "main.go", Line: 9, Summary: "a shorter name"}},
			Revisions:   revisions,
		}
		if err := o.Validate(); err != nil {
			t.Fatalf("%s outcome refused: %v", reviewer, err)
		}
		if err := o.BoundTo(revisions); err != nil {
			t.Fatalf("%s outcome not bound to its own revisions: %v", reviewer, err)
		}
	}
}

func TestReviewOutcomeRefusals(t *testing.T) {
	base := ReviewOutcome{
		Schema:    ReviewOutcomeSchema,
		Reviewer:  AutomatedReviewer,
		Verdict:   VerdictApproved,
		Summary:   summary,
		Revisions: revisions,
	}
	cases := []struct {
		name   string
		mutate func(*ReviewOutcome)
		want   string
	}{
		{"unknown schema", func(o *ReviewOutcome) { o.Schema = "guildhall.review-outcome/3" }, `schema must be "guildhall.review-outcome/2"`},
		{"unknown verdict", func(o *ReviewOutcome) { o.Verdict = "maybe" }, "verdict"},
		{"unknown reviewer", func(o *ReviewOutcome) { o.Reviewer = "robot" }, "reviewer"},
		{"no summary", func(o *ReviewOutcome) { o.Summary = "" }, "summary must be one non-blank paragraph"},
		{"blank summary", func(o *ReviewOutcome) { o.Summary = " \n\t" }, "summary must be one non-blank paragraph"},
		{"two paragraphs", func(o *ReviewOutcome) { o.Summary = "First.\n\nSecond." }, "no blank line"},
		{"whitespace line", func(o *ReviewOutcome) { o.Summary = "First.\n \nSecond." }, "no blank line"},
		{"long summary", func(o *ReviewOutcome) { o.Summary = strings.Repeat("x", MaxSummaryBytes+1) }, "the limit is"},
		{"changes without findings", func(o *ReviewOutcome) { o.Verdict = VerdictChangesRequested }, "at least one finding"},
		{"changes with suggestions only", func(o *ReviewOutcome) {
			o.Verdict, o.Suggestions = VerdictChangesRequested, []Suggestion{{ID: "s1", Summary: "naming"}}
		}, "at least one finding"},
		{"rejected without findings", func(o *ReviewOutcome) { o.Verdict = VerdictRejected }, "at least one finding"},
		{"finding without id", func(o *ReviewOutcome) { o.Findings = []Finding{{Rule: "S2", Summary: "Missing regression"}} }, "finding id"},
		{"rule-less finding", func(o *ReviewOutcome) { o.Findings = []Finding{{ID: "f1", Summary: "Naming preference"}} }, `finding "f1" cites no rule; move it to suggestions`},
		{"suggestion without id", func(o *ReviewOutcome) { o.Suggestions = []Suggestion{{Summary: "Naming preference"}} }, "suggestion id"},
		{"suggestion without summary", func(o *ReviewOutcome) { o.Suggestions = []Suggestion{{ID: "s1"}} }, "suggestion 1 has no summary"},
		{"finding without summary", func(o *ReviewOutcome) { o.Findings = []Finding{{ID: "f1", Rule: "S2", File: "a.go"}} }, "summary"},
		{"short code head", func(o *ReviewOutcome) { o.Revisions.Subject.CodeHead = "abc123" }, "code_head"},
		{"no subject", func(o *ReviewOutcome) { o.Revisions.Subject = Subject{} }, "subject needs a code_head or a document_digest"},
		{"two subjects", func(o *ReviewOutcome) { o.Revisions.Subject.DocumentDigest = documentDigest }, "subject needs a code_head or a document_digest, not both"},
		{"code head without spec", func(o *ReviewOutcome) { o.Revisions.SpecDigest = "" }, "spec_digest"},
		{"bad spec digest", func(o *ReviewOutcome) { o.Revisions.SpecDigest = "md5:00" }, "spec_digest"},
		{"blank standards", func(o *ReviewOutcome) { o.Revisions.StandardsRevision = " \t" }, "standards_revision"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := base
			c.mutate(&o)
			err := o.Validate()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
	// A summary may span lines, as long as no line is blank.
	o := base
	o.Summary = "First line\nsecond line.\n"
	if err := o.Validate(); err != nil {
		t.Fatalf("one paragraph over two lines refused: %v", err)
	}
}

func TestReviewOutcomeBinding(t *testing.T) {
	o := ReviewOutcome{Schema: ReviewOutcomeSchema, Reviewer: HumanReviewer, Verdict: VerdictApproved, Summary: summary, Revisions: revisions}
	moved := revisions
	moved.Subject.CodeHead = strings.Repeat("c", 40)
	if err := o.BoundTo(moved); err == nil || !strings.Contains(err.Error(), "code_head") {
		t.Fatalf("stale code head accepted: %v", err)
	}
	moved = revisions
	moved.StandardsRevision = "standards-2"
	if err := o.BoundTo(moved); err == nil || !strings.Contains(err.Error(), "standards_revision") {
		t.Fatalf("stale standards accepted: %v", err)
	}
}

var revisionsJSON = `"revisions": {"subject": {"code_head": "` + revisions.Subject.CodeHead + `"}, "spec_digest": "` + revisions.SpecDigest + `", "standards_revision": "standards-1"}`

func TestParseReviewOutcome(t *testing.T) {
	data := `{"schema": "guildhall.review-outcome/2", "reviewer": "human", "verdict": "approved", "summary": "Fine.",
	  "suggestions": [{"id": "s1", "file": "a.go", "line": 2, "summary": "a shorter name"}], ` + revisionsJSON + `}`
	o, err := ParseReviewOutcome([]byte(data))
	if err != nil {
		t.Fatalf("valid outcome refused: %v", err)
	}
	if o.Verdict != VerdictApproved || o.Revisions != revisions || o.Summary != "Fine." || len(o.Suggestions) != 1 || o.Suggestions[0].Line != 2 {
		t.Fatalf("unexpected outcome %+v", o)
	}
	newer := strings.Replace(data, "review-outcome/2", "review-outcome/3", 1)
	if _, err := ParseReviewOutcome([]byte(newer)); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("newer outcome schema: %v", err)
	}
	unknown := strings.Replace(data, `"verdict"`, `"merge": true, "verdict"`, 1)
	if _, err := ParseReviewOutcome([]byte(unknown)); err == nil {
		t.Fatal("unknown field accepted")
	}
	ruleless := strings.Replace(data, `"suggestions"`, `"findings"`, 1)
	if _, err := ParseReviewOutcome([]byte(ruleless)); err == nil || !strings.Contains(err.Error(), "move it to suggestions") {
		t.Fatalf("rule-less /2 finding: %v", err)
	}
}

// TestParseReviewOutcomeV1: old documents still read, with the /2 meaning: rule-less findings become suggestions,
// still-open becomes unresolved and withdrawn resolved; Schema stays /1 and a changes_requested verdict blocks.
func TestParseReviewOutcomeV1(t *testing.T) {
	data := `{"schema": "guildhall.review-outcome/1", "reviewer": "automated", "verdict": "changes_requested",
	  "findings": [{"id": "f3", "rule": "S4", "file": "a.go", "line": 7, "summary": "claim without acceptance"},
	               {"id": "f4", "file": "b.go", "line": 1, "summary": "naming preference"}],
	  "dispositions": [{"finding": "f1", "state": "resolved"}, {"finding": "f2", "state": "withdrawn"},
	                   {"finding": "f3", "state": "still-open"}, {"finding": "f4", "state": "still-open"}], ` + revisionsJSON + `}`
	o, err := ParseReviewOutcome([]byte(data))
	if err != nil {
		t.Fatalf("/1 outcome refused: %v", err)
	}
	want := ReviewOutcome{
		Schema: ReviewOutcomeSchemaV1, Reviewer: AutomatedReviewer, Verdict: VerdictChangesRequested,
		Findings:     []Finding{{ID: "f3", Rule: "S4", File: "a.go", Line: 7, Summary: "claim without acceptance"}},
		Suggestions:  []Suggestion{{ID: "f4", File: "b.go", Line: 1, Summary: "naming preference"}},
		Dispositions: []Disposition{{Finding: "f1", State: Resolved}, {Finding: "f2", State: Resolved}, {Finding: "f3", State: Unresolved}},
		Revisions:    revisions,
	}
	if !reflect.DeepEqual(*o, want) {
		t.Fatalf("normalised /1 outcome = %+v, want %+v", *o, want)
	}
	if !o.Blocking() {
		t.Fatal("a /1 change request with a rule-cited finding no longer blocks")
	}
	suggestionsOnly := strings.Replace(strings.Replace(data, `"rule": "S4", `, "", 1), `{"finding": "f3", "state": "still-open"}, `, "", 1)
	o, err = ParseReviewOutcome([]byte(suggestionsOnly))
	if err != nil || !o.Blocking() || len(o.Findings) != 0 || len(o.Suggestions) != 2 {
		t.Fatalf("/1 change request with only rule-less findings = %+v, %v; want two suggestions, blocking on the verdict", o, err)
	}
	for name, c := range map[string]struct{ from, to, want string }{
		"summary":           {`"verdict"`, `"summary": "Fine.", "verdict"`, "summary"},
		"suggestions":       {`"verdict"`, `"suggestions": [], "verdict"`, "suggestions"},
		"reason":            {`"state": "withdrawn"`, `"state": "withdrawn", "reason": "no"`, "reason"},
		"unresolved state":  {`"state": "withdrawn"`, `"state": "unresolved"`, `unknown state "unresolved"`},
		"disputed state":    {`"state": "withdrawn"`, `"state": "disputed"`, `unknown state "disputed"`},
		"still-open absent": {`{"finding": "f3", "state": "still-open"}`, `{"finding": "f9", "state": "still-open"}`, `still-open finding "f9" is not listed`},
		"unknown key":       {`{"finding": "f2", "state": "withdrawn"}`, `{"finding": "f2", "state": "withdrawn", "x": 1}`, "x"},
	} {
		t.Run(name, func(t *testing.T) {
			bad := strings.Replace(data, c.from, c.to, 1)
			if _, err := ParseReviewOutcome([]byte(bad)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
	listed := strings.Replace(data, `{"finding": "f3", "state": "still-open"}`, `{"finding": "f3", "state": "withdrawn"}`, 1)
	if _, err := ParseReviewOutcome([]byte(listed)); err == nil || !strings.Contains(err.Error(), `resolved finding "f3" is still listed`) {
		t.Fatalf("withdrawn but listed /1 finding: %v", err)
	}
}

// TestBlockingAcrossVersions: a /1 outcome means the same for Blocking whether read as /1 or written as /2.
func TestBlockingAcrossVersions(t *testing.T) {
	for _, c := range []struct {
		verdict  string
		findings string
		want     bool
	}{
		{"approved", `[{"id": "f1", "rule": "S2", "summary": "x"}]`, false},
		{"changes_requested", `[{"id": "f1", "rule": "S2", "summary": "x"}, {"id": "f2", "summary": "y"}]`, true},
		{"changes_requested", `[{"id": "f2", "summary": "y"}]`, true},
		{"rejected", `[{"id": "f1", "rule": "S2", "summary": "x"}]`, false},
	} {
		data := `{"schema": "guildhall.review-outcome/1", "reviewer": "human", "verdict": "` + c.verdict + `", "findings": ` + c.findings + `, ` + revisionsJSON + `}`
		o, err := ParseReviewOutcome([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if o.Blocking() != c.want {
			t.Errorf("/1 %s %s: Blocking() = %v, want %v", c.verdict, c.findings, o.Blocking(), c.want)
		}
		if len(o.Findings) == 0 {
			continue
		}
		o.Schema, o.Summary = ReviewOutcomeSchema, summary
		encoded, err := json.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		v2, err := ParseReviewOutcome(encoded)
		if err != nil || v2.Blocking() != c.want {
			t.Errorf("/2 of /1 %s %s = %v, %v; want Blocking() %v", c.verdict, c.findings, v2, err, c.want)
		}
	}
}

var documentDigest = "sha256:" + strings.Repeat("d", 64)

func TestDocumentReviewOutcome(t *testing.T) {
	doc := Revisions{Subject: Subject{DocumentDigest: documentDigest}, StandardsRevision: "standards-1"}
	o := ReviewOutcome{Schema: ReviewOutcomeSchema, Reviewer: AutomatedReviewer, Verdict: VerdictApproved, Summary: summary, Revisions: doc}
	if err := o.Validate(); err != nil {
		t.Fatalf("document outcome without a spec digest refused: %v", err)
	}
	if err := o.BoundTo(doc); err != nil {
		t.Fatalf("document outcome not bound to its own revisions: %v", err)
	}
	withSpec := o
	withSpec.Revisions.SpecDigest = revisions.SpecDigest
	if err := withSpec.Validate(); err != nil {
		t.Fatalf("document outcome with a spec digest refused: %v", err)
	}
	bad := o
	bad.Revisions.Subject.DocumentDigest = "sha256:XYZ"
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "document_digest") {
		t.Fatalf("malformed document digest: %v", err)
	}
	edited := doc
	edited.Subject.DocumentDigest = "sha256:" + strings.Repeat("e", 64)
	if err := o.BoundTo(edited); err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("edited document accepted: %v", err)
	}
	// A document review never counts for a code head, nor a code review for a document.
	if err := o.BoundTo(revisions); err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("document outcome bound to a code head: %v", err)
	}
	code := ReviewOutcome{Schema: ReviewOutcomeSchema, Reviewer: AutomatedReviewer, Verdict: VerdictApproved, Summary: summary, Revisions: revisions}
	if err := code.BoundTo(doc); err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("code outcome bound to a document: %v", err)
	}
	for _, schema := range []string{ReviewOutcomeSchemaV1, ReviewOutcomeSchema} {
		extra := ""
		if schema == ReviewOutcomeSchema {
			extra = `"summary": "Fine.", `
		}
		data := `{"schema": "` + schema + `", "reviewer": "human", "verdict": "approved", ` + extra + `
		  "revisions": {"subject": {"document_digest": "` + documentDigest + `"}, "standards_revision": "standards-1"}}`
		parsed, err := ParseReviewOutcome([]byte(data))
		if err != nil || parsed.Revisions != doc {
			t.Fatalf("parse %s document outcome = %+v, %v", schema, parsed, err)
		}
		flat := `{"schema": "` + schema + `", "reviewer": "human", "verdict": "approved", ` + extra + `
		  "revisions": {"code_head": "` + revisions.Subject.CodeHead + `", "spec_digest": "` + revisions.SpecDigest + `", "standards_revision": "standards-1"}}`
		if _, err := ParseReviewOutcome([]byte(flat)); err == nil {
			t.Fatalf("pre-subject %s revisions accepted", schema)
		}
	}
}

func TestReviewOutcomeFindingIDsAndDispositions(t *testing.T) {
	o := ReviewOutcome{
		Schema: ReviewOutcomeSchema, Reviewer: AutomatedReviewer, Verdict: VerdictChangesRequested, Summary: summary,
		Findings: []Finding{
			{ID: "f3", Rule: "S4", File: "a.go", Line: 7, Summary: "claim without acceptance"},
			{ID: "f4", Rule: "R1.2", Summary: "spec item missing"},
		},
		Suggestions: []Suggestion{{ID: "s1", Summary: "naming preference"}},
		Dispositions: []Disposition{
			{Finding: "f1", State: Resolved}, {Finding: "f3", State: Unresolved},
			{Finding: "f4", State: Disputed, Reason: "The builder says the spec allows it; R1.2 still asks for it."},
		},
		Revisions: revisions,
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("outcome with ids and dispositions refused: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*ReviewOutcome)
		want   string
	}{
		{"duplicate finding id", func(o *ReviewOutcome) { o.Findings[1].ID = "f3" }, `finding id "f3" is used twice`},
		{"id across lists", func(o *ReviewOutcome) { o.Suggestions[0].ID = "f3" }, `suggestion id "f3" is used twice across findings and suggestions`},
		{"invalid finding id", func(o *ReviewOutcome) { o.Findings[0].ID = "F 3" }, `finding id "F 3"`},
		{"blank rule", func(o *ReviewOutcome) { o.Findings[0].Rule = " " }, "rule"},
		{"unknown state", func(o *ReviewOutcome) { o.Dispositions[0].State = "fixed" }, `unknown state "fixed"`},
		{"/1 state in /2", func(o *ReviewOutcome) { o.Dispositions[1].State = "still-open" }, `unknown state "still-open"`},
		{"disposition without id", func(o *ReviewOutcome) { o.Dispositions[0].Finding = "" }, "names no finding"},
		{"disposition twice", func(o *ReviewOutcome) { o.Dispositions[1].Finding = "f1" }, `finding "f1" is dispositioned twice`},
		{"unresolved but not listed", func(o *ReviewOutcome) { o.Dispositions[1].Finding = "f9" }, `unresolved finding "f9" is not listed`},
		{"unresolved as a suggestion", func(o *ReviewOutcome) { o.Dispositions[1].Finding = "s1" }, `unresolved finding "s1" is not listed`},
		{"disputed but not listed", func(o *ReviewOutcome) { o.Dispositions[2].Finding = "f9" }, `disputed finding "f9" is not listed`},
		{"disputed without reason", func(o *ReviewOutcome) { o.Dispositions[2].Reason = " " }, `disputed finding "f4" needs a reason`},
		{"resolved but listed", func(o *ReviewOutcome) { o.Dispositions[0].Finding = "f3" }, `resolved finding "f3" is still listed`},
		{"resolved but a suggestion", func(o *ReviewOutcome) { o.Dispositions[0].Finding = "s1" }, `resolved finding "s1" is still listed`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := o
			o.Findings = append([]Finding(nil), o.Findings...)
			o.Suggestions = append([]Suggestion(nil), o.Suggestions...)
			o.Dispositions = append([]Disposition(nil), o.Dispositions...)
			c.mutate(&o)
			if err := o.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
}

func TestDispositionStates(t *testing.T) {
	for _, c := range []struct {
		state       DispositionState
		valid, open bool
		normalized  DispositionState
	}{
		{Resolved, true, false, Resolved},
		{Unresolved, true, true, Unresolved},
		{Disputed, true, true, Disputed},
		{"still-open", false, false, Unresolved},
		{"withdrawn", false, false, Resolved},
		{"fixed", false, false, "fixed"},
	} {
		if c.state.Valid() != c.valid || c.state.Open() != c.open || c.state.Normalized() != c.normalized {
			t.Errorf("%q: Valid %v, Open %v, Normalized %q", c.state, c.state.Valid(), c.state.Open(), c.state.Normalized())
		}
	}
}

func TestReviewOutcomeBlocking(t *testing.T) {
	ruled := Finding{ID: "f1", Rule: "S2", Summary: "no red test shown"}
	suggestion := Suggestion{ID: "f2", Summary: "naming preference"}
	cases := []struct {
		name     string
		verdict  Verdict
		findings []Finding
		want     bool
	}{
		{"approved", VerdictApproved, nil, false},
		{"approved with a ruled finding", VerdictApproved, []Finding{ruled}, false},
		{"changes with a ruled finding", VerdictChangesRequested, []Finding{ruled}, true},
		{"rejected", VerdictRejected, []Finding{ruled}, false},
	}
	for _, c := range cases {
		o := ReviewOutcome{Schema: ReviewOutcomeSchema, Reviewer: AutomatedReviewer, Verdict: c.verdict, Summary: summary, Findings: c.findings,
			Suggestions: []Suggestion{suggestion}, Revisions: revisions}
		if err := o.Validate(); err != nil {
			t.Fatal(err)
		}
		if got := o.Blocking(); got != c.want {
			t.Errorf("%s: Blocking() = %v, want %v", c.name, got, c.want)
		}
	}
}
