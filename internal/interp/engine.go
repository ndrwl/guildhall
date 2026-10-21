package interp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// EventKind names what happened to a task.
type EventKind string

const (
	// TaskCreated: the task was recorded and has not started.
	TaskCreated EventKind = "task-created"
	// AttemptResult: an Artisan attempt wrote its result file.
	AttemptResult EventKind = "attempt-result"
	// DecisionAnswered: the Guildmaster answered one of the task's decisions.
	DecisionAnswered EventKind = "decision-answered"
)

// Event is one thing that happened to a task, given to Engine.Advance.
type Event struct {
	Kind       EventKind
	Repository string
	Task       string
	// Step and Attempt name the attempt whose result arrived (AttemptResult only).
	Step    string
	Attempt int
	// Decision is the id of the answered decision (DecisionAnswered only).
	Decision string
}

// Wait says what a task waits for after an advance.
type Wait string

const (
	// WaitAttempt: an Artisan attempt is launched and its result is awaited.
	WaitAttempt Wait = "attempt"
	// WaitDecision: a decision is open for the Guildmaster.
	WaitDecision Wait = "decision"
	// WaitDirector: an attempt failed or stopped; the Director decides what happens next.
	WaitDirector Wait = "director"
)

// Next is where a task stands after an advance.
type Next struct {
	Wait Wait
	Step string
	// Attempt is the attempt number for WaitAttempt and WaitDirector.
	Attempt int
	// Decision is the open decision's id for WaitDecision, and for WaitDirector the decision the
	// Director must act on: a rejected loop exit or a changes_requested answer without a loop rule.
	Decision string
}

var (
	// ErrUnsupported reports a step, actor or outcome the engine does not run yet, such as an engine
	// step other than deliver, a Director stage, a rejected outcome or a loop back to a non-Artisan step.
	ErrUnsupported = errors.New("not supported by the engine")
	// ErrSpecGate reports an accepted spec whose digest is not the one the Guildmaster approved.
	ErrSpecGate = errors.New("spec gate")
	// ErrEvent reports an event that does not match the task's records.
	ErrEvent = errors.New("event does not match the task's records")
	// ErrRevision reports a workflow definition that is not the revision the task bound.
	ErrRevision = errors.New("workflow is not the task's bound revision")
	// ErrTaskClosed reports a task that is done or cancelled.
	ErrTaskClosed = errors.New("task is not open")
	// ErrOutcome reports a review outcome that breaks its round's contract: it is stale (not bound to the
	// revisions its attempt was given) or leaves a finding of the previous round without a disposition.
	ErrOutcome = errors.New("review outcome refused")
	// ErrReviewGate reports a deliver step reached without a current, non-blocking, non-rejected
	// outcome for every preceding review or document-review step (docs/interpreter.md#review-gate).
	ErrReviewGate = errors.New("review gate")
)

// Engine is the deterministic workflow interpreter: it advances a task through its bound workflow
// revision from store records alone, with no model and no condition language (docs/interpreter.md).
//
// Advance takes one event, validates it against the records, records what it carries and then
// moves the task forward through the steps in order until something must be waited for:
//
//   - an Artisan step records a new attempt, renders its prompt (Prompter) and launches it (Launcher),
//     then waits for its result;
//   - a Guildmaster step opens a typed decision bound to its subject and waits for the answer;
//   - deliver under the "approval" merge policy opens the merge decision and waits.
//
// A succeeded result copies the attempt's outputs into DocumentDir and records them as documents, the
// first under the step's output port; a failed or stopped attempt waits for the Director.
// Each Artisan attempt first records its binding, what it was given; it stands while that is current.
// A blocking review outcome or changes_requested answer follows the step's loop rule up to its max,
// then opens a loop-exit decision; deliver first checks the review gate (docs/interpreter.md#rounds-and-loops).
// An approved Guildmaster approval records the approved document as the accepted spec; every step taking
// an accepted spec checks it against the approval first (the spec gate).
// Every decision opened and every failed attempt is also sent to the Director's inbox through Outbox.
//
// Every write uses a mutation id derived from the task, step, round and record, and Advance derives its
// position from the records rather than the event, so repeating an advance after a crash at any point
// replays its writes and records nothing twice: an attempt recorded but never launched is rendered
// again and launched with the same identity, never recorded again.
// An attempt is marked launched in its directory only after Launch returns; a crash between the two
// relaunches the same identity, and refusing a live duplicate is the launcher's ownership record (L3).
//
// The checks gate, publish, validate, automatic merging, Director stages and loops back to a step not run
// by an Artisan are refused with ErrUnsupported. Advance is not safe for concurrent use on the same task.
type Engine struct {
	Records  Records
	Launcher Launcher
	Prompter Prompter
	Outbox   Outbox
	// Workflow returns the definition a task's revision names; Advance refuses one with another digest (ErrRevision).
	Workflow func(workflow.Revision) (*workflow.Definition, error)
	// Config gives the profile adapters and the repositories' merge policies.
	// A step without a profile runs DefaultAdapter.
	Config            *config.Config
	DefaultAdapter    string
	StandardsRevision string
	// DocumentDir is the absolute Director document directory every recorded document path is relative to.
	DocumentDir string
	// StateDir is the absolute directory holding attempt directories, <repo>/<task>/<step>-<n>, and each
	// task's working directory, <repo>/<task>/work, until worktree custody is defined.
	StateDir string
}

