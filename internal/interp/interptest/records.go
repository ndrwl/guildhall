package interptest

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Records is the in-memory interp.Records double. It keeps the store's replay, conflict, numbering and
// once-only rules and its sentinel errors, and passes RunRecordsContract with cli.storeRecords and *store.Store.
// It does not repeat the store's field validation beyond a decision's subject.
type Records struct {
	// Now stamps receipts and so decides the day StartCappedAttempt's daily cap counts; nil is wall time.
	Now       func() time.Time
	mu        sync.Mutex
	seq       uint64
	receipts  map[string]receipt
	tasks     map[[2]string]store.Task
	attempts  []store.StageAttempt
	documents []store.DocumentRef
	decisions []store.Decision
	rulings   []store.Ruling
	full      []store.FullReview
	contexts  []store.TaskContext
	links     []store.TaskLink
	// taskOverrides are the tasks' override records; overrides their dependency overrides.
	taskOverrides []store.TaskOverride
	overrides     map[[2]string]store.DependencyOverride
	grants        []store.TaskGrant
	// priorities are the tasks' priorities SetTaskPriority sets, and queue the start queue's entries by
	// repository, task and step.
	priorities map[[2]string]store.Priority
	queue      map[[3]string]store.StartQueueEntry
	// held is the Director's hold PauseDirector sets.
	held bool
}

type receipt struct {
	op, req string
	r       store.Receipt
}

// NewRecords returns empty records.
func NewRecords() *Records {
	return &Records{receipts: map[string]receipt{}, tasks: map[[2]string]store.Task{}}
}

var _ interp.Records = (*Records)(nil)

// CreateTask records an open task, as store.CreateTask does.
func (r *Records) CreateTask(mutationID string, t store.Task) (store.Receipt, error) {
	t.State = store.TaskOpen
	return r.mutate(mutationID, "create-task", t, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = t.Repository, t.ID, "task"
		if _, ok := r.tasks[[2]string{t.Repository, t.ID}]; ok {
			return fmt.Errorf("%w: task %s/%s", store.ErrExists, t.Repository, t.ID)
		}
		r.tasks[[2]string{t.Repository, t.ID}] = t
		return nil
	})
}

// AnswerDecision records an answer, as store.AnswerDecision does.
func (r *Records) AnswerDecision(mutationID, repository, taskID, id, answer string) (store.Receipt, error) {
	req := store.Decision{Repository: repository, TaskID: taskID, ID: id, Answered: true, Answer: answer}
	return r.mutate(mutationID, "answer-decision", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, "decision/"+id
		i := slices.IndexFunc(r.decisions, func(d store.Decision) bool {
			return d.Repository == repository && d.TaskID == taskID && d.ID == id
		})
		if i < 0 {
			return fmt.Errorf("decision %q: %w", id, store.ErrNotFound)
		}
		d := &r.decisions[i]
		if d.Answered {
			return fmt.Errorf("%w: decision %q is already answered", store.ErrInvalid, id)
		}
		if d.Void {
			return fmt.Errorf("%w: decision %q is void", store.ErrInvalid, id)
		}
		if len(d.Options) > 0 {
			first, rest, _ := strings.Cut(answer, "\n")
			first = strings.TrimSuffix(first, "\r")
			switch {
			case slices.Contains(d.Options, first):
				d.Choice, d.Note = first, strings.TrimSpace(rest)
			case d.Kind != store.DecisionQuestion:
				return fmt.Errorf("%w: answer %q is not one of %q", store.ErrInvalid, first, d.Options)
			}
		}
		d.Answered, d.Answer = true, answer
		return nil
	})
}

// VoidDecision voids an unanswered decision, as store.VoidDecision does.
func (r *Records) VoidDecision(mutationID, repository, taskID, id, reason string) (store.Receipt, error) {
	if strings.TrimSpace(reason) == "" {
		return store.Receipt{}, fmt.Errorf("%w: void reason must not be empty", store.ErrInvalid)
	}
	req := struct{ Repository, TaskID, ID, Reason string }{repository, taskID, id, reason}
	return r.mutate(mutationID, "void-decision", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, "decision/"+id
		i := slices.IndexFunc(r.decisions, func(d store.Decision) bool {
			return d.Repository == repository && d.TaskID == taskID && d.ID == id
		})
		if i < 0 {
			return fmt.Errorf("decision %q: %w", id, store.ErrNotFound)
		}
		d := &r.decisions[i]
		switch {
		case d.Void:
			body, _ := json.Marshal(req)
			for _, prior := range r.receipts {
				if prior.op == "void-decision" && prior.req == string(body) {
					return standing{prior.r}
				}
			}
			return fmt.Errorf("%w: decision %q is already void", store.ErrInvalid, id)
		case d.Answered:
			return fmt.Errorf("%w: decision %q is answered", store.ErrInvalid, id)
		}
		d.Void, d.VoidReason = true, reason
		return nil
	})
}

// CancelTask cancels an open task with its reason and the receipt's time, as store.CancelTask does.
func (r *Records) CancelTask(mutationID, repository, taskID, reason string) (store.Receipt, error) {
	if strings.TrimSpace(reason) == "" {
		return store.Receipt{}, fmt.Errorf("%w: cancel reason must not be empty", store.ErrInvalid)
	}
	req := struct{ Repository, TaskID, Reason string }{repository, taskID, reason}
	return r.mutate(mutationID, "cancel-task", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, "task"
		t, ok := r.tasks[[2]string{repository, taskID}]
		switch {
		case !ok:
			return fmt.Errorf("task %s/%s: %w", repository, taskID, store.ErrNotFound)
		case t.State != store.TaskOpen:
			return fmt.Errorf("%w: task %s/%s is %s", store.ErrInvalid, repository, taskID, t.State)
		}
		t.State, t.CancelReason, t.CancelledAt = store.TaskCancelled, reason, r.now()
		r.tasks[[2]string{repository, taskID}] = t
		return nil
	})
}

// RuleFinding records a ruling once per document and finding, as store.RuleFinding does, without its
// checks of the document's findings.
func (r *Records) RuleFinding(mutationID string, ru store.Ruling) (store.Receipt, error) {
	return r.mutate(mutationID, "rule-finding", ru, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = ru.Repository, ru.TaskID, "ruling/"+ru.Document+"/"+ru.Finding
		if _, ok := r.tasks[[2]string{ru.Repository, ru.TaskID}]; !ok {
			return fmt.Errorf("task %s/%s: %w", ru.Repository, ru.TaskID, store.ErrNotFound)
		}
		if r.ruling(ru.Repository, ru.TaskID, ru.Document, ru.Finding) != nil {
			return fmt.Errorf("%w: ruling on %q of %q", store.ErrExists, ru.Finding, ru.Document)
		}
		r.rulings = append(r.rulings, ru)
		slices.SortFunc(r.rulings, func(x, y store.Ruling) int {
			return cmp.Or(cmp.Compare(x.Document, y.Document), cmp.Compare(x.Finding, y.Finding))
		})
		return nil
	})
}

func (r *Records) ruling(repository, taskID, document, finding string) *store.Ruling {
	for i, ru := range r.rulings {
		if ru.Repository == repository && ru.TaskID == taskID && ru.Document == document && ru.Finding == finding {
			return &r.rulings[i]
		}
	}
	return nil
}

// Task implements interp.Records.
func (r *Records) Task(repository, taskID string) (store.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[[2]string{repository, taskID}]
	if !ok {
		return store.Task{}, fmt.Errorf("task %s/%s: %w", repository, taskID, store.ErrNotFound)
	}
	return t, nil
}

// Attempts implements interp.Records.
func (r *Records) Attempts(repository, taskID string) ([]store.StageAttempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return list(r, repository, taskID, r.attempts, func(a store.StageAttempt) (string, string) { return a.Repository, a.TaskID })
}

// Documents implements interp.Records.
func (r *Records) Documents(repository, taskID string) ([]store.DocumentRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return list(r, repository, taskID, r.documents, func(d store.DocumentRef) (string, string) { return d.Repository, d.TaskID })
}

// Decisions implements interp.Records.
func (r *Records) Decisions(repository, taskID string) ([]store.Decision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return list(r, repository, taskID, r.decisions, func(d store.Decision) (string, string) { return d.Repository, d.TaskID })
}

// Rulings implements interp.Records.
func (r *Records) Rulings(repository, taskID string) ([]store.Ruling, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return list(r, repository, taskID, r.rulings, func(ru store.Ruling) (string, string) { return ru.Repository, ru.TaskID })
}

// ContestRuling implements interp.Records.
func (r *Records) ContestRuling(mutationID, repository, taskID, document, finding, by string) (store.Receipt, error) {
	req := store.Ruling{Repository: repository, TaskID: taskID, Document: document, Finding: finding, ContestedBy: by}
	return r.mutate(mutationID, "contest-ruling", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, "ruling/"+document+"/"+finding
		ru := r.ruling(repository, taskID, document, finding)
		switch {
		case ru == nil:
			return fmt.Errorf("ruling on %q of %q: %w", finding, document, store.ErrNotFound)
		case ru.ContestedBy != "":
			return fmt.Errorf("%w: ruling on %q of %q was contested by %s", store.ErrInvalid, finding, document, ru.ContestedBy)
		}
		ru.ContestedBy = by
		return nil
	})
}

// RequestFullReview records the next full-review request, as store.RequestFullReview does.
func (r *Records) RequestFullReview(mutationID, repository, taskID string) (store.Receipt, error) {
	req := store.FullReview{Repository: repository, TaskID: taskID}
	return r.mutate(mutationID, "request-full-review", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		if _, ok := r.tasks[[2]string{repository, taskID}]; !ok {
			return fmt.Errorf("task %s/%s: %w", repository, taskID, store.ErrNotFound)
		}
		for _, f := range r.full {
			if f.Repository == repository && f.TaskID == taskID {
				if f.ConsumedBy == "" {
					return fmt.Errorf("%w: full review %d is pending", store.ErrExists, f.Number)
				}
				req.Number = f.Number
			}
		}
		req.Number++
		rc.Record = fmt.Sprintf("full-review/%d", req.Number)
		r.full = append(r.full, req)
		return nil
	})
}

// AddTaskContext records the task's next context entry on an open task, as store.AddTaskContext does, without its
// checks of the text.
func (r *Records) AddTaskContext(mutationID, repository, taskID, text string) (store.Receipt, error) {
	req := store.TaskContext{Repository: repository, TaskID: taskID, Text: text, Author: store.TaskContextAuthor}
	return r.mutate(mutationID, "add-task-context", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		t, ok := r.tasks[[2]string{repository, taskID}]
		switch {
		case !ok:
			return fmt.Errorf("task %s/%s: %w", repository, taskID, store.ErrNotFound)
		case t.State != store.TaskOpen:
			return fmt.Errorf("%w: task %s/%s is %s", store.ErrInvalid, repository, taskID, t.State)
		}
		for _, c := range r.contexts {
			if c.Repository == repository && c.TaskID == taskID {
				req.Number = c.Number
			}
		}
		req.Number++
		rc.CommittedAt = r.now()
		req.AddedAt, rc.Record = rc.CommittedAt, store.TaskContextRecord(req.Number)
		r.contexts = append(r.contexts, req)
		return nil
	})
}

// TaskContexts implements interp.Records.
func (r *Records) TaskContexts(repository, taskID string) ([]store.TaskContext, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return list(r, repository, taskID, r.contexts, func(c store.TaskContext) (string, string) { return c.Repository, c.TaskID })
}

