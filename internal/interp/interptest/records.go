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

// StartAttempt implements interp.Records.
func (r *Records) StartAttempt(mutationID, repository, taskID, step string) (store.Receipt, error) {
	req := store.StageAttempt{Repository: repository, TaskID: taskID, Step: step}
	return r.mutate(mutationID, "start-attempt", req, func(rc *store.Receipt) error { return r.startAttempt(rc, repository, taskID, step) })
}

func (r *Records) startAttempt(rc *store.Receipt, repository, taskID, step string) error {
	rc.Repository, rc.TaskID = repository, taskID
	t, ok := r.tasks[[2]string{repository, taskID}]
	switch {
	case !ok:
		return fmt.Errorf("task %s/%s: %w", repository, taskID, store.ErrNotFound)
	case t.State != store.TaskOpen:
		return fmt.Errorf("%w: task %s/%s is %s", store.ErrInvalid, repository, taskID, t.State)
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
	a := store.StageAttempt{Repository: repository, TaskID: taskID, Step: step, Number: n + 1}
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

// StartCappedAttempt implements interp.Records, counting the day by Now.
func (r *Records) StartCappedAttempt(mutationID, repository, taskID, step string, caps store.AttemptCaps) (store.Receipt, error) {
	req := store.StageAttempt{Repository: repository, TaskID: taskID, Step: step}
	return r.mutate(mutationID, "start-attempt", req, func(rc *store.Receipt) error {
		if caps.Hold && r.held {
			return fmt.Errorf("%w: the double is paused", store.ErrDirectorHeld)
		}
		if _, ok := r.tasks[[2]string{repository, taskID}]; ok && (caps.MaxRunning > 0 || caps.MaxPerDay > 0) {
			y, m, d := r.now().Date()
			day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
			running := 0
			for _, a := range r.attempts {
				if a.Outcome == "" {
					running++
				}
			}
			today := 0
			for _, prior := range r.receipts {
				if prior.op == "start-attempt" && !prior.r.CommittedAt.Before(day) {
					today++
				}
			}
			switch {
			case caps.MaxRunning > 0 && running >= caps.MaxRunning:
				return &store.CapRefusal{Cap: store.CapRunning, Count: running, Limit: caps.MaxRunning, Day: day}
			case caps.MaxPerDay > 0 && today >= caps.MaxPerDay:
				return &store.CapRefusal{Cap: store.CapDaily, Count: today, Limit: caps.MaxPerDay, Day: day}
			}
		}
		return r.startAttempt(rc, repository, taskID, step)
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
	// PauseDirector pauses the Director, so a start with AttemptCaps.Hold is refused with store.ErrDirectorHeld.
	PauseDirector func() error
	// Writers and Operations size the concurrent case, defaulting to 4 writers of 30 operations each;
	// an implementation that opens the store per operation across processes sets the writers production has.
	Writers, Operations int
}

// NewRecordsHarness returns the double under the contract suite.
func NewRecordsHarness(*testing.T) RecordsHarness {
	r := NewRecords()
	return RecordsHarness{Records: r, CreateTask: r.CreateTask, AnswerDecision: r.AnswerDecision, RuleFinding: r.RuleFinding,
		RequestFullReview: r.RequestFullReview, PauseDirector: r.PauseDirector}
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
		if _, err := h.CreateTask("task:app:t7", task); err != nil {
			t.Fatal(err)
		}
		return h
	}
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
		// Three starts today, counted whatever caps they were made under; skipped across a UTC midnight.
		day := time.Now().UTC().Truncate(24 * time.Hour)
		_, err = h.Records.StartCappedAttempt("start-review", "app", "t7", "review", store.AttemptCaps{MaxPerDay: 3})
		refusal, ok = errors.AsType[*store.CapRefusal](err)
		if time.Now().UTC().Truncate(24*time.Hour).Equal(day) && (!ok || refusal.Cap != store.CapDaily || refusal.Count != 3 || refusal.Limit != 3) {
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