// Advance applies ev to its task and returns what the task now waits for. See Engine.
func (e *Engine) Advance(ctx context.Context, ev Event) (Next, error) {
	t, err := e.Records.Task(ev.Repository, ev.Task)
	if err != nil {
		return Next{}, fmt.Errorf("interp: %w", err)
	}
	if t.State != store.TaskOpen {
		return Next{}, fmt.Errorf("interp: %w: %s/%s is %s", ErrTaskClosed, t.Repository, t.ID, t.State)
	}
	def, err := e.Workflow(t.Workflow)
	if err != nil {
		return Next{}, fmt.Errorf("interp: workflow %s: %w", t.Workflow.Name, err)
	}
	if def.Name != t.Workflow.Name || def.Digest() != t.Workflow.Digest {
		return Next{}, fmt.Errorf("interp: %w: task bound %s %s, definition is %s %s", ErrRevision, t.Workflow.Name, t.Workflow.Digest, def.Name, def.Digest())
	}
	if err := workflow.Validate(def); err != nil {
		return Next{}, fmt.Errorf("interp: %w", err)
	}
	r := &run{e: e, ctx: ctx, task: t, def: def}
	if err := r.load(); err != nil {
		return Next{}, err
	}
	if err := r.intake(ev); err != nil {
		return Next{}, err
	}
	for _, step := range def.Steps {
		done, next, err := r.step(step)
		if err != nil {
			return Next{}, fmt.Errorf("interp: %s/%s step %s: %w", t.Repository, t.ID, step.ID, err)
		}
		if !done {
			return next, nil
		}
	}
	return Next{}, fmt.Errorf("interp: %w: workflow %s ends without a step to wait for", ErrUnsupported, def.Name)
}

// run is one advance of one task over a snapshot of its records, kept current as it writes.
type run struct {
	e         *Engine
	ctx       context.Context
	task      store.Task
	def       *workflow.Definition
	attempts  []store.StageAttempt
	documents []store.DocumentRef
	decisions []store.Decision
}

func (r *run) load() error {
	var err error
	repo, id := r.task.Repository, r.task.ID
	if r.attempts, err = r.e.Records.Attempts(repo, id); err != nil {
		return fmt.Errorf("interp: %w", err)
	}
	if r.documents, err = r.e.Records.Documents(repo, id); err != nil {
		return fmt.Errorf("interp: %w", err)
	}
	if r.decisions, err = r.e.Records.Decisions(repo, id); err != nil {
		return fmt.Errorf("interp: %w", err)
	}
	return nil
}

// intake validates ev against the records and records the result it announces.
func (r *run) intake(ev Event) error {
	switch ev.Kind {
	case TaskCreated:
		return nil
	case AttemptResult:
		i := slices.IndexFunc(r.def.Steps, func(s workflow.Step) bool { return s.ID == ev.Step })
		a := r.attempt(ev.Step, ev.Attempt)
		if i < 0 || a == nil {
			return fmt.Errorf("interp: %w: no attempt %d of step %q", ErrEvent, ev.Attempt, ev.Step)
		}
		if a.Outcome != "" {
			return nil
		}
		return r.takeResult(r.def.Steps[i], a)
	case DecisionAnswered:
		d := r.decision(ev.Decision)
		switch {
		case d == nil:
			return fmt.Errorf("interp: %w: no decision %q", ErrEvent, ev.Decision)
		case !d.Answered:
			return fmt.Errorf("interp: %w: decision %q is not answered", ErrEvent, ev.Decision)
		}
		return nil
	default:
		return fmt.Errorf("interp: %w: unknown event kind %q", ErrEvent, ev.Kind)
	}
}