// AddTaskLink records the task's next link record on an open task, as store.AddTaskLink does, without its checks of
// the ids and the text.
func (r *Records) AddTaskLink(mutationID, repository, taskID string, op store.TaskLinkOp, source store.DocumentRef, prior int, text string) (store.Receipt, error) {
	src := store.TaskLinkSource(source)
	req := struct {
		Repository, TaskID, Source, Text string
		Op                               store.TaskLinkOp
		Prior                            int
	}{repository, taskID, src, text, op, prior}
	return r.mutate(mutationID, "add-task-link", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		t, ok := r.tasks[[2]string{repository, taskID}]
		switch {
		case !ok:
			return fmt.Errorf("task %s/%s: %w", repository, taskID, store.ErrNotFound)
		case t.State != store.TaskOpen:
			return fmt.Errorf("%w: task %s/%s is %s", store.ErrInvalid, repository, taskID, t.State)
		case source.Repository == repository && source.TaskID == taskID:
			return fmt.Errorf("%w: task %s/%s cannot link its own document", store.ErrInvalid, repository, taskID)
		}
		i := slices.IndexFunc(r.documents, func(d store.DocumentRef) bool { return store.TaskLinkSource(d) == src })
		if i < 0 {
			return fmt.Errorf("source document %s: %w", src, store.ErrNotFound)
		}
		doc := r.documents[i]
		var mine []store.TaskLink
		n := 0
		for _, l := range r.links {
			if l.Repository == repository && l.TaskID == taskID {
				n = l.Number
				if store.TaskLinkSource(l.Source) == src {
					mine = append(mine, l)
				}
			}
		}
		linked := len(mine) > 0 && mine[len(mine)-1].Op == store.TaskLinkAdd
		switch {
		case len(mine) != prior:
			return fmt.Errorf("%w: %d link records for %s, not %d", store.ErrExists, len(mine), src, prior)
		case op == store.TaskLinkAdd && linked:
			return fmt.Errorf("%w: already links %s", store.ErrExists, src)
		case op == store.TaskLinkRemove && !linked:
			return fmt.Errorf("%w: does not link %s", store.ErrInvalid, src)
		case op != store.TaskLinkAdd && op != store.TaskLinkRemove:
			return fmt.Errorf("%w: task link op %q", store.ErrInvalid, op)
		}
		l := store.TaskLink{Repository: repository, TaskID: taskID, Number: n + 1, Op: op, Text: text, Author: store.TaskLinkAuthor,
			Source: store.DocumentRef{Repository: doc.Repository, TaskID: doc.TaskID, Name: doc.Name, Path: doc.Path, SHA256: doc.SHA256}}
		rc.CommittedAt = r.now()
		l.AddedAt, rc.Record = rc.CommittedAt, store.TaskLinkRecord(l.Number)
		r.links = append(r.links, l)
		return nil
	})
}

// TaskLinks implements interp.Records.
func (r *Records) TaskLinks(repository, taskID string) ([]store.TaskLink, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return list(r, repository, taskID, r.links, func(l store.TaskLink) (string, string) { return l.Repository, l.TaskID })
}

// AddTaskOverride records the task's next override record on an open task, as store.AddTaskOverride does, without
// its checks of the ids, the key, the value and the text.
func (r *Records) AddTaskOverride(mutationID, repository, taskID string, op store.TaskOverrideOp, key, value string, prior int, text string) (store.Receipt, error) {
	req := struct {
		Repository, TaskID, Key, Value, Text string
		Op                                   store.TaskOverrideOp
		Prior                                int
	}{repository, taskID, key, value, text, op, prior}
	return r.mutate(mutationID, "add-task-override", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		t, ok := r.tasks[[2]string{repository, taskID}]
		switch {
		case !ok:
			return fmt.Errorf("task %s/%s: %w", repository, taskID, store.ErrNotFound)
		case t.State != store.TaskOpen:
			return fmt.Errorf("%w: task %s/%s is %s", store.ErrInvalid, repository, taskID, t.State)
		case op != store.TaskOverrideSet && op != store.TaskOverrideUnset:
			return fmt.Errorf("%w: task override op %q", store.ErrInvalid, op)
		}
		var mine []store.TaskOverride
		n := 0
		for _, o := range r.taskOverrides {
			if o.Repository == repository && o.TaskID == taskID {
				n = o.Number
				if o.Key == key {
					mine = append(mine, o)
				}
			}
		}
		switch {
		case len(mine) != prior:
			return fmt.Errorf("%w: %d override records for %s, not %d", store.ErrExists, len(mine), key, prior)
		case op == store.TaskOverrideUnset && (len(mine) == 0 || mine[len(mine)-1].Op != store.TaskOverrideSet):
			return fmt.Errorf("%w: no override of %s in effect", store.ErrInvalid, key)
		}
		o := store.TaskOverride{Repository: repository, TaskID: taskID, Number: n + 1, Op: op, Key: key, Value: value, Text: text, Author: store.TaskOverrideAuthor}
		rc.CommittedAt = r.now()
		o.AddedAt, rc.Record = rc.CommittedAt, store.TaskOverrideRecord(o.Number)
		r.taskOverrides = append(r.taskOverrides, o)
		return nil
	})
}

// TaskOverrides implements interp.Records.
func (r *Records) TaskOverrides(repository, taskID string) ([]store.TaskOverride, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return list(r, repository, taskID, r.taskOverrides, func(o store.TaskOverride) (string, string) { return o.Repository, o.TaskID })
}

// Tasks implements interp.Records.
func (r *Records) Tasks(repository string) ([]store.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []store.Task
	for k, t := range r.tasks {
		if k[0] == repository {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b store.Task) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// TaskCloses implements interp.Records, as store.TaskCloses does: the sequence of each task's create-task receipt,
// and the sequence and commit time of its first set-task-state or cancel-task receipt.
func (r *Records) TaskCloses() (map[store.TaskRef]store.TaskClose, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[store.TaskRef]store.TaskClose{}
	for _, rc := range r.receipts {
		ref := store.TaskRef{Repository: rc.r.Repository, ID: rc.r.TaskID}
		c := out[ref]
		switch rc.op {
		case "create-task":
			c.Created = rc.r.Sequence
		case "set-task-state", "cancel-task":
			if c.Closed != 0 && c.Closed < rc.r.Sequence {
				continue
			}
			c.Closed, c.At = rc.r.Sequence, rc.r.CommittedAt
		default:
			continue
		}
		out[ref] = c
	}
	return out, nil
}

// FullReviews implements interp.Records.
func (r *Records) FullReviews(repository, taskID string) ([]store.FullReview, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return list(r, repository, taskID, r.full, func(f store.FullReview) (string, string) { return f.Repository, f.TaskID })
}

// ConsumeFullReview implements interp.Records.
func (r *Records) ConsumeFullReview(mutationID, repository, taskID string, number int, by string) (store.Receipt, error) {
	req := store.FullReview{Repository: repository, TaskID: taskID, Number: number, ConsumedBy: by}
	return r.mutate(mutationID, "consume-full-review", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, fmt.Sprintf("full-review/%d", number)
		i := slices.IndexFunc(r.full, func(f store.FullReview) bool {
			return f.Repository == repository && f.TaskID == taskID && f.Number == number
		})
		switch {
		case i < 0:
			return fmt.Errorf("full review %d: %w", number, store.ErrNotFound)
		case r.full[i].ConsumedBy != "":
			return fmt.Errorf("%w: full review %d was consumed by %s", store.ErrInvalid, number, r.full[i].ConsumedBy)
		}
		r.full[i].ConsumedBy = by
		return nil
	})
}

// OverrideDependencies records a task's dependency override once, as store.OverrideDependencies does, without its
// checks of the reason's text.
func (r *Records) OverrideDependencies(mutationID, repository, taskID, reason string) (store.Receipt, error) {
	o := store.DependencyOverride{Repository: repository, TaskID: taskID, Reason: reason}
	return r.mutate(mutationID, "override-dependencies", o, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, store.DependencyOverrideRecord
		t, ok := r.tasks[[2]string{repository, taskID}]
		switch {
		case !ok:
			return fmt.Errorf("task %s/%s: %w", repository, taskID, store.ErrNotFound)
		case t.State != store.TaskOpen:
			return fmt.Errorf("%w: task %s/%s is %s", store.ErrInvalid, repository, taskID, t.State)
		case len(t.DependsOn) == 0:
			return fmt.Errorf("%w: task %s/%s has no dependencies to override", store.ErrInvalid, repository, taskID)
		}
		if _, ok := r.overrides[[2]string{repository, taskID}]; ok {
			return fmt.Errorf("%w: dependency override of %s/%s", store.ErrExists, repository, taskID)
		}
		if r.overrides == nil {
			r.overrides = map[[2]string]store.DependencyOverride{}
		}
		r.overrides[[2]string{repository, taskID}] = o
		return nil
	})
}

// DependencyOverride implements interp.Records.
func (r *Records) DependencyOverride(repository, taskID string) (store.DependencyOverride, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.overrides[[2]string{repository, taskID}]
	if !ok {
		return store.DependencyOverride{}, fmt.Errorf("dependency override of %s/%s: %w", repository, taskID, store.ErrNotFound)
	}
	return o, nil
}

func list[T any](r *Records, repository, taskID string, all []T, key func(T) (string, string)) ([]T, error) {
	if _, ok := r.tasks[[2]string{repository, taskID}]; !ok {
		return nil, nil
	}
	var out []T
	for _, v := range all {
		if repo, task := key(v); repo == repository && task == taskID {
			out = append(out, v)
		}
	}
	return out, nil
}

// SetTaskState implements interp.Records.
func (r *Records) SetTaskState(mutationID, repository, taskID string, state store.TaskState) (store.Receipt, error) {
	if state != store.TaskDone && state != store.TaskCancelled {
		return store.Receipt{}, fmt.Errorf("%w: task state %q", store.ErrInvalid, state)
	}
	req := store.Task{Repository: repository, ID: taskID, State: state}
	return r.mutate(mutationID, "set-task-state", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, "task"
		t, ok := r.tasks[[2]string{repository, taskID}]
		switch {
		case !ok:
			return fmt.Errorf("task %s/%s: %w", repository, taskID, store.ErrNotFound)
		case t.State != store.TaskOpen:
			return fmt.Errorf("%w: task %s/%s is %s", store.ErrInvalid, repository, taskID, t.State)
		}
		t.State = state
		r.tasks[[2]string{repository, taskID}] = t
		return nil
	})
}

// attemptTask refuses a task an attempt of step does not start on, as the store does: an unknown task, and one that
// is not open, or for the judge step one that is not done.
func (r *Records) attemptTask(repository, taskID, step string) error {
	t, ok := r.tasks[[2]string{repository, taskID}]
	want := store.TaskOpen
	if step == workflow.JudgeStep {
		want = store.TaskDone
	}
	switch {
	case !ok:
		return fmt.Errorf("task %s/%s: %w", repository, taskID, store.ErrNotFound)
	case t.State != want:
		return fmt.Errorf("%w: task %s/%s is %s", store.ErrInvalid, repository, taskID, t.State)
	}
	return nil
}

