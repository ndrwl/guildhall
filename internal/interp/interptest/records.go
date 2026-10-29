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
	overrides map[[2]string]store.DependencyOverride
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
		if len(d.Options) > 0 {
			first, rest, _ := strings.Cut(answer, "\n")
			first = strings.TrimSuffix(first, "\r")
			if !slices.Contains(d.Options, first) {
				return fmt.Errorf("%w: answer %q is not one of %q", store.ErrInvalid, first, d.Options)
			}
			d.Choice, d.Note = first, strings.TrimSpace(rest)
		}
		d.Answered, d.Answer = true, answer
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
	if d.Kind != store.DecisionQuestion && d.Subject == nil {
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
	rc.CommittedAt = r.now()
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
	// OverrideDependencies records the Director's dependency override, as store.OverrideDependencies does.
	OverrideDependencies func(mutationID, repository, taskID, reason string) (store.Receipt, error)
	// SetTaskPriority records a task's priority, as store.SetTaskPriority does, which orders the start queue.
	SetTaskPriority func(mutationID, repository, taskID string, p store.Priority) (store.Receipt, error)
	// PauseDirector pauses the Director, so a start with AttemptCaps.Hold is refused with store.ErrDirectorHeld.
	PauseDirector func() error
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
		RequestFullReview: r.RequestFullReview, OverrideDependencies: r.OverrideDependencies, SetTaskPriority: r.SetTaskPriority,
		PauseDirector: r.PauseDirector, Clock: clock}
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
		queued(t, "site's start", func() error {
			_, err := h.Records.StartCappedAttempt("start-site", "site", "t9", "plan", caps)
			return err
		}(), 1)
		if _, err := h.Records.FinishAttempt("finish-plan", "app", "t7", "plan", 1, store.AttemptSucceeded); err != nil {
			t.Fatal(err)
		}
		// A slot is free, but site waits ahead of app's next step, queued later.
		h.Clock.Set(h.Clock.Now().Add(time.Minute))
		_, err := h.Records.StartCappedAttempt("start-build", "app", "t7", "build", caps)
		queued(t, "a start behind the queue's front with a free slot", err, 2)
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
			queued(t, id+"'s start", err, i+1)
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
		_, err := h.Records.SetTaskState("close-x", "app", "t8", store.TaskDone)
		want(t, "SetTaskState of an unknown task", err, store.ErrNotFound)
		_, err = h.Records.SetTaskState("close-open", "app", "t7", store.TaskOpen)
		want(t, "SetTaskState to open", err, store.ErrInvalid)
		rc, err := h.Records.SetTaskState("close-1", "app", "t7", store.TaskDone)
		if err != nil || rc.Record != "task" || rc.Repository != "app" || rc.TaskID != "t7" {
			t.Fatalf("SetTaskState = %+v, %v", rc, err)
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
}