// step moves the task through one step and reports whether it is complete; when it is not,
// next is what the task waits for.
func (r *run) step(s workflow.Step) (done bool, next Next, err error) {
	switch actor := s.Actor(); {
	case actor == workflow.ArtisanActor:
		return r.artisan(s)
	case actor == workflow.GuildmasterActor:
		return r.guildmaster(s)
	case actor == workflow.EngineActor && s.Type == "deliver":
		return r.deliver(s)
	default:
		return false, Next{}, fmt.Errorf("%w: %s steps run by the %s", ErrUnsupported, s.Type, actor)
	}
}

func (r *run) artisan(s workflow.Step) (bool, Next, error) {
	a := r.latestAttempt(s.ID)
	switch {
	case a == nil:
		next, err := r.start(s)
		return false, next, err
	case a.Outcome == "":
		next := Next{Wait: WaitAttempt, Step: s.ID, Attempt: a.Number}
		if exists(filepath.Join(r.attemptDir(s.ID, a.Number), launchedFile)) || exists(r.resultFile(s.ID, a.Number)) {
			return false, next, nil
		}
		// Recorded but never marked launched: the advance stopped between the two.
		name, _, err := r.prepare(s)
		if err != nil {
			return false, Next{}, err
		}
		b, err := r.binding(s.ID, a.Number)
		if err != nil {
			return false, Next{}, err
		}
		return false, next, r.launch(s, a.Number, name, r.boundInputs(b))
	case a.Outcome != store.AttemptSucceeded:
		err := r.notify(fmt.Sprintf("attempt-failed.%s-%d", s.ID, a.Number), "attempt-failed",
			fmt.Sprintf("Attempt %d of step %s %s; the Director decides what happens next.", a.Number, s.ID, a.Outcome))
		return false, Next{Wait: WaitDirector, Step: s.ID, Attempt: a.Number}, err
	}
	// The latest attempt succeeded: it stands unless a loop sent the task back here or its inputs moved.
	pending, err := r.pending(s)
	if err != nil {
		return false, Next{}, err
	}
	stale, err := r.stale(s, a.Number)
	if err != nil {
		return false, Next{}, err
	}
	if len(pending) > 0 || stale {
		next, err := r.start(s)
		return false, next, err
	}
	if isReview(s) {
		return r.reviewed(s, a.Number)
	}
	return true, Next{}, nil
}

// prepare resolves the adapter and the inputs of an Artisan step; both are checked before anything is recorded.
func (r *run) prepare(s workflow.Step) (string, []render.Input, error) {
	name := r.e.DefaultAdapter
	if p := s.Profile(); p != "" {
		profile, ok := r.e.Config.Profiles[p]
		if !ok {
			return "", nil, fmt.Errorf("%w: %q", config.ErrUnknownProfile, p)
		}
		name = profile.Adapter
	}
	if err := CheckAdapter(name); err != nil {
		return "", nil, err
	}
	inputs, err := r.inputs(s)
	return name, inputs, err
}

// start records the step's binding and its next attempt, then launches it with its wired inputs plus
// the engine's extras (previous outcome, loop feedback).
func (r *run) start(s workflow.Step) (Next, error) {
	name, inputs, err := r.prepare(s)
	if err != nil {
		return Next{}, err
	}
	var revisions *workflow.Revisions
	if isReview(s) {
		want, err := r.want(s, inputs)
		if err != nil {
			return Next{}, err
		}
		revisions = &want
	}
	extras, err := r.extras(s)
	if err != nil {
		return Next{}, err
	}
	inputs = append(inputs, extras...)
	n := 1
	if a := r.latestAttempt(s.ID); a != nil {
		n = a.Number + 1
	}
	if err := r.bind(s, n, inputs, revisions); err != nil {
		return Next{}, err
	}
	rc, err := r.e.Records.StartAttempt(r.mutation("attempt", s.ID, strconv.Itoa(n)), r.task.Repository, r.task.ID, s.ID)
	if err != nil {
		return Next{}, err
	}
	if rc.Attempt != n {
		return Next{}, fmt.Errorf("recorded attempt %d, expected %d: another writer advanced the task", rc.Attempt, n)
	}
	r.attempts = append(r.attempts, store.StageAttempt{Repository: r.task.Repository, TaskID: r.task.ID, Step: s.ID, Number: n})
	return Next{Wait: WaitAttempt, Step: s.ID, Attempt: n}, r.launch(s, n, name, inputs)
}