// StartAttempt implements interp.Records.
func (r *Records) StartAttempt(mutationID, repository, taskID, step string) (store.Receipt, error) {
	req := store.StageAttempt{Repository: repository, TaskID: taskID, Step: step}
	return r.mutate(mutationID, "start-attempt", req, func(rc *store.Receipt) error { return r.startAttempt(rc, repository, taskID, step, false) })
}

// StartDirectorAttempt implements interp.Records: a Director stage's attempt, flagged and never capped or queued.
func (r *Records) StartDirectorAttempt(mutationID, repository, taskID, step string, hold bool) (store.Receipt, error) {
	req := store.StageAttempt{Repository: repository, TaskID: taskID, Step: step}
	return r.mutate(mutationID, "start-attempt", req, func(rc *store.Receipt) error {
		if hold && r.held {
			return fmt.Errorf("%w: the double is paused", store.ErrDirectorHeld)
		}
		return r.startAttempt(rc, repository, taskID, step, true)
	})
}

func (r *Records) startAttempt(rc *store.Receipt, repository, taskID, step string, director bool) error {
	rc.Repository, rc.TaskID = repository, taskID
	if err := r.attemptTask(repository, taskID, step); err != nil {
		return err
	}
	n := 0
	for _, a := range r.attempts {
		if a.Repository == repository && a.TaskID == taskID && a.Step == step {
			if a.Outcome == "" {
				return fmt.Errorf("%w: attempt %d of step %q is still running", store.ErrInvalid, a.Number, step)
			}
			n = max(n, a.Number)
		}
	}
	a := store.StageAttempt{Repository: repository, TaskID: taskID, Step: step, Number: n + 1, Director: director}
	rc.Attempt, rc.Record = a.Number, fmt.Sprintf("attempt/%s/%d", step, a.Number)
	r.attempts = append(r.attempts, a)
	slices.SortFunc(r.attempts, func(x, y store.StageAttempt) int {
		return cmp.Or(cmp.Compare(x.Step, y.Step), cmp.Compare(x.Number, y.Number))
	})
	return nil
}

// PauseDirector holds new attempts as a paused Director session does: a later StartCappedAttempt with
// AttemptCaps.Hold is refused with store.ErrDirectorHeld.
func (r *Records) PauseDirector() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.held = true
	return nil
}

// PauseStoreDirector records a paused Director session in s, a new session after the newest, as
// RecordsHarness.PauseDirector does for a store.
func PauseStoreDirector(s *store.Store) error {
	n := 1
	if d, err := s.LatestDirectorSession(); err == nil {
		n = d.Number + 1
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	rc, err := s.StartDirectorSession(store.DirectorSession{Number: n, State: store.DirectorRecovering,
		Identity: store.ProcessIdentity{PID: 1, Token: "interptest"}, Release: "interptest", ConfigDigest: "sha256:" + strings.Repeat("c", 64)})
	if err == nil {
		rc, err = s.SetDirectorState(n, store.DirectorRunning, rc.Sequence)
	}
	if err == nil {
		_, err = s.SetDirectorState(n, store.DirectorPaused, rc.Sequence)
	}
	return err
}

// StartCappedAttempt implements interp.Records, refusing an unknown or closed task before it counts and counting
// with store.CountUsage by Now.
func (r *Records) StartCappedAttempt(mutationID, repository, taskID, step string, caps store.AttemptCaps) (store.Receipt, error) {
	req := store.StageAttempt{Repository: repository, TaskID: taskID, Step: step}
	if caps.Queue != nil {
		return r.startQueued(mutationID, req, caps)
	}
	return r.mutate(mutationID, "start-attempt", req, func(rc *store.Receipt) error {
		if caps.Hold && r.held {
			return fmt.Errorf("%w: the double is paused", store.ErrDirectorHeld)
		}
		if caps.MaxRunning > 0 || caps.MaxPerDay > 0 {
			if err := r.attemptTask(repository, taskID, step); err != nil {
				return err
			}
			u := r.usage()
			switch {
			case caps.MaxRunning > 0 && u.Running >= caps.MaxRunning:
				return &store.CapRefusal{Cap: store.CapRunning, Count: u.Running, Limit: caps.MaxRunning, Day: u.Day}
			case caps.MaxPerDay > 0 && u.Today >= caps.MaxPerDay:
				return &store.CapRefusal{Cap: store.CapDaily, Count: u.Today, Limit: caps.MaxPerDay, Day: u.Day}
			}
		}
		return r.startAttempt(rc, repository, taskID, step, false)
	})
}

// usage counts what the caps count with store.CountUsage by Now; r.mu is held.
func (r *Records) usage() store.AttemptUsage {
	receipts := func(yield func(store.Receipt) bool) {
		for _, prior := range r.receipts {
			if !yield(prior.r) {
				return
			}
		}
	}
	return store.CountUsage(receipts, slices.Values(r.attempts), r.now())
}

// startQueued is StartCappedAttempt with caps.Queue, as the store admits or queues it in one write.
func (r *Records) startQueued(mutationID string, req store.StageAttempt, caps store.AttemptCaps) (store.Receipt, error) {
	repository, taskID, step := req.Repository, req.TaskID, req.Step
	body, err := json.Marshal(req)
	if err != nil {
		return store.Receipt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if prior, ok := r.receipts[mutationID]; ok {
		if prior.op != "start-attempt" || prior.req != string(body) {
			return store.Receipt{}, fmt.Errorf("records: %w: %q was %s", store.ErrMutationConflict, mutationID, prior.op)
		}
		return prior.r, nil
	}
	fail := func(err error) (store.Receipt, error) {
		return store.Receipt{}, fmt.Errorf("records: start-attempt %s: %w", mutationID, err)
	}
	if caps.Hold && r.held {
		return fail(fmt.Errorf("%w: the double is paused", store.ErrDirectorHeld))
	}
	if err := r.attemptTask(repository, taskID, step); err != nil {
		return fail(err)
	}
	u := r.usage()
	if caps.MaxPerDay > 0 && u.Today >= caps.MaxPerDay {
		return fail(&store.CapRefusal{Cap: store.CapDaily, Count: u.Today, Limit: caps.MaxPerDay, Day: u.Day})
	}
	if caps.MaxRunning > 0 {
		q := r.startQueue()
		if u.Running >= caps.MaxRunning || len(q) > 0 && !q[0].Is(repository, taskID, step) {
			return store.Receipt{}, r.enqueue(repository, taskID, step, *caps.Queue)
		}
	}
	rc := store.Receipt{MutationID: mutationID, Operation: "start-attempt", Sequence: r.seq + 1}
	if err := r.startAttempt(&rc, repository, taskID, step, false); err != nil {
		return fail(err)
	}
	delete(r.queue, [3]string{repository, taskID, step})
	r.seq++
	rc.CommittedAt = r.now()
	r.receipts[mutationID] = receipt{"start-attempt", string(body), rc}
	return rc, nil
}

// enqueue writes or keeps a step's queue entry, as the store does, and returns its *store.StartQueued; r.mu is held.
func (r *Records) enqueue(repository, taskID, step string, req store.StartRequest) error {
	n := r.lastAttempt(repository, taskID, step) + 1
	key := [3]string{repository, taskID, step}
	e, ok := r.queue[key]
	if !ok || e.Attempt != n {
		e = store.StartQueueEntry{Repository: repository, TaskID: taskID, Step: step, Attempt: n, StartRequest: req, QueuedAt: r.now()}
	}
	if r.queue == nil {
		r.queue = map[[3]string]store.StartQueueEntry{}
	}
	r.queue[key] = e
	id := store.QueueStartMutation(repository, taskID, step, n)
	if _, ok := r.receipts[id]; !ok {
		r.seq++
		r.receipts[id] = receipt{"queue-start", id, store.Receipt{MutationID: id, Operation: "queue-start", Repository: repository, TaskID: taskID,
			Attempt: n, Record: store.StartQueueRecord(step), Sequence: r.seq, CommittedAt: r.now()}}
	}
	q := r.startQueue()
	return &store.StartQueued{Entry: e, Position: slices.IndexFunc(q, func(qs store.QueuedStart) bool { return qs.Is(repository, taskID, step) }) + 1}
}

func (r *Records) lastAttempt(repository, taskID, step string) int {
	n := 0
	for _, a := range r.attempts {
		if a.Repository == repository && a.TaskID == taskID && a.Step == step {
			n = max(n, a.Number)
		}
	}
	return n
}

// startQueue is the queue as the store reads it: entries of open tasks whose attempt is not yet recorded, with live
// priorities, in store.OrderStartQueue order; r.mu is held.
func (r *Records) startQueue() []store.QueuedStart {
	q := []store.QueuedStart{}
	for key, e := range r.queue {
		if r.tasks[[2]string{key[0], key[1]}].State != store.TaskOpen || r.lastAttempt(key[0], key[1], key[2]) >= e.Attempt {
			continue
		}
		p, ok := r.priorities[[2]string{key[0], key[1]}]
		if !ok {
			p = store.DefaultPriority
		}
		q = append(q, store.QueuedStart{StartQueueEntry: e, Priority: p})
	}
	store.OrderStartQueue(q)
	return q
}

// StartQueue implements interp.Records.
func (r *Records) StartQueue() ([]store.QueuedStart, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startQueue(), nil
}

// SetTaskPriority records an open task's priority, as store.SetTaskPriority does, without its history.
func (r *Records) SetTaskPriority(mutationID, repository, taskID string, p store.Priority) (store.Receipt, error) {
	req := store.TaskPriority{Repository: repository, TaskID: taskID, Priority: p}
	return r.mutate(mutationID, "set-task-priority", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, store.TaskPriorityRecord
		if t, ok := r.tasks[[2]string{repository, taskID}]; !ok {
			return fmt.Errorf("task %s/%s: %w", repository, taskID, store.ErrNotFound)
		} else if t.State != store.TaskOpen {
			return fmt.Errorf("%w: task %s/%s is %s", store.ErrInvalid, repository, taskID, t.State)
		}
		if r.priorities == nil {
			r.priorities = map[[2]string]store.Priority{}
		}
		r.priorities[[2]string{repository, taskID}] = p
		return nil
	})
}

func (r *Records) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// FinishAttempt implements interp.Records.
func (r *Records) FinishAttempt(mutationID, repository, taskID, step string, number int, outcome store.AttemptOutcome) (store.Receipt, error) {
	req := store.StageAttempt{Repository: repository, TaskID: taskID, Step: step, Number: number, Outcome: outcome}
	return r.mutate(mutationID, "finish-attempt", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Attempt, rc.Record = repository, taskID, number, fmt.Sprintf("attempt/%s/%d", step, number)
		i := slices.IndexFunc(r.attempts, func(a store.StageAttempt) bool {
			return a.Repository == repository && a.TaskID == taskID && a.Step == step && a.Number == number
		})
		if i < 0 {
			return fmt.Errorf("attempt %d of step %q: %w", number, step, store.ErrNotFound)
		}
		if recorded := r.attempts[i].Outcome; recorded != "" {
			for _, prior := range r.receipts {
				if recorded == outcome && prior.op == "finish-attempt" && prior.r.Repository == rc.Repository && prior.r.TaskID == rc.TaskID && prior.r.Record == rc.Record {
					return standing{prior.r}
				}
			}
			return fmt.Errorf("%w: attempt %d of step %q already finished", store.ErrInvalid, number, step)
		}
		r.attempts[i].Outcome = outcome
		return nil
	})
}

