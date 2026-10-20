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

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Records is the in-memory interp.Records double. It keeps the store's replay, conflict, numbering and
// once-only rules and its sentinel errors, and passes RunRecordsContract with the real store.
// It does not repeat the store's field validation beyond a decision's subject.
type Records struct {
	mu        sync.Mutex
	seq       uint64
	receipts  map[string]receipt
	tasks     map[[2]string]store.Task
	attempts  []store.StageAttempt
	documents []store.DocumentRef
	decisions []store.Decision
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

// StartAttempt implements interp.Records.
func (r *Records) StartAttempt(mutationID, repository, taskID, step string) (store.Receipt, error) {
	req := store.StageAttempt{Repository: repository, TaskID: taskID, Step: step}
	return r.mutate(mutationID, "start-attempt", req, func(rc *store.Receipt) error {
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
	})
}

// FinishAttempt implements interp.Records.
func (r *Records) FinishAttempt(mutationID, repository, taskID, step string, number int, outcome store.AttemptOutcome) (store.Receipt, error) {
	req := store.StageAttempt{Repository: repository, TaskID: taskID, Step: step, Number: number, Outcome: outcome}
	return r.mutate(mutationID, "finish-attempt", req, func(rc *store.Receipt) error {
		rc.Repository, rc.TaskID, rc.Attempt, rc.Record = repository, taskID, number, fmt.Sprintf("attempt/%s/%d", step, number)
		i := slices.IndexFunc(r.attempts, func(a store.StageAttempt) bool {
			return a.Repository == repository && a.TaskID == taskID && a.Step == step && a.Number == number
		})
		switch {
		case i < 0:
			return fmt.Errorf("attempt %d of step %q: %w", number, step, store.ErrNotFound)
		case r.attempts[i].Outcome != "":
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
		return store.Receipt{}, fmt.Errorf("records: %s %s: %w", op, mutationID, err)
	}
	r.seq++
	r.receipts[mutationID] = receipt{op, string(body), rc}
	return rc, nil
}

// RecordsHarness is one interp.Records implementation under the contract suite, with the two writes
// the engine never makes: creating a task and answering a decision.
type RecordsHarness struct {
	Records        interp.Records
	CreateTask     func(mutationID string, t store.Task) (store.Receipt, error)
	AnswerDecision func(mutationID, repository, taskID, id, answer string) (store.Receipt, error)
}

// NewRecordsHarness returns the double under the contract suite.
func NewRecordsHarness(*testing.T) RecordsHarness {
	r := NewRecords()
	return RecordsHarness{Records: r, CreateTask: r.CreateTask, AnswerDecision: r.AnswerDecision}
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
		var wg sync.WaitGroup
		for worker := range 4 {
			wg.Go(func() {
				for i := range 30 {
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
		if len(a) != 120 || len(d) != 120 || len(q) != 120 {
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
}
