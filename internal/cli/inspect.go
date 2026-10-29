package cli

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// inspectLabel says what an inspection read describes: the store state as of the newest committed
// mutation, and the workflow revision the task is bound to (docs/tasks.md#inspection).
type inspectLabel struct {
	Partial     bool              `json:"partial"`
	Excluded    []string          `json:"excluded"`
	Sequence    uint64            `json:"sequence"`
	CommittedAt time.Time         `json:"committed_at"`
	Workflow    workflow.Revision `json:"workflow"`
}

// timedAttempt is an attempt with the commit times of its start and finish; times a legacy
// receipt cannot attribute are omitted, and a duration is given only when both are known.
type timedAttempt struct {
	store.StageAttempt
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	DurationSeconds *float64   `json:"duration_seconds,omitempty"`
	seq             uint64     // the start receipt's sequence, zero when unknown
}

// timedDecision is a decision with the exact words recorded and the commit times of its opening
// and answer; WaitSeconds is how long the answer took when both are known.
type timedDecision struct {
	store.Decision
	OpenedAt    *time.Time `json:"opened_at,omitempty"`
	AnsweredAt  *time.Time `json:"answered_at,omitempty"`
	WaitSeconds *float64   `json:"wait_seconds,omitempty"`
	seq         uint64     // the opening receipt's sequence, zero when unknown
}

// timedDocument is a document reference with the commit time it was recorded at.
type timedDocument struct {
	store.DocumentRef
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
}

// timedFindings is one review outcome document's summary, findings, suggestions and its dispositions of the
// previous round's, from the store's projection, with the commit time it was recorded at.
type timedFindings struct {
	Document     string                 `json:"document"`
	Summary      string                 `json:"summary,omitempty"`
	Findings     []workflow.Finding     `json:"findings"`
	Suggestions  []workflow.Suggestion  `json:"suggestions"`
	Dispositions []workflow.Disposition `json:"dispositions"`
	RecordedAt   *time.Time             `json:"recorded_at,omitempty"`
	seq          uint64
}

// timedRuling is a Director ruling with the commit times it was made and contested at.
type timedRuling struct {
	store.Ruling
	Contested   bool       `json:"contested"`
	RuledAt     *time.Time `json:"ruled_at,omitempty"`
	ContestedAt *time.Time `json:"contested_at,omitempty"`
	seq         uint64
}

type timedFullReview struct {
	store.FullReview
	RequestedAt *time.Time `json:"requested_at,omitempty"`
	ConsumedAt  *time.Time `json:"consumed_at,omitempty"`
}

type timedOverride struct {
	store.DependencyOverride
	OverriddenAt *time.Time `json:"overridden_at,omitempty"`
}

// timedPriority is one priority change with the commit time it was set at.
type timedPriority struct {
	Priority store.Priority `json:"priority"`
	SetAt    *time.Time     `json:"set_at,omitempty"`
}

// taskTimeline is what task timeline prints. Events are every receipt of the task in commit order:
// the moves made on it, including those Guildhall records for the Director.
type taskTimeline struct {
	AsOf        inspectLabel      `json:"as_of"`
	Task        store.Task        `json:"task"`
	Attempts    []timedAttempt    `json:"attempts"`
	Decisions   []timedDecision   `json:"decisions"`
	Findings    []timedFindings   `json:"findings"`
	Rulings     []timedRuling     `json:"rulings"`
	FullReviews []timedFullReview `json:"full_reviews"`
	// DependencyOverride is the Director's dependency override, with its reason; absent when there is none.
	DependencyOverride *timedOverride `json:"dependency_override,omitempty"`
	// Priority is the task's priority now, and PriorityChanges every level set, in order.
	Priority        store.Priority  `json:"priority"`
	PriorityChanges []timedPriority `json:"priority_changes"`
	// MergeHolds is every task hold-merge value set, in order: true holds the merge, false releases it.
	MergeHolds []timedHold `json:"merge_holds"`
	// Merged says what allowed the task's merge, as the merged message told the Director: a decision,
	// Guildhall's authorization, or an approval carried over to a rebased head; absent until it merges.
	Merged       *timedMerge       `json:"merged,omitempty"`
	Events       []store.Receipt   `json:"events"`
	OmittedKinds map[string]string `json:"omitted_kinds"`
}

// timedHold is one hold-merge value with the commit time it was set at.
type timedHold struct {
	Held  bool       `json:"held"`
	SetAt *time.Time `json:"set_at,omitempty"`
}

// timedMerge is the task's merge as the merged message reported it.
type timedMerge struct {
	Message  string    `json:"message"`
	Text     string    `json:"text"`
	MergedAt time.Time `json:"merged_at"`
}

// taskFiles is what task files prints: the task's recorded documents and their digests.
type taskFiles struct {
	AsOf      inspectLabel    `json:"as_of"`
	Documents []timedDocument `json:"documents"`
}

// recordReceipts maps each receipt record name to its receipts, in sequence order.
func recordReceipts(receipts []store.Receipt) map[string][]store.Receipt {
	out := map[string][]store.Receipt{}
	for _, r := range receipts {
		if r.Record != "" {
			out[r.Record] = append(out[r.Record], r)
		}
	}
	return out
}

func label(h store.TaskHistory) inspectLabel {
	return inspectLabel{Partial: true, Excluded: []string{"live worker state", "worker files", "transcript"}, Sequence: h.AsOf.Sequence, CommittedAt: h.AsOf.CommittedAt, Workflow: h.Task.Workflow}
}

