package workflow

import (
	"encoding/json"
	"testing"
)

func TestUnconfiguredStandardsBinding(t *testing.T) {
	for i, reviewer := range []Reviewer{HumanReviewer, AutomatedReviewer} {
		for _, subject := range []Subject{revisions.Subject, {DocumentDigest: documentDigest}} {
			want := revisions
			want.Subject = subject
			want.StandardsRevision = ""
			// Old /1 documents still bind the same way as /2 ones.
			outcome := ReviewOutcome{Schema: ReviewOutcomeSchema, Reviewer: reviewer, Verdict: VerdictApproved, Summary: summary, Revisions: want}
			if i == 0 {
				outcome.Schema, outcome.Summary = ReviewOutcomeSchemaV1, ""
			}
			data, err := json.Marshal(outcome)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseReviewOutcome(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := parsed.BoundTo(want); err != nil {
				t.Fatal(err)
			}
			configured := want
			configured.StandardsRevision = "standards-1"
			if err := parsed.BoundTo(configured); err == nil {
				t.Fatal("unconfigured outcome approved configured standards")
			}
			parsed.Revisions = configured
			if err := parsed.BoundTo(want); err == nil {
				t.Fatal("configured outcome approved unconfigured standards")
			}
		}
	}
}
