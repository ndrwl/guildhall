package workflow

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/strictjson"
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
	// Empty means no standards revision is configured; binding still compares it exactly.
	StandardsRevision string `json:"standards_revision"`
}

// Finding is one review comment.
//
// ID is the reviewer's stable name for the finding, kept across rounds so a later review can
// disposition it; Rule cites the rule it breaks (a requirement id such as "R1.27" or a docs/review.md
// rule such as "S4"). A finding without a rule is a suggestion: it never blocks (Blocking).
type Finding struct {
	ID      string `json:"id"`
	Rule    string `json:"rule,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Summary string `json:"summary"`
}

// DispositionState is what became of a previous round's finding.
type DispositionState string

const (
	// Resolved: the finding was fixed; it is no longer listed in Findings.
	Resolved DispositionState = "resolved"
	// StillOpen: the finding stands; it is listed again, under the same id, in Findings.
	StillOpen DispositionState = "still-open"
	// Withdrawn: the reviewer no longer holds the finding; it is no longer listed in Findings.
	Withdrawn DispositionState = "withdrawn"
)

// Valid reports whether s is one of the disposition states above.
// It is the one rule every reader of a disposition checks, so a new state is added here.
func (s DispositionState) Valid() bool {
	return s == Resolved || s == StillOpen || s == Withdrawn
}

// Disposition says what became of one finding of the previous round's outcome, by its id.
type Disposition struct {
	Finding string           `json:"finding"`
	State   DispositionState `json:"state"`
}

// ReviewOutcome is the result of a review or document-review step.
type ReviewOutcome struct {
	Schema   string    `json:"schema"`
	Reviewer Reviewer  `json:"reviewer"`
	Verdict  Verdict   `json:"verdict"`
	Findings []Finding `json:"findings,omitempty"`
	// Dispositions say what became of each finding of the same step's previous outcome.
	Dispositions []Disposition `json:"dispositions,omitempty"`
	Revisions    Revisions     `json:"revisions"`
}

// Blocking reports whether the outcome holds the task back: a changes_requested verdict with at
// least one finding that cites a rule. Findings without a rule are mechanically non-blocking
// (docs/review.md), so a changes_requested verdict carrying only those does not block.
// A rejected verdict is not a request for changes and is not Blocking.
func (o ReviewOutcome) Blocking() bool {
	return o.Verdict == VerdictChangesRequested && slices.ContainsFunc(o.Findings, func(f Finding) bool { return f.Rule != "" })
}

// ParseReviewOutcome strictly decodes and validates a review outcome.
func ParseReviewOutcome(data []byte) (*ReviewOutcome, error) {
	var o ReviewOutcome
	if err := strictjson.Decode(data, &o, strictjson.Options{Schema: reviewOutcomePrefix, Version: reviewOutcomeVersion}); err != nil {
		return nil, fmt.Errorf("review outcome: %w", err)
	}
	return &o, o.Validate()
}

// Validate checks the outcome is complete and well formed: finding ids are required valid ids
// used once; a rule, when given, is not blank; each disposition names one finding once with a known
// state, a still-open finding is listed again under its id and a resolved or withdrawn one is not.
// Whether the dispositions cover every finding of the previous round is the interpreter's check.
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
	ids := map[string]bool{}
	for i, f := range o.Findings {
		if strings.TrimSpace(f.Summary) == "" {
			problems = append(problems, fmt.Sprintf("finding %d has no summary", i+1))
		}
		if f.Rule != "" && strings.TrimSpace(f.Rule) != f.Rule {
			problems = append(problems, fmt.Sprintf("finding %d has a blank or padded rule", i+1))
		}
		switch {
		case !ValidID(f.ID):
			problems = append(problems, fmt.Sprintf("finding id %q must be lowercase letters, digits and hyphens", f.ID))
		case ids[f.ID]:
			problems = append(problems, fmt.Sprintf("finding id %q is used twice", f.ID))
		}
		ids[f.ID] = true
	}
	seen := map[string]bool{}
	for _, d := range o.Dispositions {
		switch {
		case d.Finding == "":
			problems = append(problems, "a disposition names no finding")
		case seen[d.Finding]:
			problems = append(problems, fmt.Sprintf("finding %q is dispositioned twice", d.Finding))
		case d.State == StillOpen && !ids[d.Finding]:
			problems = append(problems, fmt.Sprintf("still-open finding %q is not listed in findings", d.Finding))
		case (d.State == Resolved || d.State == Withdrawn) && ids[d.Finding]:
			problems = append(problems, fmt.Sprintf("%s finding %q is still listed in findings", d.State, d.Finding))
		case !d.State.Valid():
			problems = append(problems, fmt.Sprintf("disposition of %q has unknown state %q", d.Finding, d.State))
		}
		seen[d.Finding] = true
	}
	subject := o.Revisions.Subject
	switch {
	case subject.CodeHead == "" && subject.DocumentDigest == "":
		problems = append(problems, "subject needs a code_head or a document_digest")
	case subject.CodeHead != "" && subject.DocumentDigest != "":
		problems = append(problems, "subject needs a code_head or a document_digest, not both")
	case subject.CodeHead != "":
		if !digest.ValidHead(subject.CodeHead) {
			problems = append(problems, "code_head must be a full commit id")
		}
		if _, err := digest.Parse(o.Revisions.SpecDigest); err != nil {
			problems = append(problems, `spec_digest must be "sha256:<64 hex>" when the subject is a code head`)
		}
	default:
		if _, err := digest.Parse(subject.DocumentDigest); err != nil {
			problems = append(problems, `document_digest must be "sha256:<64 hex>"`)
		}
		if _, err := digest.Parse(o.Revisions.SpecDigest); o.Revisions.SpecDigest != "" && err != nil {
			problems = append(problems, `spec_digest must be "sha256:<64 hex>"`)
		}
	}
	if o.Revisions.StandardsRevision != "" && strings.TrimSpace(o.Revisions.StandardsRevision) == "" {
		problems = append(problems, "standards_revision must not be whitespace-only")
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