func span(rs []store.Receipt, startOp, endOp string) (start, end *time.Time, seconds *float64, seq uint64) {
	for _, r := range rs {
		switch r.Operation {
		case startOp:
			start, seq = &r.CommittedAt, r.Sequence
		case endOp:
			end = &r.CommittedAt
		}
	}
	if start != nil && end != nil {
		d := end.Sub(*start).Seconds()
		seconds = &d
	}
	return start, end, seconds, seq
}

// timeline orders attempts and decisions by when they began; those without a known start keep
// the store's order after the timed ones.
func timeline(h store.TaskHistory) taskTimeline {
	byRecord := recordReceipts(h.Receipts)
	out := taskTimeline{AsOf: label(h), Task: h.Task, Attempts: []timedAttempt{}, Decisions: []timedDecision{}, Findings: []timedFindings{},
		Rulings: []timedRuling{}, FullReviews: []timedFullReview{}, Events: h.Receipts, OmittedKinds: map[string]string{}}
	for _, a := range h.Attempts {
		t := timedAttempt{StageAttempt: a}
		t.StartedAt, t.FinishedAt, t.DurationSeconds, t.seq = span(byRecord["attempt/"+a.Step+"/"+strconv.Itoa(a.Number)], "start-attempt", "finish-attempt")
		out.Attempts = append(out.Attempts, t)
	}
	for _, d := range h.Decisions {
		t := timedDecision{Decision: d}
		t.OpenedAt, t.AnsweredAt, t.WaitSeconds, t.seq = span(byRecord["decision/"+d.ID], "open-decision", "answer-decision")
		out.Decisions = append(out.Decisions, t)
	}
	for _, d := range h.Documents {
		if d.Review == nil {
			continue
		}
		t := timedFindings{Document: d.Name, Summary: d.Review.Summary, Findings: append([]workflow.Finding{}, d.Review.Findings...),
			Suggestions: append([]workflow.Suggestion{}, d.Review.Suggestions...), Dispositions: append([]workflow.Disposition{}, d.Review.Dispositions...)}
		t.RecordedAt, _, _, t.seq = span(byRecord["document/"+d.Name], "put-document", "")
		out.Findings = append(out.Findings, t)
	}
	for _, r := range h.Rulings {
		t := timedRuling{Ruling: r, Contested: r.ContestedBy != ""}
		t.RuledAt, t.ContestedAt, _, t.seq = span(byRecord["ruling/"+r.Document+"/"+r.Finding], "rule-finding", "contest-ruling")
		out.Rulings = append(out.Rulings, t)
	}
	for _, f := range h.FullReviews {
		t := timedFullReview{FullReview: f}
		t.RequestedAt, t.ConsumedAt, _, _ = span(byRecord["full-review/"+strconv.Itoa(f.Number)], "request-full-review", "consume-full-review")
		out.FullReviews = append(out.FullReviews, t)
	}
	if o := h.DependencyOverride; o != nil {
		out.DependencyOverride = &timedOverride{DependencyOverride: *o}
		out.DependencyOverride.OverriddenAt, _, _, _ = span(byRecord[store.DependencyOverrideRecord], "override-dependencies", "")
	}
	out.Priority, out.PriorityChanges = store.DefaultPriority, []timedPriority{}
	if p := h.Priority; p != nil {
		out.Priority = p.Priority
		set := byRecord[store.TaskPriorityRecord]
		for i, level := range p.History {
			t := timedPriority{Priority: level}
			if i < len(set) {
				t.SetAt = &set[i].CommittedAt
			}
			out.PriorityChanges = append(out.PriorityChanges, t)
		}
	}
	out.MergeHolds = []timedHold{}
	if m := h.MergeHold; m != nil {
		set := byRecord[store.MergeHoldRecord]
		for i, held := range m.History {
			t := timedHold{Held: held}
			if i < len(set) {
				t.SetAt = &set[i].CommittedAt
			}
			out.MergeHolds = append(out.MergeHolds, t)
		}
	}
	slices.SortStableFunc(out.Attempts, func(a, b timedAttempt) int { return byStart(a.seq, b.seq) })
	slices.SortStableFunc(out.Decisions, func(a, b timedDecision) int { return byStart(a.seq, b.seq) })
	slices.SortStableFunc(out.Findings, func(a, b timedFindings) int { return byStart(a.seq, b.seq) })
	slices.SortStableFunc(out.Rulings, func(a, b timedRuling) int { return byStart(a.seq, b.seq) })
	return out
}

// mergedMessage reads the merged message the engine sent the Director for the task, the last when there are several,
// or nil when it has not merged.
func mergedMessage(s *store.Store, h store.TaskHistory) (*timedMerge, error) {
	prefix := "message/" + h.Task.Repository + "." + h.Task.ID + "." + store.MessageMerged + "."
	var out *timedMerge
	for _, r := range h.Receipts {
		id, ok := strings.CutPrefix(r.Record, "message/")
		if r.Operation != "send-message" || !ok || !strings.HasPrefix(r.Record, prefix) {
			continue
		}
		m, err := s.Message(id)
		if err != nil {
			return nil, err
		}
		out = &timedMerge{Message: id, Text: m.Text, MergedAt: r.CommittedAt}
	}
	return out, nil
}

func files(h store.TaskHistory) taskFiles {
	byRecord := recordReceipts(h.Receipts)
	out := taskFiles{AsOf: label(h), Documents: []timedDocument{}}
	for _, d := range h.Documents {
		t := timedDocument{DocumentRef: d}
		t.RecordedAt, _, _, _ = span(byRecord["document/"+d.Name], "put-document", "")
		out.Documents = append(out.Documents, t)
	}
	return out
}

// byStart orders by start sequence, with an unknown (zero) start after every known one.
func byStart(a, b uint64) int {
	if (a == 0) != (b == 0) {
		return cmp.Compare(b, a)
	}
	return cmp.Compare(a, b)
}