// launchedFile marks an attempt directory whose attempt Launch accepted.
const launchedFile = "launched.json"

// launch renders attempt n afresh into its prompt directory and launches it.
func (r *run) launch(s workflow.Step, n int, adapterName string, inputs []render.Input) error {
	dir := r.attemptDir(s.ID, n)
	prompt := filepath.Join(dir, "prompt")
	if err := os.RemoveAll(prompt); err != nil {
		return err
	}
	work := filepath.Join(r.e.StateDir, r.task.Repository, r.task.ID, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	rendered, err := r.e.Prompter.Prompt(render.Stage{Repository: r.task.Repository, Task: r.task.ID, Step: s, Attempt: n, Dir: prompt}, inputs)
	if err != nil {
		return err
	}
	h, err := r.e.Launcher.Launch(r.ctx, Spec{
		Identity:   r.identity(s.ID, n),
		Adapter:    adapterName,
		WorkDir:    work,
		PromptFile: rendered.PromptPath,
		ResultFile: r.resultFile(s.ID, n),
		Manifest:   rendered.Manifest,
	})
	if err != nil {
		return fmt.Errorf("launch attempt %d: %w", n, err)
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, launchedFile), data)
}

// takeResult records a finished attempt: its outputs as documents when it succeeded, then its outcome.
func (r *run) takeResult(s workflow.Step, a *store.StageAttempt) error {
	res, err := adapter.ReadResult(r.resultFile(s.ID, a.Number), r.identity(s.ID, a.Number))
	if err != nil {
		return fmt.Errorf("interp: result of %s attempt %d: %w", s.ID, a.Number, err)
	}
	outcome := store.AttemptFailed
	if res.Status == adapter.StatusSucceeded {
		if err := r.recordOutputs(s, a.Number, res.Outputs); err != nil {
			return fmt.Errorf("interp: outputs of %s attempt %d: %w", s.ID, a.Number, err)
		}
		if err := r.stall(s, a.Number); err != nil {
			return fmt.Errorf("interp: %w", err)
		}
		outcome = store.AttemptSucceeded
	}
	n := strconv.Itoa(a.Number)
	if _, err := r.e.Records.FinishAttempt(r.mutation("attempt-finish", s.ID, n), r.task.Repository, r.task.ID, s.ID, a.Number, outcome); err != nil {
		return fmt.Errorf("interp: %w", err)
	}
	a.Outcome = outcome
	return nil
}

// recordOutputs copies each output into the document directory and records it; the first is the step's
// output port, named <step>-<n>-<port>, and later ones add -<index>. Code heads and review outcomes are
// checked before anything is recorded.
func (r *run) recordOutputs(s workflow.Step, n int, outputs []adapter.Output) error {
	kind, _ := workflow.LookupKind(s.Type, s.Version)
	if len(kind.Outputs) != 1 {
		return fmt.Errorf("%w: %s has %d output ports", ErrUnsupported, s.Type, len(kind.Outputs))
	}
	port := kind.Outputs[0]
	if len(outputs) == 0 {
		return fmt.Errorf("a succeeded result lists no output for port %s", port.Name)
	}
	work := filepath.Join(r.e.StateDir, r.task.Repository, r.task.ID, "work")
	root, err := os.OpenRoot(work)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	contents := make([][]byte, len(outputs))
	for i, o := range outputs {
		if !fs.ValidPath(o.Path) || o.Path == "." || strings.ContainsAny(o.Path, `\:`) {
			return fmt.Errorf("output path %q is not a clean relative slash-separated path", o.Path)
		}
		data, err := root.ReadFile(filepath.FromSlash(o.Path))
		if err != nil {
			return err
		}
		if i == 0 {
			if err := checkPort(port.Type, data); err != nil {
				return err
			}
			if isReview(s) {
				if err := r.checkOutcome(s, n, data); err != nil {
					return err
				}
			}
		}
		contents[i] = data
	}
	for i, o := range outputs {
		data := contents[i]
		name := fmt.Sprintf("%s-%d-%s", s.ID, n, port.Name)
		if i > 0 {
			name += "-" + strconv.Itoa(i+1)
		}
		if r.document(name) != nil {
			continue
		}
		d := store.DocumentRef{
			Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
			Path:   path.Join("tasks", r.task.Repository, r.task.ID, fmt.Sprintf("%s-%d", s.ID, n), o.Path),
			SHA256: digest(data),
		}
		if err := r.put(d, data); err != nil {
			return err
		}
	}
	return nil
}

