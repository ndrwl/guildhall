package workflow

import (
	"errors"
	"strings"
	"testing"
)

var revisions = Revisions{
	Subject:           Subject{CodeHead: strings.Repeat("a", 40)},
	SpecDigest:        "sha256:" + strings.Repeat("b", 64),
	StandardsRevision: "standards-1",
}

func TestReviewOutcomeContract(t *testing.T) {
	for _, reviewer := range []Reviewer{HumanReviewer, AutomatedReviewer} {
		o := ReviewOutcome{
			Schema:    ReviewOutcomeSchema,
			Reviewer:  reviewer,
			Verdict:   VerdictChangesRequested,
			Findings:  []Finding{{ID: "f1", File: "main.go", Line: 3, Summary: "missing error check"}},
			Revisions: revisions,
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
		Revisions: revisions,
	}
	cases := []struct {
		name   string
		mutate func(*ReviewOutcome)
		want   string
	}{
		{"unknown verdict", func(o *ReviewOutcome) { o.Verdict = "maybe" }, "verdict"},
		{"unknown reviewer", func(o *ReviewOutcome) { o.Reviewer = "robot" }, "reviewer"},
		{"changes without findings", func(o *ReviewOutcome) { o.Verdict = VerdictChangesRequested }, "at least one finding"},
		{"rejected without findings", func(o *ReviewOutcome) { o.Verdict = VerdictRejected }, "at least one finding"},
		{"finding without id", func(o *ReviewOutcome) { o.Findings = []Finding{{Rule: "S2", Summary: "Missing regression"}} }, "finding id"},
		{"suggestion without id", func(o *ReviewOutcome) { o.Findings = []Finding{{Summary: "Naming preference"}} }, "finding id"},
		{"finding without summary", func(o *ReviewOutcome) { o.Findings = []Finding{{File: "a.go"}} }, "summary"},
		{"short code head", func(o *ReviewOutcome) { o.Revisions.Subject.CodeHead = "abc123" }, "code_head"},
		{"no subject", func(o *ReviewOutcome) { o.Revisions.Subject = Subject{} }, "subject needs a code_head or a document_digest"},
		{"two subjects", func(o *ReviewOutcome) { o.Revisions.Subject.DocumentDigest = documentDigest }, "subject needs a code_head or a document_digest, not both"},
		{"code head without spec", func(o *ReviewOutcome) { o.Revisions.SpecDigest = "" }, "spec_digest"},
		{"bad spec digest", func(o *ReviewOutcome) { o.Revisions.SpecDigest = "md5:00" }, "spec_digest"},
		{"missing standards", func(o *ReviewOutcome) { o.Revisions.StandardsRevision = "" }, "standards_revision"},
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
}

func TestReviewOutcomeBinding(t *testing.T) {
	o := ReviewOutcome{Schema: ReviewOutcomeSchema, Reviewer: HumanReviewer, Verdict: VerdictApproved, Revisions: revisions}
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

func TestParseReviewOutcome(t *testing.T) {
	data := `{"schema": "guildhall.review-outcome/1", "reviewer": "human", "verdict": "approved",
	  "revisions": {"subject": {"code_head": "` + revisions.Subject.CodeHead + `"}, "spec_digest": "` + revisions.SpecDigest + `", "standards_revision": "standards-1"}}`
	o, err := ParseReviewOutcome([]byte(data))
	if err != nil {
		t.Fatalf("valid outcome refused: %v", err)
	}
	if o.Verdict != VerdictApproved || o.Revisions != revisions {
		t.Fatalf("unexpected outcome %+v", o)
	}
	newer := strings.Replace(data, "review-outcome/1", "review-outcome/2", 1)
	if _, err := ParseReviewOutcome([]byte(newer)); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("newer outcome schema: %v", err)
	}
	unknown := strings.Replace(data, `"verdict"`, `"merge": true, "verdict"`, 1)
	if _, err := ParseReviewOutcome([]byte(unknown)); err == nil {
		t.Fatal("unknown field accepted")
	}
}

var documentDigest = "sha256:" + strings.Repeat("d", 64)

func TestDocumentReviewOutcome(t *testing.T) {
	doc := Revisions{Subject: Subject{DocumentDigest: documentDigest}, StandardsRevision: "standards-1"}
	o := ReviewOutcome{Schema: ReviewOutcomeSchema, Reviewer: AutomatedReviewer, Verdict: VerdictApproved, Revisions: doc}
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
	code := ReviewOutcome{Schema: ReviewOutcomeSchema, Reviewer: AutomatedReviewer, Verdict: VerdictApproved, Revisions: revisions}
	if err := code.BoundTo(doc); err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("code outcome bound to a document: %v", err)
	}
	data := `{"schema": "guildhall.review-outcome/1", "reviewer": "human", "verdict": "approved",
	  "revisions": {"subject": {"document_digest": "` + documentDigest + `"}, "standards_revision": "standards-1"}}`
	parsed, err := ParseReviewOutcome([]byte(data))
	if err != nil || parsed.Revisions != doc {
		t.Fatalf("parse document outcome = %+v, %v", parsed, err)
	}
	flat := `{"schema": "guildhall.review-outcome/1", "reviewer": "human", "verdict": "approved",
	  "revisions": {"code_head": "` + revisions.Subject.CodeHead + `", "spec_digest": "` + revisions.SpecDigest + `", "standards_revision": "standards-1"}}`
	if _, err := ParseReviewOutcome([]byte(flat)); err == nil {
		t.Fatal("pre-subject revisions accepted")
	}
}

func TestReviewOutcomeFindingIDsAndDispositions(t *testing.T) {
	o := ReviewOutcome{
		Schema: ReviewOutcomeSchema, Reviewer: AutomatedReviewer, Verdict: VerdictChangesRequested,
		Findings: []Finding{
			{ID: "f3", Rule: "S4", File: "a.go", Line: 7, Summary: "claim without acceptance"},
			{ID: "f4", Summary: "naming preference"},
		},
		Dispositions: []Disposition{{Finding: "f1", State: Resolved}, {Finding: "f2", State: Withdrawn}, {Finding: "f4", State: StillOpen}},
		Revisions:    revisions,
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
		{"invalid finding id", func(o *ReviewOutcome) { o.Findings[0].ID = "F 3" }, `finding id "F 3"`},
		{"blank rule", func(o *ReviewOutcome) { o.Findings[0].Rule = " " }, "rule"},
		{"unknown state", func(o *ReviewOutcome) { o.Dispositions[0].State = "fixed" }, `unknown state "fixed"`},
		{"disposition without id", func(o *ReviewOutcome) { o.Dispositions[0].Finding = "" }, "names no finding"},
		{"disposition twice", func(o *ReviewOutcome) { o.Dispositions[1].Finding = "f1" }, `finding "f1" is dispositioned twice`},
		{"still open but not listed", func(o *ReviewOutcome) { o.Dispositions[2].Finding = "f9" }, `still-open finding "f9" is not listed`},
		{"resolved but listed", func(o *ReviewOutcome) { o.Dispositions[0].Finding = "f3" }, `resolved finding "f3" is still listed`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := o
			o.Findings = append([]Finding(nil), o.Findings...)
			o.Dispositions = append([]Disposition(nil), o.Dispositions...)
			c.mutate(&o)
			if err := o.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v does not mention %q", err, c.want)
			}
		})
	}
}

func TestReviewOutcomeBlocking(t *testing.T) {
	ruled := Finding{ID: "f1", Rule: "S2", Summary: "no red test shown"}
	suggestion := Finding{ID: "f2", Summary: "naming preference"}
	cases := []struct {
		name     string
		verdict  Verdict
		findings []Finding
		want     bool
	}{
		{"approved", VerdictApproved, nil, false},
		{"approved with a ruled finding", VerdictApproved, []Finding{ruled}, false},
		{"changes with a ruled finding", VerdictChangesRequested, []Finding{suggestion, ruled}, true},
		{"changes with suggestions only", VerdictChangesRequested, []Finding{suggestion}, false},
		{"rejected", VerdictRejected, []Finding{ruled}, false},
	}
	for _, c := range cases {
		o := ReviewOutcome{Schema: ReviewOutcomeSchema, Reviewer: AutomatedReviewer, Verdict: c.verdict, Findings: c.findings, Revisions: revisions}
		if got := o.Blocking(); got != c.want {
			t.Errorf("%s: Blocking() = %v, want %v", c.name, got, c.want)
		}
	}
}
