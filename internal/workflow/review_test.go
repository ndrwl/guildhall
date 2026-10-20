package workflow

import (
	"errors"
	"strings"
	"testing"
)

var revisions = Revisions{
	CodeHead:          strings.Repeat("a", 40),
	SpecDigest:        "sha256:" + strings.Repeat("b", 64),
	StandardsRevision: "standards-1",
}

func TestReviewOutcomeContract(t *testing.T) {
	for _, reviewer := range []Reviewer{HumanReviewer, AutomatedReviewer} {
		o := ReviewOutcome{
			Schema:    ReviewOutcomeSchema,
			Reviewer:  reviewer,
			Verdict:   VerdictChangesRequested,
			Findings:  []Finding{{File: "main.go", Line: 3, Summary: "missing error check"}},
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
		{"finding without summary", func(o *ReviewOutcome) { o.Findings = []Finding{{File: "a.go"}} }, "summary"},
		{"short code head", func(o *ReviewOutcome) { o.Revisions.CodeHead = "abc123" }, "code_head"},
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
	moved.CodeHead = strings.Repeat("c", 40)
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
	  "revisions": {"code_head": "` + revisions.CodeHead + `", "spec_digest": "` + revisions.SpecDigest + `", "standards_revision": "standards-1"}}`
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