// checkPort refuses output content that is not the port's artefact type.
func checkPort(t workflow.ArtifactType, data []byte) error {
	switch t {
	case workflow.CodeHead:
		if !isHead(strings.TrimSpace(string(data))) {
			return fmt.Errorf("code head output is not a 40 or 64 lowercase hex object id")
		}
	case workflow.ReviewResult:
		if _, err := workflow.ParseReviewOutcome(data); err != nil {
			return err
		}
	}
	return nil
}

// put writes data to the document's file, unless the same content is already there, and records it.
func (r *run) put(d store.DocumentRef, data []byte) error {
	file := filepath.Join(r.e.DocumentDir, filepath.FromSlash(d.Path))
	if prior, err := os.ReadFile(file); err == nil {
		if digest(prior) != d.SHA256 {
			return fmt.Errorf("document file %s already holds other content", d.Path)
		}
	} else if err := writeFile(file, data); err != nil {
		return err
	}
	if _, err := r.e.Records.PutDocument(r.mutation("document", d.Name), d); err != nil {
		return err
	}
	r.documents = append(r.documents, d)
	return nil
}

func (r *run) guildmaster(s workflow.Step) (bool, Next, error) {
	d := r.latestDecision(s.ID)
	if d == nil {
		next, err := r.ask(s)
		return false, next, err
	}
	if !d.Answered {
		return false, Next{Wait: WaitDecision, Step: s.ID, Decision: d.ID}, r.notifyDecision(*d, s.ID)
	}
	inputs, err := r.inputs(s)
	if err != nil {
		return false, Next{}, err
	}
	subject, err := r.subject(s, inputs)
	if err != nil {
		return false, Next{}, err
	}
	if d.Subject == nil || *d.Subject != *subject {
		// The subject moved since this round was answered: ask about the new one.
		next, err := r.ask(s)
		return false, next, err
	}
	switch d.Choice {
	case string(workflow.VerdictApproved):
	case changesRequested:
		return r.changes(s, Next{Wait: WaitDirector, Step: s.ID, Decision: d.ID}, d.Subject)
	default:
		return false, Next{}, fmt.Errorf("decision %s: %w: outcome %s", d.ID, ErrUnsupported, d.Choice)
	}
	if s.Type == "guildmaster-approval" {
		return true, Next{}, r.accept(s, *d)
	}
	if s.Type == "review" || s.Type == "document-review" {
		return true, Next{}, r.humanOutcome(*d)
	}
	return true, Next{}, nil
}

func (r *run) humanOutcome(d store.Decision) error {
	name := d.ID + "-outcome"
	if r.document(name) != nil {
		return nil
	}
	binding := r.document(d.ID + "-revisions")
	if binding == nil {
		return fmt.Errorf("decision %s has no review revisions", d.ID)
	}
	data, err := r.read(*binding)
	if err != nil {
		return err
	}
	var revisions workflow.Revisions
	if err := json.Unmarshal(data, &revisions); err != nil {
		return err
	}
	o := workflow.ReviewOutcome{Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.HumanReviewer, Verdict: workflow.Verdict(d.Choice), Revisions: revisions}
	if err := o.Validate(); err != nil {
		return err
	}
	data, err = json.Marshal(o)
	if err != nil {
		return err
	}
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: path.Join("tasks", r.task.Repository, r.task.ID, d.ID, "outcome.json"), SHA256: digest(data)}, data)
}

// subjectPorts names the input whose artefact each Guildmaster or engine decision binds to.
var subjectPorts = map[string]struct {
	port string
	kind store.DecisionKind
}{
	"guildmaster-approval": {"spec", store.DecisionApproval},
	"document-review":      {"subject", store.DecisionReview},
	"review":               {"head", store.DecisionReview},
	"deliver":              {"head", store.DecisionMerge},
}

// ask opens the next decision of a Guildmaster step, bound to its subject.
func (r *run) ask(s workflow.Step) (Next, error) {
	inputs, err := r.inputs(s)
	if err != nil {
		return Next{}, err
	}
	k := r.nextRound(s.ID)
	ask, err := render.Question(render.Stage{Repository: r.task.Repository, Task: r.task.ID, Step: s, Attempt: k}, inputs)
	if err != nil {
		return Next{}, err
	}
	return r.open(s, k, inputs, ask)
}

