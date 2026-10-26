package workflow

import (
	"encoding/json"
	"testing"
)

func TestUnconfiguredStandardsBinding(t *testing.T) {
	for _, reviewer := range []Reviewer{HumanReviewer, AutomatedReviewer} {
		for _, subject := range []Subject{revisions.Subject, {DocumentDigest: documentDigest}} {
			want := revisions
			want.Subject = subject
			want.StandardsRevision = ""
			outcome := ReviewOutcome{Schema: ReviewOutcomeSchema, Reviewer: reviewer, Verdict: VerdictApproved, Revisions: want}
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