// RejectAttempt implements interp.Records.
func (r *Records) RejectAttempt(mutationID, repository, taskID, step string, number int, reason string) (store.Receipt, error) {
	if number < 1 || strings.TrimSpace(reason) == "" {
		return store.Receipt{}, fmt.Errorf("%w: rejection needs positive attempt and reason", store.ErrInvalid)
	}
	req := store.StageAttempt{Repository: repository, TaskID: taskID, Step: step, Number: number, Outcome: store.AttemptFailed, IntakeRejection: reason}
	return r.mutate(mutationID, "reject-attempt", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Attempt, rc.Record = repository, taskID, number, fmt.Sprintf("attempt/%s/%d", step, number)
		for i, a := range r.attempts {
			if a.Repository != repository || a.TaskID != taskID || a.Step != step || a.Number != number {
				continue
			}
			if a.Outcome != "" && a.Outcome != store.AttemptSucceeded {
				return fmt.Errorf("%w: attempt already finished", store.ErrInvalid)
			}
			r.attempts[i].Outcome, r.attempts[i].IntakeRejection = store.AttemptFailed, reason
			return nil
		}
		return store.ErrNotFound
	})
}

// PutDocument implements interp.Records.
func (r *Records) PutDocument(mutationID string, d store.DocumentRef) (store.Receipt, error) {
	return r.mutate(mutationID, "put-document", d, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = d.Repository, d.TaskID, "document/"+d.Name
		if _, ok := r.tasks[[2]string{d.Repository, d.TaskID}]; !ok {
			return fmt.Errorf("task %s/%s: %w", d.Repository, d.TaskID, store.ErrNotFound)
		}
		if slices.ContainsFunc(r.documents, func(o store.DocumentRef) bool {
			return o.Repository == d.Repository && o.TaskID == d.TaskID && o.Name == d.Name
		}) {
			return fmt.Errorf("%w: document %q", store.ErrExists, d.Name)
		}
		r.documents = append(r.documents, d)
		slices.SortFunc(r.documents, func(x, y store.DocumentRef) int { return cmp.Compare(x.Name, y.Name) })
		return nil
	})
}

// OpenDecision implements interp.Records.
func (r *Records) OpenDecision(mutationID string, d store.Decision) (store.Receipt, error) {
	if d.Kind != store.DecisionQuestion && d.Kind != store.DecisionGrant && d.Subject == nil {
		return store.Receipt{}, fmt.Errorf("%w: a %s decision needs a subject", store.ErrInvalid, d.Kind)
	}
	return r.mutate(mutationID, "open-decision", d, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = d.Repository, d.TaskID, "decision/"+d.ID
		if _, ok := r.tasks[[2]string{d.Repository, d.TaskID}]; !ok {
			return fmt.Errorf("task %s/%s: %w", d.Repository, d.TaskID, store.ErrNotFound)
		}
		if slices.ContainsFunc(r.decisions, func(o store.Decision) bool {
			return o.Repository == d.Repository && o.TaskID == d.TaskID && o.ID == d.ID
		}) {
			return fmt.Errorf("%w: decision %q", store.ErrExists, d.ID)
		}
		r.decisions = append(r.decisions, d)
		slices.SortFunc(r.decisions, func(x, y store.Decision) int { return cmp.Compare(x.ID, y.ID) })
		return nil
	})
}

// CreateTaskGrant records a task grant and opens its decision, as store.CreateTaskGrant does, without its checks of
// the rules, steps and overlapping grants.
func (r *Records) CreateTaskGrant(mutationID string, g store.TaskGrant, _ []string) (store.Receipt, error) {
	g.Decision = store.GrantDecision(g.ID)
	return r.mutate(mutationID, "create-task-grant", g, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = g.Repository, g.TaskID, "task-grant/"+g.ID
		if _, ok := r.tasks[[2]string{g.Repository, g.TaskID}]; !ok {
			return fmt.Errorf("task %s/%s: %w", g.Repository, g.TaskID, store.ErrNotFound)
		}
		if slices.ContainsFunc(r.grants, func(o store.TaskGrant) bool {
			return o.Repository == g.Repository && o.TaskID == g.TaskID && o.ID == g.ID
		}) {
			return fmt.Errorf("%w: grant %s", store.ErrExists, g.ID)
		}
		r.grants = append(r.grants, g)
		r.decisions = append(r.decisions, store.Decision{Repository: g.Repository, TaskID: g.TaskID, ID: g.Decision, Kind: store.DecisionGrant,
			Options: []string{store.GrantApprove, store.GrantReject}, Question: "Grant " + strings.Join(g.Rules, ", ") + "?"})
		slices.SortFunc(r.decisions, func(x, y store.Decision) int { return cmp.Compare(x.ID, y.ID) })
		return nil
	})
}

// TaskGrants implements interp.Records.
func (r *Records) TaskGrants(repository, taskID string) ([]store.TaskGrant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []store.TaskGrant
	for _, g := range r.grants {
		if g.Repository == repository && (taskID == "" || g.TaskID == taskID) {
			g.Rules, g.Bound = slices.Clone(g.Rules), slices.Clone(g.Bound)
			out = append(out, g)
		}
	}
	return out, nil
}

// BindTaskGrant implements interp.Records, as store.BindTaskGrant does.
func (r *Records) BindTaskGrant(mutationID, repository, taskID, id, step string, number int) (store.Receipt, error) {
	use := store.GrantUse{Step: step, Attempt: number}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
		ID         string `json:"id"`
		store.GrantUse
	}{repository, taskID, id, use}
	return r.mutate(mutationID, "bind-task-grant", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Attempt, rc.Record = repository, taskID, number, "task-grant/"+id
		i := slices.IndexFunc(r.grants, func(g store.TaskGrant) bool {
			return g.Repository == repository && g.TaskID == taskID && g.ID == id
		})
		if i < 0 {
			return fmt.Errorf("grant %s: %w", id, store.ErrNotFound)
		}
		g := &r.grants[i]
		var d *store.Decision
		if j := slices.IndexFunc(r.decisions, func(d store.Decision) bool {
			return d.Repository == repository && d.TaskID == taskID && d.ID == g.Decision
		}); j >= 0 {
			d = &r.decisions[j]
		}
		switch {
		case slices.Contains(g.Bound, use):
			return nil
		case !g.Covers(step):
			return fmt.Errorf("%w: grant %s is for step %s", store.ErrInvalid, id, g.Step)
		case g.State(d, r.tasks[[2]string{repository, taskID}], r.attempts) != store.GrantActive:
			return fmt.Errorf("%w: grant %s is not active", store.ErrInvalid, id)
		case g.Until == store.GrantUntilAttempt && len(g.Bound) > 0:
			return fmt.Errorf("%w: grant %s was bound to another attempt", store.ErrInvalid, id)
		}
		g.Bound = append(g.Bound, use)
		return nil
	})
}

// mutate applies a write once per mutation id, replaying the original receipt for the same request.
func (r *Records) mutate(mutationID, op string, req any, apply func(rc *store.Receipt) error) (store.Receipt, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return store.Receipt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if prior, ok := r.receipts[mutationID]; ok {
		if prior.op != op || prior.req != string(body) {
			return store.Receipt{}, fmt.Errorf("records: %w: %q was %s", store.ErrMutationConflict, mutationID, prior.op)
		}
		return prior.r, nil
	}
	rc := store.Receipt{MutationID: mutationID, Operation: op, Sequence: r.seq + 1}
	if err := apply(&rc); err != nil {
		if st, ok := err.(standing); ok {
			return st.r, nil
		}
		return store.Receipt{}, fmt.Errorf("records: %s %s: %w", op, mutationID, err)
	}
	r.seq++
	// An apply that records its commit time in what it writes sets it first, as the store's does.
	if rc.CommittedAt.IsZero() {
		rc.CommittedAt = r.now()
	}
	r.receipts[mutationID] = receipt{op, string(body), rc}
	return rc, nil
}

// standing is returned by a mutation's apply when an earlier mutation, r, already wrote what it
// asks for: the mutation writes nothing and returns r, as the store does.
type standing struct{ r store.Receipt }

func (standing) Error() string { return "an earlier mutation stands" }

// RecordsHarness is one interp.Records implementation under the contract suite, with the writes
// the engine never makes: creating a task, answering a decision, ruling on a finding and requesting a full review.
type RecordsHarness struct {
	Records           interp.Records
	CreateTask        func(mutationID string, t store.Task) (store.Receipt, error)
	AnswerDecision    func(mutationID, repository, taskID, id, answer string) (store.Receipt, error)
	RuleFinding       func(mutationID string, r store.Ruling) (store.Receipt, error)
	RequestFullReview func(mutationID, repository, taskID string) (store.Receipt, error)
	// AddTaskContext records the Director's next context entry on a task, as store.AddTaskContext does.
	AddTaskContext func(mutationID, repository, taskID, text string) (store.Receipt, error)
	// AddTaskLink records the Director's next link record on a task, as store.AddTaskLink does; nil skips the link
	// cases.
	AddTaskLink func(mutationID, repository, taskID string, op store.TaskLinkOp, source store.DocumentRef, prior int, text string) (store.Receipt, error)
	// AddTaskOverride records the Director's next override record on a task, as store.AddTaskOverride does; nil
	// skips the override cases.
	AddTaskOverride func(mutationID, repository, taskID string, op store.TaskOverrideOp, key, value string, prior int, text string) (store.Receipt, error)
	// OverrideDependencies records the Director's dependency override, as store.OverrideDependencies does.
	OverrideDependencies func(mutationID, repository, taskID, reason string) (store.Receipt, error)
	// SetTaskPriority records a task's priority, as store.SetTaskPriority does, which orders the start queue.
	SetTaskPriority func(mutationID, repository, taskID string, p store.Priority) (store.Receipt, error)
	// PauseDirector pauses the Director, so a start with AttemptCaps.Hold is refused with store.ErrDirectorHeld.
	PauseDirector func() error
	// CancelTask and VoidDecision are `guildhall task cancel`'s writes, as store.CancelTask and store.VoidDecision
	// make them; nil skips the cancel cases.
	CancelTask   func(mutationID, repository, taskID, reason string) (store.Receipt, error)
	VoidDecision func(mutationID, repository, taskID, id, reason string) (store.Receipt, error)
	// Clock stamps the receipts Records writes, so it decides the day the daily cap counts.
	Clock *Clock
	// Writers and Operations size the concurrent case, defaulting to 4 writers of 30 operations each;
	// an implementation that opens the store per operation across processes sets the writers production has.
	Writers, Operations int
}

// NewRecordsHarness returns the double under the contract suite.
func NewRecordsHarness(*testing.T) RecordsHarness {
	r, clock := NewRecords(), NewClock()
	r.Now = clock.Now
	return RecordsHarness{Records: r, CreateTask: r.CreateTask, AnswerDecision: r.AnswerDecision, RuleFinding: r.RuleFinding,
		RequestFullReview: r.RequestFullReview, AddTaskContext: r.AddTaskContext, AddTaskLink: r.AddTaskLink, AddTaskOverride: r.AddTaskOverride, OverrideDependencies: r.OverrideDependencies,
		SetTaskPriority: r.SetTaskPriority, PauseDirector: r.PauseDirector, CancelTask: r.CancelTask, VoidDecision: r.VoidDecision, Clock: clock}
}

// Clock is a settable clock for a RecordsHarness; until Set it reads wall time.
type Clock struct {
	mu  sync.Mutex
	set time.Time
}