// subject returns what a decision of step s binds to, from its current inputs.
func (r *run) subject(s workflow.Step, inputs []render.Input) (*store.Subject, error) {
	sp, ok := subjectPorts[s.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %s decisions", ErrUnsupported, s.Type)
	}
	i := slices.IndexFunc(inputs, func(in render.Input) bool { return in.Port == sp.port })
	if i < 0 {
		return nil, fmt.Errorf("input %s is not wired", sp.port)
	}
	if s.Type == "review" || s.Type == "deliver" {
		h, err := r.head(inputs[i])
		return &store.Subject{Kind: store.SubjectHead, Digest: h}, err
	}
	return &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + inputs[i].SHA256}, nil
}

func (r *run) open(s workflow.Step, k int, inputs []render.Input, ask render.Ask) (Next, error) {
	sp := subjectPorts[s.Type]
	subject, err := r.subject(s, inputs)
	if err != nil {
		return Next{}, err
	}
	d := store.Decision{
		Repository: r.task.Repository, TaskID: r.task.ID, ID: fmt.Sprintf("%s-%d", s.ID, k),
		Kind: sp.kind, Subject: subject, Options: ask.Options, Question: ask.Question,
	}
	if s.Type == "review" || s.Type == "document-review" {
		name := d.ID + "-revisions"
		if r.document(name) == nil {
			revisions := workflow.Revisions{StandardsRevision: r.e.StandardsRevision}
			if s.Type == "document-review" {
				revisions.Subject.DocumentDigest = subject.Digest
			} else {
				revisions.Subject.CodeHead = subject.Digest
				for _, in := range inputs {
					if in.Port == "spec" {
						revisions.SpecDigest = "sha256:" + in.SHA256
					}
				}
			}
			o := workflow.ReviewOutcome{Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.HumanReviewer, Verdict: workflow.VerdictApproved, Revisions: revisions}
			if err := o.Validate(); err != nil {
				return Next{}, err
			}
			data, err := json.Marshal(revisions)
			if err != nil {
				return Next{}, err
			}
			if err := r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: path.Join("tasks", r.task.Repository, r.task.ID, d.ID, "revisions.json"), SHA256: digest(data)}, data); err != nil {
				return Next{}, err
			}
		}
	}
	if _, err := r.e.Records.OpenDecision(r.mutation("decision", d.ID), d); err != nil {
		return Next{}, err
	}
	r.decisions = append(r.decisions, d)
	return Next{Wait: WaitDecision, Step: s.ID, Decision: d.ID}, r.notifyDecision(d, s.ID)
}

// accept records the approved document as the accepted spec, after checking it is the one the answer bound.
func (r *run) accept(s workflow.Step, d store.Decision) error {
	name := d.ID + "-spec"
	if r.document(name) != nil {
		return nil
	}
	inputs, err := r.inputs(s)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(inputs, func(in render.Input) bool { return in.Port == "spec" })
	if d.Subject == nil || "sha256:"+inputs[i].SHA256 != d.Subject.Digest {
		return fmt.Errorf("%w: decision %s approved another document than %s", ErrSpecGate, d.ID, inputs[i].Name)
	}
	doc := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: inputs[i].Name, SHA256: inputs[i].SHA256}
	if _, err := r.e.Records.PutDocument(r.mutation("document", name), doc); err != nil {
		return err
	}
	r.documents = append(r.documents, doc)
	return nil
}

// deliver opens the merge decision under the approval policy; merging itself is later work.
func (r *run) deliver(s workflow.Step) (bool, Next, error) {
	i := slices.IndexFunc(r.e.Config.Repositories, func(c config.Repository) bool { return c.ID == r.task.Repository })
	if i < 0 {
		return false, Next{}, fmt.Errorf("repository %s is not configured", r.task.Repository)
	}
	if policy := r.e.Config.Repositories[i].MergePolicy; policy != config.MergeApproval {
		return false, Next{}, fmt.Errorf("%w: merge policy %q", ErrUnsupported, policy)
	}
	inputs, err := r.inputs(s)
	if err != nil {
		return false, Next{}, err
	}
	in, err := r.input(s, "head")
	if err != nil {
		return false, Next{}, err
	}
	h, err := r.head(in)
	if err != nil {
		return false, Next{}, err
	}
	if err := r.gate(s, h); err != nil {
		return false, Next{}, err
	}
	d := r.latestDecision(s.ID)
	switch {
	case d == nil || d.Subject == nil || d.Subject.Digest != h:
		k := r.nextRound(s.ID)
		ask := render.Ask{
			Question: fmt.Sprintf("Merge needed for %s/%s, step %s, attempt %d: head %s.\n\nAnswer with one of: %s.\n",
				r.task.Repository, r.task.ID, s.ID, k, h, strings.Join(mergeOptions, ", ")),
			Options: mergeOptions,
		}
		next, err := r.open(s, k, inputs, ask)
		return false, next, err
	case !d.Answered:
		return false, Next{Wait: WaitDecision, Step: s.ID, Decision: d.ID}, r.notifyDecision(*d, s.ID)
	default:
		return false, Next{}, fmt.Errorf("%w: delivering after the merge decision", ErrUnsupported)
	}
}

