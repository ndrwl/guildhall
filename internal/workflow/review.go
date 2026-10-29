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
// Every writer writes it; ParseReviewOutcome still reads ReviewOutcomeSchemaV1 (docs/decisions/review-outcome-2.md).
const (
	reviewOutcomePrefix   = "guildhall.review-outcome/"
	reviewOutcomeVersion  = 2
	ReviewOutcomeSchema   = "guildhall.review-outcome/2"
	ReviewOutcomeSchemaV1 = "guildhall.review-outcome/1"
)

// MaxSummaryBytes bounds an outcome's summary; it is the store's text limit (store.MaxTextBytes),
// so a summary that passes Validate always fits the store's projection.
const MaxSummaryBytes = 64 << 10

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

// Finding is one blocking review comment.
//
// ID is the reviewer's stable name for the finding, kept across rounds so a later review can
// disposition it; Rule cites the rule it breaks (a requirement id such as "R1.27" or a docs/review.md
// rule such as "S4") and is required: a comment that cites no rule is a Suggestion.
type Finding struct {
	ID      string `json:"id"`
	Rule    string `json:"rule,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Summary string `json:"summary"`
}

// Suggestion is one non-blocking review comment. It never blocks, never counts for the nothing_open merge
// requirement and needs no disposition in a later round; its ID shares the findings' namespace.
type Suggestion struct {
	ID      string `json:"id"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Summary string `json:"summary"`
}

// DispositionState is what became of a previous round's finding.
type DispositionState string

const (
	// Resolved: the finding no longer stands; it is listed in neither Findings nor Suggestions.
	Resolved DispositionState = "resolved"
	// Unresolved: the finding stands; it is listed again, under the same id, in Findings.
	Unresolved DispositionState = "unresolved"
	// Disputed: the builder argued against the finding and the reviewer, finding the argument plausible, still
	// holds it; it is listed again in Findings and its disposition gives the Reason.
	Disputed DispositionState = "disputed"

	// The /1 states, read from old documents and old store projections only (Normalized).
	v1StillOpen DispositionState = "still-open"
	v1Withdrawn DispositionState = "withdrawn"
)

// Valid reports whether s is one of the disposition states above.
// It is the one rule every reader of a disposition checks, so a new state is added here.
func (s DispositionState) Valid() bool {
	return s == Resolved || s == Unresolved || s == Disputed
}

// Open reports whether the finding still stands: unresolved or disputed.
func (s DispositionState) Open() bool {
	return s == Unresolved || s == Disputed
}

// Normalized maps a /1 state to its /2 meaning (still-open is unresolved, withdrawn is resolved) and returns any
// other state unchanged, so a reader of an old store projection checks Normalized().Valid().
func (s DispositionState) Normalized() DispositionState {
	switch s {
	case v1StillOpen:
		return Unresolved
	case v1Withdrawn:
		return Resolved
	}
	return s
}

// Disposition says what became of one finding of the previous round's outcome, by its id.
type Disposition struct {
	Finding string           `json:"finding"`
	State   DispositionState `json:"state"`
	// Reason is why the finding is disputed; required for Disputed and optional otherwise.
	Reason string `json:"reason,omitempty"`
}

// ReviewOutcome is the result of a review or document-review step.
// A parsed /1 document keeps its Schema but carries the /2 meaning (ParseReviewOutcome).
type ReviewOutcome struct {
	Schema   string   `json:"schema"`
	Reviewer Reviewer `json:"reviewer"`
	Verdict  Verdict  `json:"verdict"`
	// Summary is the reviewer's one-paragraph overall verdict; required in /2, empty for a /1 document.
	Summary string `json:"summary,omitempty"`
	// Findings are the blocking comments, each citing a rule; an approved verdict may still carry some.
	Findings []Finding `json:"findings,omitempty"`
	// Suggestions are the non-blocking comments.
	Suggestions []Suggestion `json:"suggestions,omitempty"`
	// Dispositions say what became of each finding of the same step's previous outcome.
	Dispositions []Disposition `json:"dispositions,omitempty"`
	Revisions    Revisions     `json:"revisions"`
}