// NewClock returns a clock reading wall time.
func NewClock() *Clock { return &Clock{} }

// Now is the time last Set, or wall time before any Set.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.set.IsZero() {
		return time.Now()
	}
	return c.set
}

// Set fixes the time Now reads.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.set = t
}

// RunRecordsContract runs the interp.Records contract against fresh harnesses from newHarness.
func RunRecordsContract(t *testing.T, newHarness func(t *testing.T) RecordsHarness) {
	task := store.Task{
		Repository: "app", ID: "t7", Title: "Records contract",
		Workflow:     workflow.Revision{Name: "straight", Digest: "sha256:" + strings.Repeat("a", 64)},
		ConfigDigest: "sha256:" + strings.Repeat("c", 64),
	}
	setup := func(t *testing.T) RecordsHarness {
		h := newHarness(t)
		h.Clock.Set(time.Date(2031, 3, 9, 12, 0, 0, 0, time.UTC))
		if _, err := h.CreateTask("task:app:t7", task); err != nil {
			t.Fatal(err)
		}
		return h
	}
	runRejectionContract(t, setup)
	want := func(t *testing.T, what string, err, target error) {
		t.Helper()
		if !errors.Is(err, target) {
			t.Errorf("%s = %v, want %v", what, err, target)
		}
	}

	t.Run("concurrent lists and writes", func(t *testing.T) {
		h := setup(t)
		writers, operations := cmp.Or(h.Writers, 4), cmp.Or(h.Operations, 30)
		var wg sync.WaitGroup
		for worker := range writers {
			wg.Go(func() {
				for i := range operations {
					id := fmt.Sprintf("record-%d-%d", worker, i)
					if _, err := h.Records.StartAttempt("attempt-"+id, "app", "t7", id); err != nil {
						t.Error(err)
					}
					if _, err := h.Records.PutDocument("doc-"+id, store.DocumentRef{Repository: "app", TaskID: "t7", Name: id, Path: id, SHA256: strings.Repeat("b", 64)}); err != nil {
						t.Error(err)
					}
					if _, err := h.Records.OpenDecision("decision-"+id, store.Decision{Repository: "app", TaskID: "t7", ID: id, Kind: store.DecisionApproval, Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("b", 64)}, Question: "Approve?", Options: []string{"approved"}}); err != nil {
						t.Error(err)
					}
					if _, err := h.Records.Attempts("app", "t7"); err != nil {
						t.Error(err)
					}
					if _, err := h.Records.Documents("app", "t7"); err != nil {
						t.Error(err)
					}
					if _, err := h.Records.Decisions("app", "t7"); err != nil {
						t.Error(err)
					}
				}
			})
		}
		wg.Wait()
		a, _ := h.Records.Attempts("app", "t7")
		d, _ := h.Records.Documents("app", "t7")
		q, _ := h.Records.Decisions("app", "t7")
		if n := writers * operations; len(a) != n || len(d) != n || len(q) != n {
			t.Fatalf("counts = %d, %d, %d", len(a), len(d), len(q))
		}
	})

	t.Run("starts the judge step on a done task only, and no other step there", func(t *testing.T) {
		h := setup(t)
		_, err := h.Records.StartAttempt("judge-open", "app", "t7", workflow.JudgeStep)
		want(t, "a judge attempt on an open task", err, store.ErrInvalid)
		if _, err := h.Records.SetTaskState("done", "app", "t7", store.TaskDone); err != nil {
			t.Fatal(err)
		}
		if r, err := h.Records.StartCappedAttempt("judge", "app", "t7", workflow.JudgeStep, store.AttemptCaps{MaxRunning: 4, MaxPerDay: 40}); err != nil || r.Attempt != 1 {
			t.Fatalf("a judge attempt on a done task = %+v, %v", r, err)
		}
		_, err = h.Records.StartAttempt("plan-done", "app", "t7", "plan")
		want(t, "a plan attempt on a done task", err, store.ErrInvalid)
	})

	t.Run("reads a task and refuses an unknown one", func(t *testing.T) {
		h := setup(t)
		got, err := h.Records.Task("app", "t7")
		if err != nil || got.Title != task.Title || got.State != store.TaskOpen {
			t.Errorf("Task = %+v, %v", got, err)
		}
		_, err = h.Records.Task("app", "t8")
		want(t, "Task of an unknown task", err, store.ErrNotFound)
	})

	t.Run("numbers attempts, refuses a second running attempt and finishes once", func(t *testing.T) {
		h := setup(t)
		r1, err := h.Records.StartAttempt("start-1", "app", "t7", "plan")
		if err != nil || r1.Attempt != 1 {
			t.Fatalf("StartAttempt = %+v, %v", r1, err)
		}
		_, err = h.Records.StartAttempt("start-2", "app", "t7", "plan")
		want(t, "StartAttempt while running", err, store.ErrInvalid)
		if _, err := h.Records.FinishAttempt("finish-1", "app", "t7", "plan", 1, store.AttemptFailed); err != nil {
			t.Fatal(err)
		}
		_, err = h.Records.FinishAttempt("finish-1b", "app", "t7", "plan", 1, store.AttemptSucceeded)
		want(t, "FinishAttempt twice", err, store.ErrInvalid)
		if r, err := h.Records.FinishAttempt("finish-1c", "app", "t7", "plan", 1, store.AttemptFailed); err != nil || r.MutationID != "finish-1" {
			t.Errorf("FinishAttempt twice with the same outcome = %+v, %v; want finish-1's receipt", r, err)
		}
		_, err = h.Records.FinishAttempt("finish-9", "app", "t7", "plan", 9, store.AttemptSucceeded)
		want(t, "FinishAttempt of an unknown attempt", err, store.ErrNotFound)
		r2, err := h.Records.StartAttempt("start-2", "app", "t7", "plan")
		if err != nil || r2.Attempt != 2 {
			t.Fatalf("second StartAttempt = %+v, %v", r2, err)
		}
		if _, err := h.Records.StartAttempt("start-build", "app", "t7", "build"); err != nil {
			t.Fatal(err)
		}
		got, err := h.Records.Attempts("app", "t7")
		if err != nil {
			t.Fatal(err)
		}
		wantAttempts := []store.StageAttempt{
			{Repository: "app", TaskID: "t7", Step: "build", Number: 1},
			{Repository: "app", TaskID: "t7", Step: "plan", Number: 1, Outcome: store.AttemptFailed},
			{Repository: "app", TaskID: "t7", Step: "plan", Number: 2},
		}
		if !slices.Equal(got, wantAttempts) {
			t.Errorf("Attempts = %+v, want %+v", got, wantAttempts)
		}
		_, err = h.Records.StartAttempt("start-x", "app", "t8", "plan")
		want(t, "StartAttempt of an unknown task", err, store.ErrNotFound)
	})

	t.Run("refuses a start at a cap and admits it after a finish", func(t *testing.T) {
		h := setup(t)
		other := task
		other.Repository, other.ID = "site", "t9"
		if _, err := h.CreateTask("task:site:t9", other); err != nil {
			t.Fatal(err)
		}
		caps := store.AttemptCaps{MaxRunning: 2}
		if _, err := h.Records.StartCappedAttempt("start-plan", "app", "t7", "plan", caps); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Records.StartCappedAttempt("start-site", "site", "t9", "plan", caps); err != nil {
			t.Fatal(err)
		}
		_, err := h.Records.StartCappedAttempt("start-build", "app", "t7", "build", caps)
		refusal, ok := errors.AsType[*store.CapRefusal](err)
		if !errors.Is(err, store.ErrAttemptCap) || !ok || refusal.Cap != store.CapRunning || refusal.Count != 2 || refusal.Limit != 2 {
			t.Fatalf("start past the running cap = %v, want a running cap refusal at 2 of 2", err)
		}
		if got, _ := h.Records.Attempts("app", "t7"); len(got) != 1 {
			t.Fatalf("a refused start recorded an attempt: %+v", got)
		}
		if r, err := h.Records.StartCappedAttempt("start-plan", "app", "t7", "plan", store.AttemptCaps{MaxRunning: 1, MaxPerDay: 1}); err != nil || r.Attempt != 1 {
			t.Errorf("replayed capped start = %+v, %v; want its receipt", r, err)
		}
		if _, err := h.Records.FinishAttempt("finish-site", "site", "t9", "plan", 1, store.AttemptSucceeded); err != nil {
			t.Fatal(err)
		}
		if r, err := h.Records.StartCappedAttempt("start-build", "app", "t7", "build", caps); err != nil || r.Attempt != 1 {
			t.Fatalf("start after a finish = %+v, %v", r, err)
		}
		// Three starts today, counted whatever caps they were made under.
		_, err = h.Records.StartCappedAttempt("start-review", "app", "t7", "review", store.AttemptCaps{MaxPerDay: 3})
		refusal, ok = errors.AsType[*store.CapRefusal](err)
		if !ok || refusal.Cap != store.CapDaily || refusal.Count != 3 || refusal.Limit != 3 {
			t.Fatalf("start past the daily cap = %v, want a daily cap refusal at 3 of 3", err)
		}
	})

	t.Run("holds a start while the Director is paused", func(t *testing.T) {
		h := setup(t)
		hold := store.AttemptCaps{Hold: true}
		if _, err := h.Records.StartCappedAttempt("start-plan", "app", "t7", "plan", hold); err != nil {
			t.Fatal(err)
		}
		if err := h.PauseDirector(); err != nil {
			t.Fatal(err)
		}
		_, err := h.Records.StartCappedAttempt("start-build", "app", "t7", "build", hold)
		want(t, "start while paused", err, store.ErrDirectorHeld)
		if got, _ := h.Records.Attempts("app", "t7"); len(got) != 1 {
			t.Fatalf("a held start recorded an attempt: %+v", got)
		}
		if r, err := h.Records.StartCappedAttempt("start-plan", "app", "t7", "plan", hold); err != nil || r.Attempt != 1 {
			t.Errorf("replayed start while paused = %+v, %v; want its receipt", r, err)
		}
		if r, err := h.Records.StartCappedAttempt("start-build", "app", "t7", "build", store.AttemptCaps{}); err != nil || r.Attempt != 1 {
			t.Errorf("start without Hold while paused = %+v, %v; want it admitted", r, err)
		}
	})

	t.Run("refuses at the daily cap until the clock crosses a UTC midnight", func(t *testing.T) {
		h := setup(t)
		h.Clock.Set(time.Date(2031, 3, 9, 23, 30, 0, 0, time.UTC))
		caps := store.AttemptCaps{MaxRunning: 10, MaxPerDay: 2}
		for _, step := range []string{"plan", "build"} {
			if _, err := h.Records.StartCappedAttempt("start-"+step, "app", "t7", step, caps); err != nil {
				t.Fatal(err)
			}
		}
		_, err := h.Records.StartCappedAttempt("start-review", "app", "t7", "review", caps)
		refusal, ok := errors.AsType[*store.CapRefusal](err)
		if !ok || refusal.Cap != store.CapDaily || refusal.Count != 2 || !refusal.Day.Equal(time.Date(2031, 3, 9, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("third start before midnight = %v, want a daily cap refusal at 2 on 2031-03-09", err)
		}
		h.Clock.Set(time.Date(2031, 3, 10, 0, 30, 0, 0, time.UTC))
		if r, err := h.Records.StartCappedAttempt("start-review", "app", "t7", "review", caps); err != nil || r.Attempt != 1 {
			t.Fatalf("first start after midnight = %+v, %v; want it admitted", r, err)
		}
		_, err = h.Records.StartCappedAttempt("start-fix", "app", "t7", "fix", store.AttemptCaps{MaxPerDay: 1})
		refusal, ok = errors.AsType[*store.CapRefusal](err)
		if !ok || refusal.Cap != store.CapDaily || refusal.Count != 1 || !refusal.Day.Equal(time.Date(2031, 3, 10, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("second start after midnight = %v, want a daily cap refusal at 1 on 2031-03-10", err)
		}
	})

	t.Run("refuses a capped start of a closed task as invalid", func(t *testing.T) {
		h := setup(t)
		caps := store.AttemptCaps{MaxRunning: 1}
		if _, err := h.Records.StartCappedAttempt("start-plan", "app", "t7", "plan", caps); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Records.SetTaskState("close", "app", "t7", store.TaskCancelled); err != nil {
			t.Fatal(err)
		}
		_, err := h.Records.StartCappedAttempt("start-build", "app", "t7", "build", caps)
		if _, ok := errors.AsType[*store.CapRefusal](err); ok || !errors.Is(err, store.ErrInvalid) {
			t.Errorf("capped start of a closed task at a cap = %v, want %v and no cap refusal", err, store.ErrInvalid)
		}
	})

	// newTask records another open task for the start-queue cases.
	newTask := func(t *testing.T, h RecordsHarness, repository, id string) {
		t.Helper()
		other := task
		other.Repository, other.ID = repository, id
		if _, err := h.CreateTask("task:"+repository+":"+id, other); err != nil {
			t.Fatal(err)
		}
	}
	queued := func(t *testing.T, what string, err error, position int) *store.StartQueued {
		t.Helper()
		q, ok := errors.AsType[*store.StartQueued](err)
		if !ok || !errors.Is(err, store.ErrStartQueued) || q.Position != position {
			t.Fatalf("%s = %v, want it queued at place %d", what, err, position)
		}
		return q
	}
	order := func(t *testing.T, h RecordsHarness) []string {
		t.Helper()
		q, err := h.Records.StartQueue()
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, e := range q {
			out = append(out, fmt.Sprintf("%s/%s/%s-%d", e.Repository, e.TaskID, e.Step, e.Attempt))
		}
		return out
	}
	start := &store.StartRequest{Kind: store.StartByTask}

	t.Run("queues a start at the running cap and keeps its entry when it is repeated", func(t *testing.T) {
		h := setup(t)
		newTask(t, h, "site", "t9")
		caps := store.AttemptCaps{MaxRunning: 1, Queue: start}
		if _, err := h.Records.StartCappedAttempt("start-plan", "app", "t7", "plan", caps); err != nil {
			t.Fatal(err)
		}
		_, err := h.Records.StartCappedAttempt("start-site", "site", "t9", "plan", store.AttemptCaps{MaxRunning: 1, Queue: &store.StartRequest{Kind: store.StartBySendBack}})
		first := queued(t, "start past the running cap", err, 1)
		if e := first.Entry; e.Attempt != 1 || e.Kind != store.StartBySendBack || !e.QueuedAt.Equal(h.Clock.Now().UTC()) {
			t.Errorf("queue entry = %+v, want attempt 1 of a send-back queued now", e)
		}
		if got, _ := h.Records.Attempts("site", "t9"); len(got) != 0 {
			t.Fatalf("a queued start recorded an attempt: %+v", got)
		}
		h.Clock.Set(h.Clock.Now().Add(time.Minute))
		_, err = h.Records.StartCappedAttempt("start-site", "site", "t9", "plan", caps)
		if again := queued(t, "the queued start repeated", err, 1); again.Entry != first.Entry {
			t.Errorf("repeated queue entry = %+v, want the first, %+v", again.Entry, first.Entry)
		}
		if got := order(t, h); !slices.Equal(got, []string{"site/t9/plan-1"}) {
			t.Errorf("StartQueue = %v", got)
		}
		_, err = h.Records.StartCappedAttempt("start-review", "app", "t7", "review", store.AttemptCaps{MaxRunning: 1, MaxPerDay: 1, Queue: start})
		if refusal, ok := errors.AsType[*store.CapRefusal](err); !ok || refusal.Cap != store.CapDaily {
			t.Errorf("a queued start past the daily cap = %v, want the daily cap's refusal", err)
		}
	})

	t.Run("admits only the queue's front, removing its entry in the same write", func(t *testing.T) {
		h := setup(t)
		newTask(t, h, "site", "t9")
		caps := store.AttemptCaps{MaxRunning: 1, Queue: start}
		if _, err := h.Records.StartCappedAttempt("start-plan", "app", "t7", "plan", caps); err != nil {
			t.Fatal(err)
		}
		_ = queued(t, "site's start", func() error {
			_, err := h.Records.StartCappedAttempt("start-site", "site", "t9", "plan", caps)
			return err
		}(), 1)
		if _, err := h.Records.FinishAttempt("finish-plan", "app", "t7", "plan", 1, store.AttemptSucceeded); err != nil {
			t.Fatal(err)
		}
		// A slot is free, but site waits ahead of app's next step, queued later.
		h.Clock.Set(h.Clock.Now().Add(time.Minute))
		_, err := h.Records.StartCappedAttempt("start-build", "app", "t7", "build", caps)
		_ = queued(t, "a start behind the queue's front with a free slot", err, 2)
		r, err := h.Records.StartCappedAttempt("start-site", "site", "t9", "plan", caps)
		if err != nil || r.Attempt != 1 {
			t.Fatalf("the front's start = %+v, %v; want it admitted", r, err)
		}
		if got := order(t, h); !slices.Equal(got, []string{"app/t7/build-1"}) {
			t.Errorf("StartQueue after the front's admission = %v, want only app's build", got)
		}
		if r, err := h.Records.StartCappedAttempt("start-site", "site", "t9", "plan", store.AttemptCaps{MaxRunning: 1, Queue: start}); err != nil || r.Attempt != 1 {
			t.Errorf("the admitted start replayed with the cap full and a queue = %+v, %v; want its receipt", r, err)
		}
		if _, err := h.Records.FinishAttempt("finish-site", "site", "t9", "plan", 1, store.AttemptSucceeded); err != nil {
			t.Fatal(err)
		}
		if r, err := h.Records.StartCappedAttempt("start-build", "app", "t7", "build", caps); err != nil || r.Attempt != 1 {
			t.Fatalf("the new front's start = %+v, %v", r, err)
		}
		if got := order(t, h); len(got) != 0 {
			t.Errorf("StartQueue = %v, want it empty", got)
		}
	})

	t.Run("orders the queue by live priority, then queue time, and drops closed tasks", func(t *testing.T) {
		h := setup(t)
		for _, id := range []string{"t9", "t10", "t11"} {
			newTask(t, h, "app", id)
		}
		caps := store.AttemptCaps{MaxRunning: 1, Queue: start}
		if _, err := h.Records.StartCappedAttempt("start-plan", "app", "t7", "plan", caps); err != nil {
			t.Fatal(err)
		}
		for i, id := range []string{"t9", "t10", "t11"} {
			h.Clock.Set(h.Clock.Now().Add(time.Minute))
			_, err := h.Records.StartCappedAttempt("start-"+id, "app", id, "plan", caps)
			_ = queued(t, id+"'s start", err, i+1)
		}
		if got := order(t, h); !slices.Equal(got, []string{"app/t9/plan-1", "app/t10/plan-1", "app/t11/plan-1"}) {
			t.Errorf("StartQueue at one priority = %v, want queue time order", got)
		}
		if _, err := h.SetTaskPriority("priority-t11", "app", "t11", store.PriorityUrgent); err != nil {
			t.Fatal(err)
		}
		if got := order(t, h); !slices.Equal(got, []string{"app/t11/plan-1", "app/t9/plan-1", "app/t10/plan-1"}) {
			t.Errorf("StartQueue after t11 became p0 = %v, want t11 first", got)
		}
		if _, err := h.Records.SetTaskState("cancel-t11", "app", "t11", store.TaskCancelled); err != nil {
			t.Fatal(err)
		}
		if got := order(t, h); !slices.Equal(got, []string{"app/t9/plan-1", "app/t10/plan-1"}) {
			t.Errorf("StartQueue after t11 was cancelled = %v", got)
		}
		if _, err := h.Records.FinishAttempt("finish-plan", "app", "t7", "plan", 1, store.AttemptSucceeded); err != nil {
			t.Fatal(err)
		}
		if r, err := h.Records.StartCappedAttempt("start-t9", "app", "t9", "plan", caps); err != nil || r.Attempt != 1 {
			t.Errorf("t9's start once the cancelled front is gone = %+v, %v", r, err)
		}
	})

	t.Run("starts a Director stage uncounted and uncapped, but held", func(t *testing.T) {
		h := setup(t)
		r, err := h.Records.StartDirectorAttempt("start-grill", "app", "t7", "grill", true)
		if err != nil || r.Attempt != 1 {
			t.Fatalf("StartDirectorAttempt = %+v, %v", r, err)
		}
		if got, _ := h.Records.Attempts("app", "t7"); len(got) != 1 || !got[0].Director {
			t.Errorf("Attempts = %+v, want one Director attempt", got)
		}
		if r, err := h.Records.StartCappedAttempt("start-plan", "app", "t7", "plan", store.AttemptCaps{MaxRunning: 1, Queue: start}); err != nil || r.Attempt != 1 {
			t.Fatalf("a start with only a Director stage running = %+v, %v; want it admitted", r, err)
		}
		if r, err := h.Records.StartDirectorAttempt("start-research", "app", "t7", "research", true); err != nil || r.Attempt != 1 {
			t.Errorf("a Director stage at the running cap = %+v, %v; want it started", r, err)
		}
		if err := h.PauseDirector(); err != nil {
			t.Fatal(err)
		}
		_, err = h.Records.StartDirectorAttempt("start-shape", "app", "t7", "shape", true)
		want(t, "a Director stage while paused", err, store.ErrDirectorHeld)
	})

	t.Run("replays a mutation id and refuses its reuse", func(t *testing.T) {
		h := setup(t)
		first, err := h.Records.StartAttempt("start-1", "app", "t7", "plan")
		if err != nil {
			t.Fatal(err)
		}
		again, err := h.Records.StartAttempt("start-1", "app", "t7", "plan")
		if err != nil || again.Sequence != first.Sequence || again.Attempt != 1 {
			t.Errorf("replayed StartAttempt = %+v, %v, want %+v", again, err, first)
		}
		if got, _ := h.Records.Attempts("app", "t7"); len(got) != 1 {
			t.Errorf("replay recorded %d attempts", len(got))
		}
		_, err = h.Records.StartAttempt("start-1", "app", "t7", "build")
		want(t, "reused mutation id", err, store.ErrMutationConflict)
	})

	t.Run("records a document once", func(t *testing.T) {
		h := setup(t)
		d := store.DocumentRef{Repository: "app", TaskID: "t7", Name: "plan-1-spec", Path: "tasks/app/t7/plan-1/spec.md", SHA256: strings.Repeat("b", 64)}
		if _, err := h.Records.PutDocument("doc-1", d); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Records.PutDocument("doc-1", d); err != nil {
			t.Errorf("replayed PutDocument = %v", err)
		}
		other := d
		other.SHA256 = strings.Repeat("e", 64)
		_, err := h.Records.PutDocument("doc-2", other)
		want(t, "PutDocument of a recorded name", err, store.ErrExists)
		d2 := d
		d2.Name = "accept-1-spec"
		if _, err := h.Records.PutDocument("doc-3", d2); err != nil {
			t.Fatal(err)
		}
		got, err := h.Records.Documents("app", "t7")
		if err != nil || !slices.Equal(got, []store.DocumentRef{d2, d}) {
			t.Errorf("Documents = %+v, %v", got, err)
		}
	})

	t.Run("opens a typed decision once and lists its answer", func(t *testing.T) {
		h := setup(t)
		d := store.Decision{
			Repository: "app", TaskID: "t7", ID: "accept-1", Kind: store.DecisionApproval,
			Subject:  &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("b", 64)},
			Options:  []string{"approved", "changes_requested"},
			Question: "Approve the plan?",
		}
		noSubject := d
		noSubject.Subject = nil
		_, err := h.Records.OpenDecision("decision-0", noSubject)
		want(t, "OpenDecision of an approval without a subject", err, store.ErrInvalid)
		if _, err := h.Records.OpenDecision("decision-1", d); err != nil {
			t.Fatal(err)
		}
		_, err = h.Records.OpenDecision("decision-2", d)
		want(t, "OpenDecision of a recorded id", err, store.ErrExists)
		if _, err := h.AnswerDecision("answer-1", "app", "t7", "accept-1", "approved\nship it"); err != nil {
			t.Fatal(err)
		}
		got, err := h.Records.Decisions("app", "t7")
		if err != nil || len(got) != 1 || !got[0].Answered || got[0].Choice != "approved" || got[0].Note != "ship it" || *got[0].Subject != *d.Subject {
			t.Errorf("Decisions = %+v, %v", got, err)
		}
	})
	t.Run("lists rulings and contests one once", func(t *testing.T) {
		h := setup(t)
		d := store.DocumentRef{Repository: "app", TaskID: "t7", Name: "review-1-outcome", Path: "tasks/app/t7/review-1/outcome.json", SHA256: strings.Repeat("b", 64),
			Review: &store.Review{Findings: []workflow.Finding{{ID: "f1", Rule: "S2", Summary: "No red test shown."}}}}
		if _, err := h.Records.PutDocument("doc-1", d); err != nil {
			t.Fatal(err)
		}
		ru := store.Ruling{Repository: "app", TaskID: "t7", Document: d.Name, Finding: "f1", Move: store.RulingOutOfScope, Reason: "Spec §2 excludes it."}
		if _, err := h.RuleFinding("rule-1", ru); err != nil {
			t.Fatal(err)
		}
		_, err := h.Records.ContestRuling("contest-0", "app", "t7", d.Name, "f2", "review-2")
		want(t, "ContestRuling of an unknown ruling", err, store.ErrNotFound)
		for range 2 {
			if _, err := h.Records.ContestRuling("contest-1", "app", "t7", d.Name, "f1", "review-2"); err != nil {
				t.Fatalf("ContestRuling = %v", err)
			}
		}
		_, err = h.Records.ContestRuling("contest-2", "app", "t7", d.Name, "f1", "review-3")
		want(t, "ContestRuling of a contested ruling", err, store.ErrInvalid)
		ru.ContestedBy = "review-2"
		if got, err := h.Records.Rulings("app", "t7"); err != nil || !slices.Equal(got, []store.Ruling{ru}) {
			t.Errorf("Rulings = %+v, %v", got, err)
		}
	})
	t.Run("requests a full review and consumes it once", func(t *testing.T) {
		h := setup(t)
		_, err := h.RequestFullReview("full-x", "app", "t8")
		want(t, "RequestFullReview of an unknown task", err, store.ErrNotFound)
		rc, err := h.RequestFullReview("full-1", "app", "t7")
		if err != nil || rc.Record != "full-review/1" {
			t.Fatalf("RequestFullReview = %+v, %v", rc, err)
		}
		if replay, err := h.RequestFullReview("full-1", "app", "t7"); err != nil || replay != rc {
			t.Fatalf("replay = %+v, %v", replay, err)
		}
		_, err = h.RequestFullReview("full-2", "app", "t7")
		want(t, "RequestFullReview while one is pending", err, store.ErrExists)
		_, err = h.Records.ConsumeFullReview("consume-0", "app", "t7", 2, "review-1")
		want(t, "ConsumeFullReview of an unknown request", err, store.ErrNotFound)
		for range 2 {
			if rc, err := h.Records.ConsumeFullReview("consume-1", "app", "t7", 1, "review-1"); err != nil || rc.Record != "full-review/1" {
				t.Fatalf("ConsumeFullReview = %+v, %v", rc, err)
			}
		}
		_, err = h.Records.ConsumeFullReview("consume-2", "app", "t7", 1, "review-2")
		want(t, "ConsumeFullReview of a consumed request", err, store.ErrInvalid)
		if rc, err := h.RequestFullReview("full-2", "app", "t7"); err != nil || rc.Record != "full-review/2" {
			t.Fatalf("second RequestFullReview = %+v, %v", rc, err)
		}
		wantList := []store.FullReview{{Repository: "app", TaskID: "t7", Number: 1, ConsumedBy: "review-1"}, {Repository: "app", TaskID: "t7", Number: 2}}
		if got, err := h.Records.FullReviews("app", "t7"); err != nil || !slices.Equal(got, wantList) {
			t.Errorf("FullReviews = %+v, %v", got, err)
		}
	})
	t.Run("lists context entries in order and refuses them on an unknown or closed task", func(t *testing.T) {
		h := setup(t)
		_, err := h.AddTaskContext("context-x", "app", "t8", "Keep it quiet.")
		want(t, "AddTaskContext of an unknown task", err, store.ErrNotFound)
		var receipts []store.Receipt
		for i, text := range []string{"Keep it quiet.", "Supersedes 1: keep it loud."} {
			rc, err := h.AddTaskContext(fmt.Sprintf("context-%d", i+1), "app", "t7", text)
			if err != nil || rc.Record != store.TaskContextRecord(i+1) {
				t.Fatalf("AddTaskContext %d = %+v, %v", i+1, rc, err)
			}
			receipts = append(receipts, rc)
		}
		if replay, err := h.AddTaskContext("context-1", "app", "t7", "Keep it quiet."); err != nil || replay != receipts[0] {
			t.Fatalf("replay = %+v, %v", replay, err)
		}
		got, err := h.Records.TaskContexts("app", "t7")
		if err != nil || len(got) != 2 {
			t.Fatalf("TaskContexts = %+v, %v", got, err)
		}
		for i, c := range got {
			w := store.TaskContext{Repository: "app", TaskID: "t7", Number: i + 1, Text: c.Text, Author: store.TaskContextAuthor, AddedAt: receipts[i].CommittedAt}
			if c != w || c.Text != []string{"Keep it quiet.", "Supersedes 1: keep it loud."}[i] {
				t.Errorf("TaskContexts[%d] = %+v, want %+v", i, c, w)
			}
		}
		if got, err := h.Records.TaskContexts("app", "t8"); err != nil || len(got) != 0 {
			t.Errorf("TaskContexts of an unknown task = %+v, %v", got, err)
		}
		if _, err := h.Records.SetTaskState("done", "app", "t7", store.TaskDone); err != nil {
			t.Fatal(err)
		}
		_, err = h.AddTaskContext("context-3", "app", "t7", "Too late.")
		want(t, "AddTaskContext of a done task", err, store.ErrInvalid)
	})
	t.Run("lists link records in order and refuses a link that is stale, repeated or to nothing", func(t *testing.T) {
		h := setup(t)
		if h.AddTaskLink == nil {
			t.Skip("the harness records no task links")
		}
		src := task
		src.ID = "t2"
		if _, err := h.CreateTask("task:app:t2", src); err != nil {
			t.Fatal(err)
		}
		spec := store.DocumentRef{Repository: "app", TaskID: "t2", Name: "plan-1-spec", Path: "tasks/app/t2/plan-1/spec.md", SHA256: strings.Repeat("d", 64)}
		if _, err := h.Records.PutDocument("doc-spec", spec); err != nil {
			t.Fatal(err)
		}
		named := store.DocumentRef{Repository: "app", TaskID: "t2", Name: "plan-1-spec"}
		_, err := h.AddTaskLink("link-x", "app", "t7", store.TaskLinkAdd, store.DocumentRef{Repository: "app", TaskID: "t2", Name: "plan-9-spec"}, 0, "Read it.")
		want(t, "AddTaskLink of an unknown document", err, store.ErrNotFound)
		_, err = h.AddTaskLink("link-y", "app", "t7", store.TaskLinkRemove, named, 0, "Drop it.")
		want(t, "AddTaskLink removing an unlinked document", err, store.ErrInvalid)
		var receipts []store.Receipt
		for i, op := range []store.TaskLinkOp{store.TaskLinkAdd, store.TaskLinkRemove} {
			rc, err := h.AddTaskLink(fmt.Sprintf("link-%d", i+1), "app", "t7", op, named, i, "Guildmaster: "+string(op)+".")
			if err != nil || rc.Record != store.TaskLinkRecord(i+1) {
				t.Fatalf("AddTaskLink %d = %+v, %v", i+1, rc, err)
			}
			receipts = append(receipts, rc)
		}
		if replay, err := h.AddTaskLink("link-1", "app", "t7", store.TaskLinkAdd, named, 0, "Guildmaster: add."); err != nil || replay != receipts[0] {
			t.Fatalf("replay = %+v, %v", replay, err)
		}
		_, err = h.AddTaskLink("link-3", "app", "t7", store.TaskLinkAdd, named, 1, "Guildmaster: add.")
		want(t, "AddTaskLink with a stale prior count", err, store.ErrExists)
		got, err := h.Records.TaskLinks("app", "t7")
		if err != nil || len(got) != 2 {
			t.Fatalf("TaskLinks = %+v, %v", got, err)
		}
		for i, l := range got {
			op := []store.TaskLinkOp{store.TaskLinkAdd, store.TaskLinkRemove}[i]
			w := store.TaskLink{Repository: "app", TaskID: "t7", Number: i + 1, Op: op, Source: spec, Text: "Guildmaster: " + string(op) + ".",
				Author: store.TaskLinkAuthor, AddedAt: receipts[i].CommittedAt}
			if l != w {
				t.Errorf("TaskLinks[%d] = %+v, want %+v", i, l, w)
			}
		}
		if active := store.ActiveTaskLinks(got); len(active) != 0 {
			t.Errorf("ActiveTaskLinks after the remove = %+v", active)
		}
		if got, err := h.Records.TaskLinks("app", "t8"); err != nil || len(got) != 0 {
			t.Errorf("TaskLinks of an unknown task = %+v, %v", got, err)
		}
	})

	t.Run("lists override records in order and refuses one that is stale or unsets nothing", func(t *testing.T) {
		h := setup(t)
		if h.AddTaskOverride == nil {
			t.Skip("the harness records no task overrides")
		}
		_, err := h.AddTaskOverride("override-x", "app", "t8", store.TaskOverrideSet, "default_effort", "high", 0, "Run it high.")
		want(t, "AddTaskOverride of an unknown task", err, store.ErrNotFound)
		_, err = h.AddTaskOverride("override-y", "app", "t7", store.TaskOverrideUnset, "default_effort", "", 0, "Drop it.")
		want(t, "AddTaskOverride unsetting a key not in effect", err, store.ErrInvalid)
		type rec struct {
			op         store.TaskOverrideOp
			key, value string
			prior      int
		}
		recs := []rec{{store.TaskOverrideSet, "default_effort", "high", 0}, {store.TaskOverrideSet, "stall_interval", "5m", 0}, {store.TaskOverrideUnset, "default_effort", "", 1}}
		var receipts []store.Receipt
		for i, o := range recs {
			rc, err := h.AddTaskOverride(fmt.Sprintf("override-%d", i+1), "app", "t7", o.op, o.key, o.value, o.prior, "Guildmaster: "+string(o.op)+".")
			if err != nil || rc.Record != store.TaskOverrideRecord(i+1) {
				t.Fatalf("AddTaskOverride %d = %+v, %v", i+1, rc, err)
			}
			receipts = append(receipts, rc)
		}
		if replay, err := h.AddTaskOverride("override-1", "app", "t7", store.TaskOverrideSet, "default_effort", "high", 0, "Guildmaster: set."); err != nil || replay != receipts[0] {
			t.Fatalf("replay = %+v, %v", replay, err)
		}
		_, err = h.AddTaskOverride("override-4", "app", "t7", store.TaskOverrideSet, "default_effort", "low", 1, "Guildmaster: set.")
		want(t, "AddTaskOverride with a stale prior count", err, store.ErrExists)
		got, err := h.Records.TaskOverrides("app", "t7")
		if err != nil || len(got) != len(recs) {
			t.Fatalf("TaskOverrides = %+v, %v", got, err)
		}
		for i, o := range got {
			w := store.TaskOverride{Repository: "app", TaskID: "t7", Number: i + 1, Op: recs[i].op, Key: recs[i].key, Value: recs[i].value,
				Text: "Guildmaster: " + string(recs[i].op) + ".", Author: store.TaskOverrideAuthor, AddedAt: receipts[i].CommittedAt}
			if o != w {
				t.Errorf("TaskOverrides[%d] = %+v, want %+v", i, o, w)
			}
		}
		if active := store.ActiveTaskOverrides(got); len(active) != 1 || active[0] != got[1] {
			t.Errorf("ActiveTaskOverrides = %+v, want only %+v", active, got[1])
		}
		if got, err := h.Records.TaskOverrides("app", "t8"); err != nil || len(got) != 0 {
			t.Errorf("TaskOverrides of an unknown task = %+v, %v", got, err)
		}
	})

	t.Run("reads a dependency override", func(t *testing.T) {
		h := setup(t)
		if h.OverrideDependencies == nil {
			t.Skip("the harness records no dependency overrides")
		}
		dependent := task
		dependent.ID, dependent.DependsOn = "t9", []store.TaskRef{{Repository: "app", ID: "t7"}}
		if _, err := h.CreateTask("task-t9", dependent); err != nil {
			t.Fatal(err)
		}
		_, err := h.Records.DependencyOverride("app", "t9")
		want(t, "DependencyOverride before any", err, store.ErrNotFound)
		_, err = h.OverrideDependencies("override-t7", "app", "t7", "Start anyway.")
		want(t, "OverrideDependencies of a task with no dependencies", err, store.ErrInvalid)
		rc, err := h.OverrideDependencies("dependency-override.app.t9", "app", "t9", "Start anyway.")
		if err != nil || rc.Record != store.DependencyOverrideRecord || rc.TaskID != "t9" {
			t.Fatalf("OverrideDependencies = %+v, %v", rc, err)
		}
		_, err = h.OverrideDependencies("dependency-override.app.t9", "app", "t9", "Another reason.")
		want(t, "OverrideDependencies with another reason", err, store.ErrMutationConflict)
		wantOverride := store.DependencyOverride{Repository: "app", TaskID: "t9", Reason: "Start anyway."}
		if got, err := h.Records.DependencyOverride("app", "t9"); err != nil || got != wantOverride {
			t.Errorf("DependencyOverride = %+v, %v; want %+v", got, err, wantOverride)
		}
	})

	t.Run("closes a task once", func(t *testing.T) {
		h := setup(t)
		ref := store.TaskRef{Repository: "app", ID: "t7"}
		open, err := h.Records.TaskCloses()
		if c := open[ref]; err != nil || c.Created == 0 || c.Closed != 0 || !c.At.IsZero() {
			t.Fatalf("TaskCloses of the open task = %+v, %v; want its create receipt only", c, err)
		}
		_, err = h.Records.SetTaskState("close-x", "app", "t8", store.TaskDone)
		want(t, "SetTaskState of an unknown task", err, store.ErrNotFound)
		_, err = h.Records.SetTaskState("close-open", "app", "t7", store.TaskOpen)
		want(t, "SetTaskState to open", err, store.ErrInvalid)
		rc, err := h.Records.SetTaskState("close-1", "app", "t7", store.TaskDone)
		if err != nil || rc.Record != "task" || rc.Repository != "app" || rc.TaskID != "t7" {
			t.Fatalf("SetTaskState = %+v, %v", rc, err)
		}
		closes, err := h.Records.TaskCloses()
		if c := closes[ref]; err != nil || c.Created != open[ref].Created || c.Closed != rc.Sequence || !c.At.Equal(rc.CommittedAt) {
			t.Fatalf("TaskCloses of the done task = %+v, %v; want created at %d, closed by %+v", c, err, open[ref].Created, rc)
		}
		if replay, err := h.Records.SetTaskState("close-1", "app", "t7", store.TaskDone); err != nil || replay != rc {
			t.Fatalf("replay = %+v, %v", replay, err)
		}
		if got, err := h.Records.Task("app", "t7"); err != nil || got.State != store.TaskDone {
			t.Errorf("Task after closing = %+v, %v", got, err)
		}
		_, err = h.Records.SetTaskState("close-2", "app", "t7", store.TaskCancelled)
		want(t, "SetTaskState of a closed task", err, store.ErrInvalid)
		_, err = h.Records.StartAttempt("start-closed", "app", "t7", "plan")
		want(t, "StartAttempt on a closed task", err, store.ErrInvalid)
	})

	t.Run("cancels an open task once with its reason and time", func(t *testing.T) {
		h := setup(t)
		if h.CancelTask == nil {
			t.Skip("the harness records no cancels")
		}
		_, err := h.CancelTask("cancel-x", "app", "t8", "Duplicate.")
		want(t, "CancelTask of an unknown task", err, store.ErrNotFound)
		_, err = h.CancelTask("cancel-blank", "app", "t7", " ")
		want(t, "CancelTask without a reason", err, store.ErrInvalid)
		at := time.Date(2031, 3, 10, 8, 30, 0, 0, time.UTC)
		h.Clock.Set(at)
		rc, err := h.CancelTask("cancel.app.t7", "app", "t7", "Duplicate of t3.")
		if err != nil || rc.Record != "task" || rc.Repository != "app" || rc.TaskID != "t7" {
			t.Fatalf("CancelTask = %+v, %v", rc, err)
		}
		h.Clock.Set(at.Add(time.Hour))
		if replay, err := h.CancelTask("cancel.app.t7", "app", "t7", "Duplicate of t3."); err != nil || replay != rc {
			t.Fatalf("replay = %+v, %v; want %+v", replay, err, rc)
		}
		got, err := h.Records.Task("app", "t7")
		if err != nil || got.State != store.TaskCancelled || got.CancelReason != "Duplicate of t3." || !got.CancelledAt.Equal(at) {
			t.Errorf("Task after cancelling = %+v, %v; want cancelled with the reason at %v", got, err, at)
		}
		_, err = h.CancelTask("cancel-again", "app", "t7", "Duplicate of t3.")
		want(t, "CancelTask of a cancelled task", err, store.ErrInvalid)
		_, err = h.Records.SetTaskState("close-cancelled", "app", "t7", store.TaskDone)
		want(t, "SetTaskState of a cancelled task", err, store.ErrInvalid)
		_, err = h.Records.StartAttempt("start-cancelled", "app", "t7", "plan")
		want(t, "StartAttempt on a cancelled task", err, store.ErrInvalid)
		_, err = h.Records.StartAttempt("judge-cancelled", "app", "t7", workflow.JudgeStep)
		want(t, "a judge attempt on a cancelled task", err, store.ErrInvalid)
	})

	t.Run("refuses to cancel a done task", func(t *testing.T) {
		h := setup(t)
		if h.CancelTask == nil {
			t.Skip("the harness records no cancels")
		}
		if _, err := h.Records.SetTaskState("done", "app", "t7", store.TaskDone); err != nil {
			t.Fatal(err)
		}
		_, err := h.CancelTask("cancel.app.t7", "app", "t7", "Too late.")
		want(t, "CancelTask of a done task", err, store.ErrInvalid)
		if got, err := h.Records.Task("app", "t7"); err != nil || got.State != store.TaskDone || got.CancelReason != "" || !got.CancelledAt.IsZero() {
			t.Errorf("Task = %+v, %v; want it done, without a cancel", got, err)
		}
	})

	t.Run("voids an open decision once, which then refuses an answer", func(t *testing.T) {
		h := setup(t)
		if h.VoidDecision == nil {
			t.Skip("the harness records no voids")
		}
		open := func(id string) {
			t.Helper()
			d := store.Decision{Repository: "app", TaskID: "t7", ID: id, Kind: store.DecisionQuestion, Question: "Which?"}
			if _, err := h.Records.OpenDecision("open-"+id, d); err != nil {
				t.Fatal(err)
			}
		}
		open("q-1")
		open("q-2")
		if _, err := h.AnswerDecision("answer-q-2", "app", "t7", "q-2", "This one."); err != nil {
			t.Fatal(err)
		}
		_, err := h.VoidDecision("void-x", "app", "t7", "q-9", "Cancelled.")
		want(t, "VoidDecision of an unknown decision", err, store.ErrNotFound)
		_, err = h.VoidDecision("void-q-2", "app", "t7", "q-2", "Cancelled.")
		want(t, "VoidDecision of an answered decision", err, store.ErrInvalid)
		rc, err := h.VoidDecision("void-q-1", "app", "t7", "q-1", "Cancelled.")
		if err != nil || rc.Record != "decision/q-1" {
			t.Fatalf("VoidDecision = %+v, %v", rc, err)
		}
		for _, m := range []string{"void-q-1", "void-q-1-again"} {
			if again, err := h.VoidDecision(m, "app", "t7", "q-1", "Cancelled."); err != nil || again != rc {
				t.Errorf("VoidDecision again under %s = %+v, %v; want %+v", m, again, err, rc)
			}
		}
		_, err = h.VoidDecision("void-q-1-other", "app", "t7", "q-1", "Another reason.")
		want(t, "VoidDecision of a void decision with another reason", err, store.ErrInvalid)
		_, err = h.AnswerDecision("answer-q-1", "app", "t7", "q-1", "Too late.")
		want(t, "AnswerDecision of a void decision", err, store.ErrInvalid)
		got, err := h.Records.Decisions("app", "t7")
		if err != nil || len(got) != 2 || !got[0].Void || got[0].VoidReason != "Cancelled." || got[0].Answered || got[0].Open() || got[1].Void || got[1].Open() {
			t.Errorf("Decisions = %+v, %v; want q-1 void and unanswered, q-2 answered", got, err)
		}
	})
}