// mergeOptions are the answers the merge decision offers.
var mergeOptions = []string{"merge", "hold"}

// inputs resolves every wired input of s, in the kind's port order, to its latest recorded document,
// applying the spec gate to accepted specs.
func (r *run) inputs(s workflow.Step) ([]render.Input, error) {
	kind, _ := workflow.LookupKind(s.Type, s.Version)
	var out []render.Input
	for _, port := range kind.Inputs {
		for _, ref := range s.Inputs[port.Name] {
			in, err := r.resolve(port, ref)
			if err != nil {
				return nil, err
			}
			out = append(out, in)
		}
	}
	return out, nil
}

// input resolves the first reference wired to one port of s.
func (r *run) input(s workflow.Step, name string) (render.Input, error) {
	kind, _ := workflow.LookupKind(s.Type, s.Version)
	i := slices.IndexFunc(kind.Inputs, func(p workflow.Port) bool { return p.Name == name })
	if i < 0 || len(s.Inputs[name]) == 0 {
		return render.Input{}, fmt.Errorf("input %s is not wired", name)
	}
	return r.resolve(kind.Inputs[i], s.Inputs[name][0])
}

func (r *run) resolve(port workflow.Port, ref string) (render.Input, error) {
	from, out, _ := strings.Cut(ref, ".")
	doc, source, err := r.output(from, out)
	if err != nil {
		return render.Input{}, fmt.Errorf("input %s: %w", port.Name, err)
	}
	if port.Type == workflow.AcceptedSpec {
		if err := r.specGate(source, doc); err != nil {
			return render.Input{}, err
		}
	}
	return render.Input{Port: port.Name, Name: doc.Path, SHA256: doc.SHA256, Path: filepath.Join(r.e.DocumentDir, filepath.FromSlash(doc.Path))}, nil
}

// specGate checks that an accepted spec is the document its approval decision approved.
func (r *run) specGate(source string, doc store.DocumentRef) error {
	d := r.decision(source)
	switch {
	case d == nil || !d.Answered || (d.Kind != store.DecisionApproval || d.Choice != string(workflow.VerdictApproved)) && (d.Kind != store.DecisionLoopExit || d.Choice != "accept"):
		return fmt.Errorf("%w: %s has no approved decision", ErrSpecGate, doc.Name)
	case d.Subject == nil || d.Subject.Digest != "sha256:"+doc.SHA256:
		return fmt.Errorf("%w: accepted spec %s is sha256:%s, decision %s approved %v", ErrSpecGate, doc.Name, doc.SHA256, d.ID, d.Subject)
	}
	return nil
}

// output returns the latest recorded document of one step's output port and its source id.
func (r *run) output(step, port string) (store.DocumentRef, string, error) {
	var best *store.DocumentRef
	bestRound, bestExit := 0, false
	source := ""
	for i, d := range r.documents {
		mid, ok := strings.CutPrefix(d.Name, step+"-")
		if !ok {
			continue
		}
		mid, ok = strings.CutSuffix(mid, "-"+port)
		if !ok {
			continue
		}
		number, exit := strings.CutPrefix(mid, "x")
		if !isNumber(number) {
			continue
		}
		n, _ := strconv.Atoi(number)
		if exit {
			j := slices.IndexFunc(r.def.Steps, func(s workflow.Step) bool { return s.ID == step })
			if j < 0 {
				continue
			}
			fs, err := r.firings(r.def.Steps[j])
			if err != nil {
				return store.DocumentRef{}, "", err
			}
			if n > len(fs) {
				return store.DocumentRef{}, "", fmt.Errorf("output %s has no originating firing", d.Name)
			}
			n = fs[n-1].round
		}
		if n > bestRound || n == bestRound && exit && !bestExit {
			best, bestRound, bestExit = &r.documents[i], n, exit
			source = step + "-" + mid
		}
	}
	if best == nil {
		return store.DocumentRef{}, "", fmt.Errorf("%s.%s has no recorded document", step, port)
	}
	return *best, source, nil
}

