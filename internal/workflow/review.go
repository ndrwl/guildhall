package workflow

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ReviewOutcomeSchema identifies the review outcome contract shared by every review and document-review step, whoever its actor.
const (
	reviewOutcomePrefix  = "guildhall.review-outcome/"
	reviewOutcomeVersion = 1
	ReviewOutcomeSchema  = "guildhall.review-outcome/1"
)

// Verdict is a review's decision; its values double as the outcome names in workflow "on" rules.
type Verdict string

const (
	VerdictApproved         Verdict = "approved"
	VerdictChangesRequested Verdict = "changes_requested"
	VerdictRejected         Verdict = "rejected"
)

// Reviewer says who produced an outcome; the contract is otherwise identical for both.
type Reviewer string

const (
	HumanReviewer     Reviewer = "human"
	AutomatedReviewer Reviewer = "automated"
)

// Subject is what a review looked at: exactly one of a code head (review) or a document digest (document-review).
type Subject struct {
	// CodeHead is the full commit id of the reviewed code.
	CodeHead string `json:"code_head,omitempty"`
	// DocumentDigest is the "sha256:<hex>" digest of the reviewed document.
	DocumentDigest string `json:"document_digest,omitempty"`
}

// Revisions binds an outcome to exactly what was reviewed.
type Revisions struct {
	Subject Subject `json:"subject"`
	// SpecDigest is the "sha256:<hex>" digest of the accepted specification.
	// It is required when the subject is a code head and optional for a document.
	SpecDigest string `json:"spec_digest,omitempty"`
	// StandardsRevision names the central standards revision reviewed against.
	StandardsRevision string `json:"standards_revision"`
}

// Finding is one review comment.
type Finding struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Summary string `json:"summary"`
}

// ReviewOutcome is the result of a review or document-review step.
type ReviewOutcome struct {
	Schema    string    `json:"schema"`
	Reviewer  Reviewer  `json:"reviewer"`
	Verdict   Verdict   `json:"verdict"`
	Findings  []Finding `json:"findings,omitempty"`
	Revisions Revisions `json:"revisions"`
}

// ParseReviewOutcome strictly decodes and validates a review outcome.
func ParseReviewOutcome(data []byte) (*ReviewOutcome, error) {
	if err := checkSchema(data, reviewOutcomePrefix, reviewOutcomeVersion); err != nil {
		return nil, err
	}
	var o ReviewOutcome
	if err := decodeStrict(data, &o); err != nil {
		return nil, fmt.Errorf("review outcome: %w", err)
	}
	return &o, o.Validate()
}

// Validate checks the outcome is complete and well formed.
func (o ReviewOutcome) Validate() error {
	var problems []string
	if o.Schema != ReviewOutcomeSchema {
		problems = append(problems, fmt.Sprintf("schema must be %q", ReviewOutcomeSchema))
	}
	if o.Reviewer != HumanReviewer && o.Reviewer != AutomatedReviewer {
		problems = append(problems, fmt.Sprintf("unknown reviewer %q", o.Reviewer))
	}
	switch o.Verdict {
	case VerdictApproved:
	case VerdictChangesRequested, VerdictRejected:
		if len(o.Findings) == 0 {
			problems = append(problems, fmt.Sprintf("verdict %s needs at least one finding", o.Verdict))
		}
	default:
		problems = append(problems, fmt.Sprintf("unknown verdict %q", o.Verdict))
	}
	for i, f := range o.Findings {
		if strings.TrimSpace(f.Summary) == "" {
			problems = append(problems, fmt.Sprintf("finding %d has no summary", i+1))
		}
	}
	subject := o.Revisions.Subject
	switch {
	case subject.CodeHead == "" && subject.DocumentDigest == "":
		problems = append(problems, "subject needs a code_head or a document_digest")
	case subject.CodeHead != "" && subject.DocumentDigest != "":
		problems = append(problems, "subject needs a code_head or a document_digest, not both")
	case subject.CodeHead != "":
		if !isHex(subject.CodeHead, 40) && !isHex(subject.CodeHead, 64) {
			problems = append(problems, "code_head must be a full commit id")
		}
		if !isDigest(o.Revisions.SpecDigest) {
			problems = append(problems, `spec_digest must be "sha256:<64 hex>" when the subject is a code head`)
		}
	default:
		if !isDigest(subject.DocumentDigest) {
			problems = append(problems, `document_digest must be "sha256:<64 hex>"`)
		}
		if o.Revisions.SpecDigest != "" && !isDigest(o.Revisions.SpecDigest) {
			problems = append(problems, `spec_digest must be "sha256:<64 hex>"`)
		}
	}
	if strings.TrimSpace(o.Revisions.StandardsRevision) == "" {
		problems = append(problems, "standards_revision is required")
	}
	if len(problems) > 0 {
		return errors.New("invalid review outcome: " + strings.Join(problems, "; "))
	}
	return nil
}

// BoundTo refuses an outcome that reviewed anything other than the given revisions,
// so a stale review never counts for a moved head or an edited document, a changed spec or newer standards,
// and a document review never counts for a code head or the reverse.
func (o ReviewOutcome) BoundTo(want Revisions) error {
	var stale []string
	got, wantSubject := o.Revisions.Subject, want.Subject
	switch {
	case got == wantSubject:
	case (got.CodeHead == "") != (wantSubject.CodeHead == ""):
		stale = append(stale, "subject")
	case got.CodeHead != wantSubject.CodeHead:
		stale = append(stale, "subject.code_head")
	default:
		stale = append(stale, "subject.document_digest")
	}
	if o.Revisions.SpecDigest != want.SpecDigest {
		stale = append(stale, "spec_digest")
	}
	if o.Revisions.StandardsRevision != want.StandardsRevision {
		stale = append(stale, "standards_revision")
	}
	if len(stale) > 0 {
		return fmt.Errorf("review outcome is stale: %s differ", strings.Join(stale, ", "))
	}
	return nil
}

func isDigest(s string) bool {
	digest, ok := strings.CutPrefix(s, "sha256:")
	return ok && isHex(digest, 64)
}

func isHex(s string, n int) bool {
	if len(s) != n || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
