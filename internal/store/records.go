package store

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/workflow"
)

// MaxTextBytes bounds every free-text field; larger content belongs in a referenced document.
const MaxTextBytes = 64 << 10

// Per-repository buckets inside the repositories bucket.
var (
	bucketTasks     = []byte("tasks")
	bucketAttempts  = []byte("attempts")
	bucketDocuments = []byte("documents")
	bucketPanes     = []byte("panes")
	bucketDecisions = []byte("decisions")
)

// TaskState is a task's lifecycle state.
type TaskState string

const (
	TaskOpen      TaskState = "open"
	TaskDone      TaskState = "done"
	TaskCancelled TaskState = "cancelled"
)

// TaskRef names a task in any repository of the same Director.
type TaskRef struct {
	Repository string `json:"repository"`
	ID         string `json:"id"`
}

// Task is one unit of work in a repository.
// Its workflow and configuration revisions are bound at creation and never change (R1.62).
type Task struct {
	Repository string `json:"repository"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	// Workflow is the immutable workflow revision the task runs.
	Workflow workflow.Revision `json:"workflow"`
	// ConfigDigest is the "sha256:<hex>" digest of the configuration the task was accepted under.
	ConfigDigest string `json:"config_digest"`
	// DependsOn lists tasks that existed when this task was created.
	DependsOn []TaskRef `json:"depends_on,omitempty"`
	// State is set by the store: CreateTask always records TaskOpen.
	State TaskState `json:"state"`
}

// AttemptOutcome is how a stage attempt finished; empty means it is still running.
type AttemptOutcome string

const (
	AttemptSucceeded AttemptOutcome = "succeeded"
	AttemptFailed    AttemptOutcome = "failed"
	AttemptStopped   AttemptOutcome = "stopped"
)

// StageAttempt is one run of one workflow step for a task.
type StageAttempt struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Step       string `json:"step"`
	// Number counts attempts of this step for this task, starting at 1.
	Number  int            `json:"number"`
	Outcome AttemptOutcome `json:"outcome,omitempty"`
}

// DocumentRef points at a durable document file for a task (R1.13).
type DocumentRef struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Name       string `json:"name"`
	// Path is slash-separated and relative to the Director's document directory,
	// so a store copied between platforms keeps valid references.
	Path string `json:"path"`
	// SHA256 is the lowercase hex sha256 of the file's content.
	SHA256 string `json:"sha256"`
}

// PaneBinding records where a task's Artisan is presented in Herdr.
// It is presentation only: binding, rebinding or unbinding never changes task state (R1.52).
type PaneBinding struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Session    string `json:"session"`
	Workspace  string `json:"workspace"`
	Tab        string `json:"tab"`
	Pane       string `json:"pane"`
}

// DecisionKind says what a decision decides; it is set when the decision is opened.
type DecisionKind string

const (
	DecisionApproval DecisionKind = "approval"
	DecisionReview   DecisionKind = "review"
	DecisionLoopExit DecisionKind = "loop-exit"
	DecisionMerge    DecisionKind = "merge"
	DecisionQuestion DecisionKind = "question"
)

// SubjectKind names what a decision's answer binds to.
type SubjectKind string

const (
	// SubjectDocument binds to a document's content; its digest is "sha256:<64 lowercase hex>".
	SubjectDocument SubjectKind = "document"
	// SubjectHead binds to a commit; its digest is the 40 or 64 lowercase hex object id.
	SubjectHead SubjectKind = "head"
)

// Subject is the exact artefact a decision's answer applies to, so an answer
// cannot be carried over to a document or head that changed after it was asked.
type Subject struct {
	Kind   SubjectKind `json:"kind"`
	Digest string      `json:"digest"`
}

// MaxDecisionOptions bounds the options a decision may offer; MaxOptionBytes bounds each one.
const (
	MaxDecisionOptions = 16
	MaxOptionBytes     = 256
)

// Decision is a question waiting for, or answered by, the Guildmaster (R1.61).
//
// A typed decision has a Kind, a Subject unless its kind is question, and
// optionally Options. When it offers options, its answer's first line, with
// surrounding white space removed, must equal one option exactly; the answer is
// still recorded verbatim, the matched option is Choice and the trimmed text after
// the first line is Note.
// Decisions recorded before typed decisions have no Kind, Subject or Options and
// accept any non-blank answer.
// Answering records the answer only; it never advances a task by itself.
type Decision struct {
	Repository string       `json:"repository"`
	TaskID     string       `json:"task_id"`
	ID         string       `json:"id"`
	Kind       DecisionKind `json:"kind,omitempty"`
	Subject    *Subject     `json:"subject,omitempty"`
	Options    []string     `json:"options,omitempty"`
	Question   string       `json:"question"`
	Answered   bool         `json:"answered"`
	// Answer is recorded exactly as given.
	Answer string `json:"answer,omitempty"`
	// Choice is the option the answer's first line named; empty when no options were offered.
	Choice string `json:"choice,omitempty"`
	// Note is the trimmed text after the first line of an answer to a decision with options.
	Note string `json:"note,omitempty"`
}

// CreateTask records a new open task. A task id is unique within its repository.
func (s *Store) CreateTask(mutationID string, t Task) (Receipt, error) {
	if err := validateTask(t); err != nil {
		return Receipt{}, err
	}
	t.State = TaskOpen
	return s.mutate(mutationID, "create-task", t, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID = t.Repository, t.ID
		for _, dep := range t.DependsOn {
			if _, err := getTask(tx, dep.Repository, dep.ID); err != nil {
				return fmt.Errorf("dependency: %w", err)
			}
		}
		tasks, err := createRepoBucket(tx, t.Repository, bucketTasks)
		if err != nil {
			return err
		}
		if tasks.Get([]byte(t.ID)) != nil {
			return fmt.Errorf("%w: task %s/%s", ErrExists, t.Repository, t.ID)
		}
		return putJSON(tasks, []byte(t.ID), t)
	})
}

// SetTaskState moves an open task to done or cancelled; finished tasks do not change.
func (s *Store) SetTaskState(mutationID, repository, taskID string, state TaskState) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if state != TaskDone && state != TaskCancelled {
		return Receipt{}, invalid("task state %q must be %q or %q", state, TaskDone, TaskCancelled)
	}
	req := struct {
		Repository string    `json:"repository"`
		TaskID     string    `json:"task_id"`
		State      TaskState `json:"state"`
	}{repository, taskID, state}
	return s.mutate(mutationID, "set-task-state", req, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID = repository, taskID
		t, err := openTask(tx, repository, taskID)
		if err != nil {
			return err
		}
		t.State = state
		return putJSON(repoBucket(tx, repository, bucketTasks), []byte(taskID), t)
	})
}

// StartAttempt records a new running attempt of step and returns its number in the receipt.
// It refuses while an earlier attempt of the same step is still running.
func (s *Store) StartAttempt(mutationID, repository, taskID, step string) (Receipt, error) {
	if err := validateIDs(repository, taskID, step); err != nil {
		return Receipt{}, err
	}
	req := StageAttempt{Repository: repository, TaskID: taskID, Step: step}
	return s.mutate(mutationID, "start-attempt", req, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID = repository, taskID
		if _, err := openTask(tx, repository, taskID); err != nil {
			return err
		}
		attempts, err := createRepoBucket(tx, repository, bucketAttempts)
		if err != nil {
			return err
		}
		prefix := recordKey(taskID, step, "")
		last := StageAttempt{}
		c := attempts.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			if err := decode(k, v, &last); err != nil {
				return err
			}
		}
		if last.Number > 0 && last.Outcome == "" {
			return invalid("attempt %d of step %q is still running", last.Number, step)
		}
		a := StageAttempt{Repository: repository, TaskID: taskID, Step: step, Number: last.Number + 1}
		r.Attempt = a.Number
		return putJSON(attempts, attemptKey(taskID, step, a.Number), a)
	})
}

// FinishAttempt records the outcome of a running attempt.
func (s *Store) FinishAttempt(mutationID, repository, taskID, step string, number int, outcome AttemptOutcome) (Receipt, error) {
	if err := validateIDs(repository, taskID, step); err != nil {
		return Receipt{}, err
	}
	switch {
	case number < 1:
		return Receipt{}, invalid("attempt number %d must be positive", number)
	case outcome != AttemptSucceeded && outcome != AttemptFailed && outcome != AttemptStopped:
		return Receipt{}, invalid("attempt outcome %q must be %q, %q or %q", outcome, AttemptSucceeded, AttemptFailed, AttemptStopped)
	}
	req := StageAttempt{Repository: repository, TaskID: taskID, Step: step, Number: number, Outcome: outcome}
	return s.mutate(mutationID, "finish-attempt", req, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID, r.Attempt = repository, taskID, number
		attempts := repoBucket(tx, repository, bucketAttempts)
		key := attemptKey(taskID, step, number)
		var a StageAttempt
		if err := getJSON(attempts, key, &a); err != nil {
			return fmt.Errorf("attempt %d of %s/%s step %q: %w", number, repository, taskID, step, err)
		}
		if a.Outcome != "" {
			return invalid("attempt %d of step %q already finished: %s", number, step, a.Outcome)
		}
		a.Outcome = outcome
		return putJSON(attempts, key, a)
	})
}

// PutDocument records a document reference. A reference never changes once recorded.
func (s *Store) PutDocument(mutationID string, d DocumentRef) (Receipt, error) {
	if err := validateIDs(d.Repository, d.TaskID, d.Name); err != nil {
		return Receipt{}, err
	}
	if !validDocumentPath(d.Path) {
		return Receipt{}, invalid("document path %q must be a clean relative slash-separated path", d.Path)
	}
	if !isLowerHex(d.SHA256, 64) {
		return Receipt{}, invalid("document sha256 %q must be 64 lowercase hex characters", d.SHA256)
	}
	return s.mutate(mutationID, "put-document", d, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID = d.Repository, d.TaskID
		if _, err := getTask(tx, d.Repository, d.TaskID); err != nil {
			return err
		}
		docs, err := createRepoBucket(tx, d.Repository, bucketDocuments)
		if err != nil {
			return err
		}
		key := recordKey(d.TaskID, d.Name)
		if docs.Get(key) != nil {
			return fmt.Errorf("%w: document %q of %s/%s", ErrExists, d.Name, d.Repository, d.TaskID)
		}
		return putJSON(docs, key, d)
	})
}

// BindPane records or replaces a task's presentation pane.
func (s *Store) BindPane(mutationID string, b PaneBinding) (Receipt, error) {
	if err := validateIDs(b.Repository, b.TaskID); err != nil {
		return Receipt{}, err
	}
	for name, v := range map[string]string{"session": b.Session, "workspace": b.Workspace, "tab": b.Tab, "pane": b.Pane} {
		if err := validateText(name, v, 256); err != nil {
			return Receipt{}, err
		}
	}
	return s.mutate(mutationID, "bind-pane", b, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID = b.Repository, b.TaskID
		if _, err := getTask(tx, b.Repository, b.TaskID); err != nil {
			return err
		}
		panes, err := createRepoBucket(tx, b.Repository, bucketPanes)
		if err != nil {
			return err
		}
		return putJSON(panes, []byte(b.TaskID), b)
	})
}

// UnbindPane removes a task's presentation pane.
func (s *Store) UnbindPane(mutationID, repository, taskID string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	req := TaskRef{Repository: repository, ID: taskID}
	return s.mutate(mutationID, "unbind-pane", req, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID = repository, taskID
		panes := repoBucket(tx, repository, bucketPanes)
		if panes == nil || panes.Get([]byte(taskID)) == nil {
			return fmt.Errorf("%w: pane binding of %s/%s", ErrNotFound, repository, taskID)
		}
		return panes.Delete([]byte(taskID))
	})
}

// OpenDecision records an open typed decision for the Guildmaster.
// It refuses an unknown kind, a missing or malformed subject (a question may omit it),
// options that are blank, multi-line, padded, duplicated, longer than MaxOptionBytes
// or more than MaxDecisionOptions, and any answer fields; the decision id is unique within its task.
func (s *Store) OpenDecision(mutationID string, d Decision) (Receipt, error) {
	if err := validateIDs(d.Repository, d.TaskID, d.ID); err != nil {
		return Receipt{}, err
	}
	if err := validateDecision(d); err != nil {
		return Receipt{}, err
	}
	repository, taskID, id := d.Repository, d.TaskID, d.ID
	return s.mutate(mutationID, "open-decision", d, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID = repository, taskID
		if _, err := getTask(tx, repository, taskID); err != nil {
			return err
		}
		decisions, err := createRepoBucket(tx, repository, bucketDecisions)
		if err != nil {
			return err
		}
		key := recordKey(taskID, id)
		if decisions.Get(key) != nil {
			return fmt.Errorf("%w: decision %q of %s/%s", ErrExists, id, repository, taskID)
		}
		return putJSON(decisions, key, d)
	})
}

// AnswerDecision records the Guildmaster's exact answer; a decision is answered once.
// When the decision offers options, an answer whose first line does not name one
// is refused with ErrInvalid and nothing is written (see Decision).
func (s *Store) AnswerDecision(mutationID, repository, taskID, id, answer string) (Receipt, error) {
	if err := validateIDs(repository, taskID, id); err != nil {
		return Receipt{}, err
	}
	if err := validateText("answer", answer, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	req := Decision{Repository: repository, TaskID: taskID, ID: id, Answered: true, Answer: answer}
	return s.mutate(mutationID, "answer-decision", req, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID = repository, taskID
		decisions := repoBucket(tx, repository, bucketDecisions)
		key := recordKey(taskID, id)
		var d Decision
		if err := getJSON(decisions, key, &d); err != nil {
			return fmt.Errorf("decision %q of %s/%s: %w", id, repository, taskID, err)
		}
		if d.Answered {
			return invalid("decision %q of %s/%s is already answered", id, repository, taskID)
		}
		if len(d.Options) > 0 {
			first, rest, _ := strings.Cut(answer, "\n")
			first = strings.TrimSpace(first)
			if !slices.Contains(d.Options, first) {
				return invalid("the first line of the answer to decision %q of %s/%s is %q; it must be one of %q", id, repository, taskID, first, d.Options)
			}
			d.Choice, d.Note = first, strings.TrimSpace(rest)
		}
		d.Answered, d.Answer = true, answer
		return putJSON(decisions, key, d)
	})
}

// Repositories lists the repository ids that have records, in order.
func (s *Store) Repositories() ([]string, error) {
	var out []string
	err := s.view(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRepos).ForEachBucket(func(k []byte) error {
			out = append(out, string(k))
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: repositories: %w", err)
	}
	return out, nil
}

// Task returns one task.
func (s *Store) Task(repository, taskID string) (Task, error) {
	var t Task
	err := s.view(func(tx *bolt.Tx) error {
		var err error
		t, err = getTask(tx, repository, taskID)
		return err
	})
	if err != nil {
		return Task{}, fmt.Errorf("store: %w", err)
	}
	return t, nil
}

// Tasks lists a repository's tasks in id order.
func (s *Store) Tasks(repository string) ([]Task, error) {
	return list[Task](s, repository, bucketTasks, nil)
}

// Attempts lists a task's stage attempts by step, then attempt number.
func (s *Store) Attempts(repository, taskID string) ([]StageAttempt, error) {
	return list[StageAttempt](s, repository, bucketAttempts, recordKey(taskID, ""))
}

// Documents lists a task's document references in name order.
func (s *Store) Documents(repository, taskID string) ([]DocumentRef, error) {
	return list[DocumentRef](s, repository, bucketDocuments, recordKey(taskID, ""))
}

// PaneBinding returns a task's presentation pane, or ErrNotFound when it has none.
func (s *Store) PaneBinding(repository, taskID string) (PaneBinding, error) {
	var b PaneBinding
	err := s.view(func(tx *bolt.Tx) error {
		return getJSON(repoBucket(tx, repository, bucketPanes), []byte(taskID), &b)
	})
	if err != nil {
		return PaneBinding{}, fmt.Errorf("store: pane binding of %s/%s: %w", repository, taskID, err)
	}
	return b, nil
}

// AllDecisions lists every decision in a repository by task id, then decision id.
func (s *Store) AllDecisions(repository string) ([]Decision, error) {
	return list[Decision](s, repository, bucketDecisions, nil)
}

// Decisions lists a task's decisions in id order.
func (s *Store) Decisions(repository, taskID string) ([]Decision, error) {
	return list[Decision](s, repository, bucketDecisions, recordKey(taskID, ""))
}

func list[T any](s *Store, repository string, bucket, prefix []byte) ([]T, error) {
	var out []T
	err := s.view(func(tx *bolt.Tx) error {
		b := repoBucket(tx, repository, bucket)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var rec T
			if err := decode(k, v, &rec); err != nil {
				return err
			}
			out = append(out, rec)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: %s of %s: %w", bucket, repository, err)
	}
	return out, nil
}

// repoBucket returns a repository's record bucket, or nil when it has none yet.
func repoBucket(tx *bolt.Tx, repository string, name []byte) *bolt.Bucket {
	repo := tx.Bucket(bucketRepos).Bucket([]byte(repository))
	if repo == nil {
		return nil
	}
	return repo.Bucket(name)
}

func createRepoBucket(tx *bolt.Tx, repository string, name []byte) (*bolt.Bucket, error) {
	repo, err := tx.Bucket(bucketRepos).CreateBucketIfNotExists([]byte(repository))
	if err != nil {
		return nil, err
	}
	return repo.CreateBucketIfNotExists(name)
}

func getTask(tx *bolt.Tx, repository, taskID string) (Task, error) {
	var t Task
	if err := getJSON(repoBucket(tx, repository, bucketTasks), []byte(taskID), &t); err != nil {
		return Task{}, fmt.Errorf("task %s/%s: %w", repository, taskID, err)
	}
	return t, nil
}

func openTask(tx *bolt.Tx, repository, taskID string) (Task, error) {
	t, err := getTask(tx, repository, taskID)
	if err == nil && t.State != TaskOpen {
		err = invalid("task %s/%s is %s", repository, taskID, t.State)
	}
	return t, err
}

// decode reads a record found by a cursor, where a nil value marks a nested bucket.
func decode(k, v []byte, rec any) error {
	if v == nil {
		return fmt.Errorf("%w: %q is a bucket, not a record", ErrNotStore, k)
	}
	if err := json.Unmarshal(v, rec); err != nil {
		return fmt.Errorf("%w: record %q: %w", ErrNotStore, k, err)
	}
	return nil
}

// recordKey joins ids with "/", which no valid id contains, so a prefix scan of
// recordKey(task, "") sees only that task's records.
func recordKey(parts ...string) []byte { return []byte(strings.Join(parts, "/")) }

// attemptKey zero-pads the number so attempts sort numerically.
func attemptKey(taskID, step string, number int) []byte {
	return recordKey(taskID, step, fmt.Sprintf("%010d", number))
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("store: %w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func validateIDs(ids ...string) error {
	for _, id := range ids {
		if !workflow.ValidID(id) {
			return invalid("id %q must be lowercase letters, digits and hyphens, starting with a letter", id)
		}
	}
	return nil
}

func validateText(name, v string, limit int) error {
	switch {
	case strings.TrimSpace(v) == "":
		return invalid("%s must not be empty", name)
	case len(v) > limit:
		return invalid("%s is %d bytes; the limit is %d", name, len(v), limit)
	case !utf8.ValidString(v):
		return invalid("%s is not valid UTF-8", name)
	}
	return nil
}

func validateTask(t Task) error {
	if err := validateIDs(t.Repository, t.ID); err != nil {
		return err
	}
	if err := validateText("title", t.Title, MaxTextBytes); err != nil {
		return err
	}
	if !workflow.ValidID(t.Workflow.Name) {
		return invalid("workflow name %q is not a valid id", t.Workflow.Name)
	}
	for name, d := range map[string]string{"workflow digest": t.Workflow.Digest, "config digest": t.ConfigDigest} {
		if hexPart, ok := strings.CutPrefix(d, "sha256:"); !ok || !isLowerHex(hexPart, 64) {
			return invalid(`%s %q must be "sha256:<64 lowercase hex>"`, name, d)
		}
	}
	if t.State != "" && t.State != TaskOpen {
		return invalid("a new task is always %q, not %q", TaskOpen, t.State)
	}
	for _, dep := range t.DependsOn {
		if err := validateIDs(dep.Repository, dep.ID); err != nil {
			return err
		}
		if dep == (TaskRef{Repository: t.Repository, ID: t.ID}) {
			return invalid("task %s/%s depends on itself", t.Repository, t.ID)
		}
	}
	return nil
}

func validateDecision(d Decision) error {
	if err := validateText("question", d.Question, MaxTextBytes); err != nil {
		return err
	}
	switch d.Kind {
	case DecisionApproval, DecisionReview, DecisionLoopExit, DecisionMerge:
		if d.Subject == nil {
			return invalid("a %s decision needs a subject", d.Kind)
		}
	case DecisionQuestion:
	default:
		return invalid("decision kind %q must be one of approval, review, loop-exit, merge or question", d.Kind)
	}
	if sub := d.Subject; sub != nil {
		hexPart, prefixed := strings.CutPrefix(sub.Digest, "sha256:")
		switch {
		case sub.Kind == SubjectDocument && (!prefixed || !isLowerHex(hexPart, 64)):
			return invalid(`document subject digest %q must be "sha256:<64 lowercase hex>"`, sub.Digest)
		case sub.Kind == SubjectHead && !isLowerHex(sub.Digest, 40) && !isLowerHex(sub.Digest, 64):
			return invalid("head subject digest %q must be a 40 or 64 lowercase hex object id", sub.Digest)
		case sub.Kind != SubjectDocument && sub.Kind != SubjectHead:
			return invalid("subject kind %q must be %q or %q", sub.Kind, SubjectDocument, SubjectHead)
		}
	}
	if len(d.Options) > MaxDecisionOptions {
		return invalid("a decision offers at most %d options, not %d", MaxDecisionOptions, len(d.Options))
	}
	for i, o := range d.Options {
		if err := validateText("option", o, MaxOptionBytes); err != nil {
			return err
		}
		if strings.TrimSpace(o) != o || strings.ContainsAny(o, "\r\n") {
			return invalid("option %q must be one line without surrounding white space", o)
		}
		if slices.Contains(d.Options[:i], o) {
			return invalid("option %q is offered twice", o)
		}
	}
	if d.Answered || d.Answer != "" || d.Choice != "" || d.Note != "" {
		return invalid("a new decision has no answer")
	}
	return nil
}

// validDocumentPath accepts a clean, local, slash-separated relative path.
func validDocumentPath(p string) bool {
	return p != "" && utf8.ValidString(p) && len(p) <= 1024 && !strings.Contains(p, `\`) && path.Clean(p) == p &&
		!path.IsAbs(p) && filepath.IsLocal(filepath.FromSlash(p)) && !strings.Contains(p, ":")
}

func isLowerHex(s string, n int) bool {
	if len(s) != n || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