// head reads a code head document, checking its recorded digest.
func (r *run) head(in render.Input) (string, error) {
	data, err := r.read(store.DocumentRef{Name: in.Name, Path: in.Name, SHA256: in.SHA256})
	if err != nil {
		return "", err
	}
	h := strings.TrimSpace(string(data))
	if !isHead(h) {
		return "", fmt.Errorf("document %s is not a code head", in.Name)
	}
	return h, nil
}

// read returns a document's content after checking its recorded digest.
func (r *run) read(d store.DocumentRef) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(r.e.DocumentDir, filepath.FromSlash(d.Path)))
	if err != nil {
		return nil, err
	}
	if digest(data) != d.SHA256 {
		return nil, fmt.Errorf("document %s does not match its recorded digest", d.Path)
	}
	return data, nil
}

func (r *run) notifyDecision(d store.Decision, step string) error {
	return r.notify("decision."+d.ID, "decision", fmt.Sprintf("Decision %s is open for step %s: %s", d.ID, step, d.Kind))
}

// notify sends the Director one message about the task, with an id derived from the task and what
// happened so resending it replays. It names no attempt, so it is accepted whatever the attempt's state.
func (r *run) notify(what, typ, text string) error {
	id := fmt.Sprintf("%s.%s.%s", r.task.Repository, r.task.ID, what)
	if len(id) > 100 {
		id = "advance." + digest([]byte(id))[:64]
	}
	_, err := r.e.Outbox.Send(store.Recipient{Role: store.RecipientDirector}, messaging.Envelope{
		Schema: messaging.SchemaPrefix + strconv.Itoa(messaging.SchemaVersion), ID: id, Recipient: store.RecipientDirector,
		Repository: r.task.Repository, Task: r.task.ID, Type: typ, Text: text,
	})
	return err
}

// mutation derives a write's mutation id from the task and the record, hashing ids longer than the store allows.
func (r *run) mutation(kind string, parts ...string) string {
	id := strings.Join(append([]string{kind, r.task.Repository, r.task.ID}, parts...), ":")
	if len(id) <= 128 {
		return id
	}
	return kind + ":sha256:" + digest([]byte(id))
}

func (r *run) identity(step string, n int) adapter.Identity {
	return adapter.Identity{TaskID: r.task.ID, AttemptID: fmt.Sprintf("%s-%d", step, n)}
}

func (r *run) attemptDir(step string, n int) string {
	return filepath.Join(r.e.StateDir, r.task.Repository, r.task.ID, fmt.Sprintf("%s-%d", step, n))
}

func (r *run) resultFile(step string, n int) string {
	return filepath.Join(r.attemptDir(step, n), "result.json")
}

func (r *run) attempt(step string, n int) *store.StageAttempt {
	for i, a := range r.attempts {
		if a.Step == step && a.Number == n {
			return &r.attempts[i]
		}
	}
	return nil
}

func (r *run) latestAttempt(step string) *store.StageAttempt {
	var last *store.StageAttempt
	for i, a := range r.attempts {
		if a.Step == step && (last == nil || a.Number > last.Number) {
			last = &r.attempts[i]
		}
	}
	return last
}

func (r *run) document(name string) *store.DocumentRef {
	for i, d := range r.documents {
		if d.Name == name {
			return &r.documents[i]
		}
	}
	return nil
}

func (r *run) decision(id string) *store.Decision {
	for i, d := range r.decisions {
		if d.ID == id {
			return &r.decisions[i]
		}
	}
	return nil
}

// latestDecision returns the step's decision of the highest round, <step>-<round>.
func (r *run) latestDecision(step string) *store.Decision {
	var last *store.Decision
	round := 0
	for i, d := range r.decisions {
		if rest, ok := strings.CutPrefix(d.ID, step+"-"); ok {
			if n, err := strconv.Atoi(rest); err == nil && n > round {
				last, round = &r.decisions[i], n
			}
		}
	}
	return last
}

func (r *run) nextRound(step string) int {
	if d := r.latestDecision(step); d != nil {
		n, _ := strconv.Atoi(strings.TrimPrefix(d.ID, step+"-"))
		return n + 1
	}
	return 1
}

func isHead(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// writeFile writes data through a temporary file renamed into place, so a reader sees all of it or nothing.
func writeFile(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), name); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
