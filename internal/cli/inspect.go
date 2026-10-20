package cli

import (
	"cmp"
	"slices"
	"strconv"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// inspectLabel says what an inspection read describes: the store state as of the newest committed
// mutation, and the workflow revision the task is bound to (docs/tasks.md#inspection).
type inspectLabel struct {
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

// taskTimeline is what task timeline prints. Events are every receipt of the task in commit order:
// the moves made on it, including those Guildhall records for the Director.
type taskTimeline struct {
	AsOf      inspectLabel    `json:"as_of"`
	Task      store.Task      `json:"task"`
	Attempts  []timedAttempt  `json:"attempts"`
	Decisions []timedDecision `json:"decisions"`
	Events    []store.Receipt `json:"events"`
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
	return inspectLabel{Sequence: h.AsOf.Sequence, CommittedAt: h.AsOf.CommittedAt, Workflow: h.Task.Workflow}
}

// span returns the commit times of a record's first and second receipts and the seconds between them,
// and the first receipt's sequence, or zero when the record has none.
func span(rs []store.Receipt) (start, end *time.Time, seconds *float64, seq uint64) {
	if len(rs) > 0 {
		start, seq = &rs[0].CommittedAt, rs[0].Sequence
	}
	if len(rs) > 1 {
		end = &rs[1].CommittedAt
		d := end.Sub(*start).Seconds()
		seconds = &d
	}
	return start, end, seconds, seq
}

// timeline orders attempts and decisions by when they began; those without a known start keep
// the store's order after the timed ones.
func timeline(h store.TaskHistory) taskTimeline {
	byRecord := recordReceipts(h.Receipts)
	out := taskTimeline{AsOf: label(h), Task: h.Task, Attempts: []timedAttempt{}, Decisions: []timedDecision{}, Events: h.Receipts}
	for _, a := range h.Attempts {
		t := timedAttempt{StageAttempt: a}
		t.StartedAt, t.FinishedAt, t.DurationSeconds, t.seq = span(byRecord["attempt/"+a.Step+"/"+strconv.Itoa(a.Number)])
		out.Attempts = append(out.Attempts, t)
	}
	for _, d := range h.Decisions {
		t := timedDecision{Decision: d}
		t.OpenedAt, t.AnsweredAt, t.WaitSeconds, t.seq = span(byRecord["decision/"+d.ID])
		out.Decisions = append(out.Decisions, t)
	}
	slices.SortStableFunc(out.Attempts, func(a, b timedAttempt) int { return byStart(a.seq, b.seq) })
	slices.SortStableFunc(out.Decisions, func(a, b timedDecision) int { return byStart(a.seq, b.seq) })
	return out
}

func files(h store.TaskHistory) taskFiles {
	byRecord := recordReceipts(h.Receipts)
	out := taskFiles{AsOf: label(h), Documents: []timedDocument{}}
	for _, d := range h.Documents {
		t := timedDocument{DocumentRef: d}
		t.RecordedAt, _, _, _ = span(byRecord["document/"+d.Name])
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