// Blocking reports whether the outcome holds the task back: a changes_requested verdict with at
// least one finding that cites a rule. Comments without a rule are mechanically non-blocking
// (docs/review.md), so a /1 changes_requested verdict carrying only those does not block.
// A rejected verdict is not a request for changes and is not Blocking.
func (o ReviewOutcome) Blocking() bool {
	return o.Verdict == VerdictChangesRequested && slices.ContainsFunc(o.Findings, func(f Finding) bool { return f.Rule != "" })
}

// ParseReviewOutcome strictly decodes and validates a review outcome of either version.
//
// A /1 document is decoded with the /1 shape, so a /2-only key in it is refused, and returned with the /2
// meaning, in memory only: a finding that cites no rule becomes a suggestion, still-open becomes unresolved and
// withdrawn becomes resolved. Its Schema stays /1 and its Summary empty.
func ParseReviewOutcome(data []byte) (*ReviewOutcome, error) {
	var o ReviewOutcome
	if err := strictjson.Decode(data, &o, strictjson.Options{Schema: reviewOutcomePrefix, Version: reviewOutcomeVersion}); err != nil {
		return nil, fmt.Errorf("review outcome: %w", err)
	}
	if o.Schema == ReviewOutcomeSchemaV1 {
		var v1 reviewOutcomeV1
		if err := strictjson.Decode(data, &v1, strictjson.Options{Schema: reviewOutcomePrefix, Version: 1}); err != nil {
			return nil, fmt.Errorf("review outcome %s (summary, suggestions and reason are /2 fields): %w", ReviewOutcomeSchemaV1, err)
		}
		var err error
		if o, err = v1.normalize(); err != nil {
			return nil, err
		}
	}
	return &o, o.Validate()
}

// reviewOutcomeV1 is the guildhall.review-outcome/1 document: one findings list, rule-less comments included,
// and dispositions resolved, still-open or withdrawn, without a reason.
type reviewOutcomeV1 struct {
	Schema       string          `json:"schema"`
	Reviewer     Reviewer        `json:"reviewer"`
	Verdict      Verdict         `json:"verdict"`
	Findings     []Finding       `json:"findings,omitempty"`
	Dispositions []dispositionV1 `json:"dispositions,omitempty"`
	Revisions    Revisions       `json:"revisions"`
}

type dispositionV1 struct {
	Finding string           `json:"finding"`
	State   DispositionState `json:"state"`
}

// normalize returns the /1 document with the /2 meaning. It checks the /1 disposition rules Validate cannot see
// afterwards: the state is a /1 one, and a still-open finding is listed again. A still-open finding relisted
// without a rule is a suggestion, which needs no disposition, so its disposition is dropped.
func (v reviewOutcomeV1) normalize() (ReviewOutcome, error) {
	o := ReviewOutcome{Schema: v.Schema, Reviewer: v.Reviewer, Verdict: v.Verdict, Revisions: v.Revisions}
	rules := map[string]string{}
	for _, f := range v.Findings {
		if _, ok := rules[f.ID]; !ok {
			rules[f.ID] = f.Rule
		}
		if f.Rule == "" {
			o.Suggestions = append(o.Suggestions, Suggestion{ID: f.ID, File: f.File, Line: f.Line, Summary: f.Summary})
		} else {
			o.Findings = append(o.Findings, f)
		}
	}
	var problems []string
	for _, d := range v.Dispositions {
		rule, listed := rules[d.Finding]
		switch {
		case d.State != Resolved && d.State != v1StillOpen && d.State != v1Withdrawn:
			problems = append(problems, fmt.Sprintf("disposition of %q has unknown state %q", d.Finding, d.State))
		case d.State == v1StillOpen && !listed:
			problems = append(problems, fmt.Sprintf("still-open finding %q is not listed in findings", d.Finding))
		case d.State == v1StillOpen && rule == "":
		default:
			o.Dispositions = append(o.Dispositions, Disposition{Finding: d.Finding, State: d.State.Normalized()})
		}
	}
	if len(problems) > 0 {
		return ReviewOutcome{}, errors.New("invalid review outcome: " + strings.Join(problems, "; "))
	}
	return o, nil
}

// Validate checks the outcome is complete and well formed: a /2 outcome has a one-paragraph summary; every
// finding cites a rule; finding and suggestion ids are valid ids used once across both lists; each disposition
// names one finding once with a known state, an unresolved or disputed finding is listed again in findings, a
// disputed one gives a reason, and a resolved one is listed in neither list. A /1 outcome is checked with its
// /2 meaning, without the summary. Whether the dispositions cover every finding of the previous round is the
// interpreter's check.
func (o ReviewOutcome) Validate() error {
	var problems []string
	v1 := o.Schema == ReviewOutcomeSchemaV1
	if o.Schema != ReviewOutcomeSchema && !v1 {
		problems = append(problems, fmt.Sprintf("schema must be %q", ReviewOutcomeSchema))
	}
	if o.Reviewer != HumanReviewer && o.Reviewer != AutomatedReviewer {
		problems = append(problems, fmt.Sprintf("unknown reviewer %q", o.Reviewer))
	}
	switch {
	case o.Summary == "" && v1:
	case strings.TrimSpace(o.Summary) == "":
		problems = append(problems, "summary must be one non-blank paragraph")
	case len(o.Summary) > MaxSummaryBytes:
		problems = append(problems, fmt.Sprintf("summary is %d bytes; the limit is %d", len(o.Summary), MaxSummaryBytes))
	case slices.ContainsFunc(strings.Split(strings.TrimSpace(o.Summary), "\n"), func(l string) bool { return strings.TrimSpace(l) == "" }):
		problems = append(problems, "summary must be one paragraph, with no blank line")
	}
	switch o.Verdict {
	case VerdictApproved:
	case VerdictChangesRequested, VerdictRejected:
		// A /1 verdict's findings may all have become suggestions.
		if len(o.Findings) == 0 && (!v1 || len(o.Suggestions) == 0) {
			problems = append(problems, fmt.Sprintf("verdict %s needs at least one finding", o.Verdict))
		}
	default:
		problems = append(problems, fmt.Sprintf("unknown verdict %q", o.Verdict))
	}
	ids := map[string]bool{}
	id := func(what, v string) {
		switch {
		case !ValidID(v):
			problems = append(problems, fmt.Sprintf("%s id %q must be lowercase letters, digits and hyphens, starting with a letter", what, v))
		case ids[v]:
			problems = append(problems, fmt.Sprintf("%s id %q is used twice across findings and suggestions", what, v))
		}
		ids[v] = true
	}
	findings := map[string]bool{}
	for i, f := range o.Findings {
		if strings.TrimSpace(f.Summary) == "" {
			problems = append(problems, fmt.Sprintf("finding %d has no summary", i+1))
		}
		switch {
		case f.Rule == "":
			problems = append(problems, fmt.Sprintf("finding %q cites no rule; move it to suggestions", f.ID))
		case strings.TrimSpace(f.Rule) != f.Rule:
			problems = append(problems, fmt.Sprintf("finding %d has a blank or padded rule", i+1))
		}
		id("finding", f.ID)
		findings[f.ID] = true
	}
	for i, s := range o.Suggestions {
		if strings.TrimSpace(s.Summary) == "" {
			problems = append(problems, fmt.Sprintf("suggestion %d has no summary", i+1))
		}
		id("suggestion", s.ID)
	}
	seen := map[string]bool{}
	for _, d := range o.Dispositions {
		switch {
		case d.Finding == "":
			problems = append(problems, "a disposition names no finding")
		case seen[d.Finding]:
			problems = append(problems, fmt.Sprintf("finding %q is dispositioned twice", d.Finding))
		case !d.State.Valid():
			problems = append(problems, fmt.Sprintf("disposition of %q has unknown state %q", d.Finding, d.State))
		case d.State.Open() && !findings[d.Finding]:
			problems = append(problems, fmt.Sprintf("%s finding %q is not listed in findings", d.State, d.Finding))
		case d.State == Resolved && ids[d.Finding]:
			problems = append(problems, fmt.Sprintf("resolved finding %q is still listed in findings or suggestions", d.Finding))
		case d.State == Disputed && strings.TrimSpace(d.Reason) == "":
			problems = append(problems, fmt.Sprintf("disputed finding %q needs a reason", d.Finding))
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
