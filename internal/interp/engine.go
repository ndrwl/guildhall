package interp

import (
	"context"
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
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
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
	// AttemptRetried: the Director asked for the next attempt of a step whose latest attempt failed or stopped.
	AttemptRetried EventKind = "attempt-retried"
	// StepSentBack: the Director sends a succeeded Artisan step back as a new attempt with Reason as feedback.
	StepSentBack EventKind = "step-sent-back"
)

// Event is one thing that happened to a task, given to Engine.Advance.
type Event struct {
	Kind       EventKind
	Repository string
	Task       string
	// Step and Attempt name the attempt whose result arrived (AttemptResult), that is retried (AttemptRetried),
	// or whose succeeded work the Director sends back (StepSentBack).
	Step    string
	Attempt int
	// Decision is the id of the answered decision (DecisionAnswered only).
	Decision string
	// Reason is the Director's reason for a retry (AttemptRetried), or required feedback for StepSentBack.
	// A retry binds it to the next attempt, whose prompt quotes it, and records it as a Director message once that
	// attempt launches; a retry repeated after a refusal keeps the first bound reason. Send-back records an
	// immutable input document.
	Reason string
}

// Wait says what a task waits for after an advance.
type Wait string

const (
	// WaitAttempt: an Artisan attempt is launched and its result is awaited.
	WaitAttempt Wait = "attempt"
	// WaitDecision: a decision is open for the Guildmaster.
	WaitDecision Wait = "decision"
	// WaitDirector: an attempt failed or stopped, or the forge refused or the Guildmaster held a merge; the
	// Director decides what happens next.
	WaitDirector Wait = "director"
	// WaitChecks: validate waits for the forge's checks of the published head to finish; advancing the task
	// again reads them again.
	WaitChecks Wait = "checks"
	// WaitDone: deliver merged the task's pull request and the task is done.
	WaitDone Wait = "done"
	// WaitDirectorStage: a Director stage's attempt is handed to the Director, whose `step finish` writes its result
	// (docs/interpreter.md#director-stages).
	WaitDirectorStage Wait = "director-stage"
	// WaitPaused: Engine.Hold held the step's next attempt; advancing the task again once the hold lifts starts it.
	WaitPaused Wait = "paused"
	// WaitDependencies: the dependency gate holds the step, the task's first not run by the Director, until every
	// task it depends on is done or the Director overrides (docs/interpreter.md#the-dependency-gate).
	WaitDependencies Wait = "dependencies"
	// WaitMergeQueue: deliver authorized the task's merge and it waits in its repository's merge queue for the
	// front, or its merge is held (docs/interpreter.md#the-merge-queue); advancing it again, as the supervisor's
	// merge-queue watcher does, claims the front when the task is first.
	WaitMergeQueue Wait = "merge-queue"
)

// Next is where a task stands after an advance.
type Next struct {
	Wait Wait
	Step string
	// Attempt is the attempt number for WaitAttempt, WaitDirectorStage and WaitDirector.
	Attempt int
	// Decision is the open decision's id for WaitDecision, and for WaitDirector the decision the
	// Director must act on: a rejected loop exit or a changes_requested answer without a loop rule.
	Decision string
}

var (
	// ErrUnsupported reports a step, actor or outcome the engine does not run yet, such as a rejected outcome or a
	// loop back to a non-Artisan step.
	ErrUnsupported = errors.New("not supported by the engine")
	// ErrSpecGate reports an accepted spec whose digest is not the one the Guildmaster approved.
	ErrSpecGate = errors.New("spec gate")
	// ErrEvent reports an event that does not match the task's records.
	ErrEvent = errors.New("event does not match the task's records")
	// ErrRevision reports a workflow definition that is not the revision the task bound.
	ErrRevision = errors.New("workflow is not the task's bound revision")
	// ErrPreparation reports a published attempt preparation that is incomplete, corrupt or not the
	// attempt's own (docs/interpreter.md#attempt-preparation); nothing is launched or removed.
	ErrPreparation = errors.New("attempt preparation is not valid")
	// ErrTaskClosed reports a task that is done or cancelled.
	ErrTaskClosed = errors.New("task is not open")
	// ErrOutcome reports a review outcome that breaks its round's contract: it is stale (not bound to the
	// revisions its attempt was given) or leaves a finding of the previous round without a disposition.
	ErrOutcome = errors.New("review outcome refused")
	// ErrReviewGate reports a deliver step reached without a current, non-blocking, non-rejected
	// outcome for every preceding review or document-review step (docs/interpreter.md#review-gate).
	ErrReviewGate = errors.New("review gate")
	// ErrValidationGate reports a deliver step whose validation input is not a passed result for the delivered
	// head (docs/interpreter.md#validate).
	ErrValidationGate = errors.New("validation gate")
)

// Engine is the deterministic workflow interpreter: it advances a task through its bound workflow
// revision from store records alone, with no model and no condition language (docs/interpreter.md).
//
// Advance takes one event, validates it against the records, records what it carries and then
// moves the task forward through the steps in order until something must be waited for:
//
//   - before the first step not run by the Director, a task with dependencies passes the dependency gate: until
//     every task it depends on is done, or the Director overrides, it records nothing, sends one dependency-held
//     message per state of its dependencies and waits (WaitDependencies); as it first passes, its untouched branch
//     is fast-forwarded to the current main (docs/interpreter.md#the-dependency-gate);
//   - an Artisan step records a new attempt, renders its prompt (Prompter) and launches it (Launcher),
//     then waits for its result;
//   - a Director stage records and prepares an attempt as an Artisan step does but launches nothing: it sends the
//     Director a director-stage message, marks the attempt launched by the Director and waits for the result the
//     Director's `step finish` writes, taken as an Artisan's is (docs/interpreter.md#director-stages);
//   - a Guildmaster step opens a typed decision bound to its subject and waits for the answer;
//   - publish pushes the head to the task branch and records its pull request (Forge);
//   - validate reads the forge's checks of the published head and waits while they are pending; finished
//     checks are recorded as a result, and a failed one follows the step's failed loop rule
//     (docs/interpreter.md#validate);
//   - deliver checks the repository's merge requirements; when guildmaster_approval is required, or another
//     requirement fails, it opens the merge decision and waits, and otherwise it records its authorization and
//     merges by itself. A merge merges the published pull request through Forge and closes the task, and a hold
//     answer or a refused merge waits for the Director (docs/interpreter.md#publish-and-deliver).
//
// A succeeded result copies the attempt's outputs into DocumentDir and records them as documents, the
// first under the step's output port; a failed or stopped attempt waits for the Director, whose
// attempt-retried for it starts the step's next attempt as a first attempt starts.
// Each Artisan attempt first records its binding, what it was given; it stands while that is current.
// A blocking review outcome or changes_requested answer follows the step's loop rule up to its max,
// then opens a loop-exit decision; deliver first checks the review gate (docs/interpreter.md#rounds-and-loops).
// An approved Guildmaster approval records the approved document as the accepted spec; every step taking
// an accepted spec checks it against the approval first (the spec gate).
// Every decision opened and every failed attempt is also sent to the Director's inbox through Outbox.
//
// Every write uses a mutation id derived from the task, step, round and record, and Advance derives its
// position from the records rather than the event, so repeating an advance after a crash at any point
// replays its writes and records nothing twice: an attempt recorded but never launched is launched
// again with the same identity, never recorded again.
// An attempt is marked launched in its directory only after Launch returns; a lost acknowledgement or a
// crash between the two relaunches the same identity, and refusing a live duplicate is the launcher's
// ownership record (L3).
//
// Because a missing mark does not prove that no worker holds the attempt's files, an attempt's
// preparation is published once and never replaced: the prompt is rendered into <step>-<n>/prompt.partial
// with its manifest as manifest.json, then the directory is renamed to <step>-<n>/prompt. A replay launches
// from a published prompt directory unchanged after checking it against its manifest and the attempt's
// recorded binding, and refuses one that fails with ErrPreparation, launching and removing nothing.
// Only an unpublished prompt.partial, which no launch was given, is removed and rendered again.
//
// The checks gate and loops back to a step not run by an Artisan are refused with ErrUnsupported. Advance is not safe for concurrent use on the same task.
type Engine struct {
	Records  Records
	Launcher Launcher
	Prompter Prompter
	Outbox   Outbox
	// Workflow returns the definition a task's revision names; Advance refuses one with another digest (ErrRevision).
	Workflow func(workflow.Revision) (*workflow.Definition, error)
	// Config gives the profile adapters and the repositories' merge requirements.
	Config            *config.Config
	StandardsRevision string
	// CheckSettings, when set, refuses the model and effort of a step's profile that the profile's adapter cannot
	// honour (adapter.ErrUnsupportedSetting), before anything is recorded, as CheckAdapter refuses an adapter; nil
	// leaves them to the launch.
	CheckSettings func(adapter string, s adapter.Settings) error
	// DocumentDir is the absolute Director document directory every recorded document path is relative to.
	DocumentDir string
	Copy        string
	// StateDir is the absolute directory holding attempt directories, <repo>/<task>/<step>-<n>, and each
	// task's working directory, <repo>/<task>/work.
	StateDir string
	// Workspaces makes a task's working directory a worktree of its repository on branch guildhall/<task> before
	// each attempt launches (docs/interpreter.md#the-working-directory); nil leaves it a plain directory, from
	// which publish cannot push an Artisan's commit.
	Workspaces Workspaces
	// StallInterval enables worker-stalled observations when positive: an advance that reaches a launched,
	// unfinished attempt sends the Director a worker-stalled message once its heartbeat is within the
	// interval and its worker's last output older than it (docs/interpreter.md#worker-stalled-observations).
	// Zero or negative disables them; `task start` and `step done` set it from the config's stall_interval.
	StallInterval time.Duration
	// ChecksRefresh is how long a waiting validate step waits after reading a head's checks before an advance
	// reads that head's checks again; zero or negative reads them at every advance (docs/interpreter.md#validate).
	// `task start` and `step done` set it from the config's checks_refresh.
	ChecksRefresh time.Duration
	// Now is the clock the observations and ChecksRefresh read; nil is wall time.
	Now func() time.Time
	// Base returns the commit a repository's tasks are based on now; nil or an error means every code review
	// is full. Rebases classifies a new head against the last reviewed range (docs/interpreter.md#rebase-aware-review).
	Base    func(repository string) (string, error)
	Rebases Rebases
	// Forge publishes, reads the checks of and merges a repository's task branches; nil refuses publish,
	// validate and merging with ErrUnsupported.
	Forge Forge
	// Hold, when set, is asked before each new attempt, of an Artisan step or a Director stage, is recorded; true
	// holds it: the advance records, renders and launches nothing for it and returns WaitPaused for the step, and a
	// later advance once Hold answers false starts it. An error fails the advance. Results, decisions and messages
	// are never held, nor is the relaunch of an attempt already recorded. `task start`, `step done` and `step retry`
	// hold while the Director's session is paused or stopping (docs/director.md#pause-and-resume). Hold is an early
	// read that spares a held advance its preparation; the hold that decides is Caps.Hold, read in the start's own
	// record, so a pause or stop that commits after Hold answered still holds the attempt.
	Hold func() (bool, error)
	// Caps are the attempt caps every new attempt starts under (Records.StartCappedAttempt); zero fields are no cap.
	// An advance whose next attempt is refused at a cap records and launches nothing, sends the Director one
	// budget-refused message per task and UTC day, and returns an error wrapping store.ErrAttemptCap that names the
	// guard rail's setting; advancing the task again once capacity returns starts it (docs/director.md#guard-rails).
	// `task start`, `step done` and `step retry` set them from the config's guard rails. With Caps.Hold, a start the
	// records refuse with store.ErrDirectorHeld records and launches nothing and the advance waits paused, as Hold
	// holds it; those commands set it with Hold.
	Caps store.AttemptCaps
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
	if err := e.Config.CheckArtisanAdapters(def); err != nil {
		return Next{}, fmt.Errorf("interp: %w", err)
	}
	r := &run{e: e, ctx: ctx, task: t, def: def}
	if err := r.load(); err != nil {
		return Next{}, err
	}
	if err := r.intake(ev); err != nil {
		return Next{}, err
	}
	if err := r.replayMergeBlocked(); err != nil {
		return Next{}, err
	}
	for _, step := range def.Steps {
		if !runsAttempts(step) {
			continue
		}
		a := r.latestAttempt(step.ID)
		if a != nil && a.Outcome == store.AttemptSucceeded && !r.taken(step, a.Number) {
			if err := r.takeResult(step, a); err != nil {
				return Next{}, err
			}
		}
	}
	for _, step := range def.Steps {
		if step.Type == "validate" {
			if err := r.replayValidation(step); err != nil {
				return Next{}, fmt.Errorf("interp: %s/%s step %s: %w", t.Repository, t.ID, step.ID, err)
			}
		}
	}
	gated := false
	for _, step := range def.Steps {
		// The dependency gate stands before the first step not run by the Director, whatever its kind, and before
		// anything of it is recorded or held.
		if !gated && step.Actor() != workflow.DirectorActor {
			gated = true
			held, next, err := r.dependencyGate(step)
			if err != nil {
				return Next{}, fmt.Errorf("interp: %s/%s step %s: %w", t.Repository, t.ID, step.ID, err)
			}
			if held {
				return next, nil
			}
		}
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

// run is one advance of one task over the history view of its records, kept current as it writes.
type run struct {
	e    *Engine
	ctx  context.Context
	task store.Task
	def  *workflow.Definition
	// retry is the attempt-retried event whose failed or stopped attempt the walk follows with the next.
	retry *Event
	*history
}

func (r *run) load() error {
	repo, id := r.task.Repository, r.task.ID
	attempts, err := r.e.Records.Attempts(repo, id)
	if err != nil {
		return fmt.Errorf("interp: %w", err)
	}
	documents, err := r.e.Records.Documents(repo, id)
	if err != nil {
		return fmt.Errorf("interp: %w", err)
	}
	decisions, err := r.e.Records.Decisions(repo, id)
	if err != nil {
		return fmt.Errorf("interp: %w", err)
	}
	rulings, err := r.e.Records.Rulings(repo, id)
	if err != nil {
		return fmt.Errorf("interp: %w", err)
	}
	full, err := r.e.Records.FullReviews(repo, id)
	if err != nil {
		return fmt.Errorf("interp: %w", err)
	}
	r.history = newHistory(repo, id, r.def, r.read, attempts, documents, decisions)
	r.rulings, r.fullReviews = rulings, full
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
	case AttemptRetried:
		return r.retried(ev)
	case StepSentBack:
		return r.sentBack(ev)
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

// retried validates a retry of attempt n of a step: n must have finished failed or stopped and be the step's
// latest attempt, which the walk then follows with the next. When attempt n+1 already exists the event is a
// replay: it resends the reason once n+1 has launched, launching it first when it has not; any other attempt is
// refused.
func (r *run) retried(ev Event) error {
	a := r.attempt(ev.Step, ev.Attempt)
	if !slices.ContainsFunc(r.def.Steps, func(s workflow.Step) bool { return s.ID == ev.Step }) || a == nil {
		return fmt.Errorf("interp: %w: no attempt %d of step %q", ErrEvent, ev.Attempt, ev.Step)
	}
	if a.Outcome != store.AttemptFailed && a.Outcome != store.AttemptStopped {
		state := string(a.Outcome)
		if state == "" {
			state = "running"
		}
		return fmt.Errorf("interp: %w: attempt %d of step %s is %s; only a failed or stopped attempt is retried", ErrEvent, a.Number, a.Step, state)
	}
	switch latest := r.latestAttempt(ev.Step).Number; latest {
	case ev.Attempt:
	case ev.Attempt + 1:
		if exists(filepath.Join(r.attemptDir(ev.Step, ev.Attempt+1), launchedFile)) {
			return r.retryReason(ev)
		}
	default:
		return fmt.Errorf("interp: %w: attempt %d of step %s is not its latest, %d", ErrEvent, a.Number, a.Step, latest)
	}
	r.retry = &ev
	return nil
}

// step moves the task through one step and reports whether it is complete; when it is not,
// next is what the task waits for.
func (r *run) step(s workflow.Step) (done bool, next Next, err error) {
	switch actor := s.Actor(); {
	case runsAttempts(s):
		return r.attempted(s)
	case actor == workflow.GuildmasterActor:
		return r.guildmaster(s)
	case actor == workflow.EngineActor && s.Type == "publish":
		return r.publish(s)
	case actor == workflow.EngineActor && s.Type == "validate":
		return r.validate(s)
	case actor == workflow.EngineActor && s.Type == "deliver":
		return r.deliver(s)
	default:
		return false, Next{}, fmt.Errorf("%w: %s steps run by the %s", ErrUnsupported, s.Type, actor)
	}
}

// runsAttempts reports whether s runs as attempts with result files: an Artisan step, or a Director stage handed to
// the Director (docs/interpreter.md#director-stages).
func runsAttempts(s workflow.Step) bool {
	return s.Actor() == workflow.ArtisanActor || s.Actor() == workflow.DirectorActor
}

// waitFor is what a task waits for while an attempt of s runs.
func waitFor(s workflow.Step) Wait {
	if s.Actor() == workflow.DirectorActor {
		return WaitDirectorStage
	}
	return WaitAttempt
}

func (r *run) attempted(s workflow.Step) (bool, Next, error) {
	a := r.latestAttempt(s.ID)
	switch {
	case a == nil:
		next, err := r.start(s)
		return false, next, err
	case a.Outcome == "":
		next := Next{Wait: waitFor(s), Step: s.ID, Attempt: a.Number}
		if exists(r.resultFile(s.ID, a.Number)) {
			if s.Actor() != workflow.DirectorActor {
				return false, next, nil
			}
			// No launcher announces a Director stage's result, so an advance that stopped after step finish
			// published it is recovered by taking it here.
			if err := r.takeResult(s, a); err != nil {
				return false, Next{}, err
			}
			return r.attempted(s)
		}
		if exists(filepath.Join(r.attemptDir(s.ID, a.Number), launchedFile)) {
			if s.Actor() == workflow.DirectorActor {
				return false, next, nil
			}
			return false, next, r.workerStalled(s.ID, a.Number)
		}
		// Recorded but never marked launched: the advance stopped between the two.
		if held, err := r.admissionHeld(); err != nil {
			return false, Next{}, err
		} else if held {
			return false, Next{Wait: WaitPaused, Step: s.ID}, nil
		}
		rt, _, err := r.prepare(s)
		if err != nil {
			return false, Next{}, err
		}
		b, err := r.binding(s.ID, a.Number)
		if err != nil {
			return false, Next{}, err
		}
		return false, next, r.retryLaunched(s.ID, a.Number, r.launch(s, a.Number, rt, r.boundInputs(b)))
	case a.Outcome != store.AttemptSucceeded && r.retry != nil && r.retry.Step == s.ID && r.retry.Attempt == a.Number:
		next, err := r.start(s)
		return false, next, r.retryLaunched(s.ID, next.Attempt, err)
	case a.Outcome != store.AttemptSucceeded:
		text := fmt.Sprintf("Attempt %d of step %s %s; the Director decides what happens next.", a.Number, s.ID, a.Outcome)
		if a.IntakeRejection != "" {
			text += " Result rejected: " + a.IntakeRejection
		}
		err := r.notify(fmt.Sprintf("attempt-failed.%s-%d", s.ID, a.Number), store.MessageAttemptFailed, text)
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

// artisanRuntime identifies the adapter and profile, with the profile's model and effort; grants are resolved by
// the runtime, not transported here.
type artisanRuntime struct {
	adapter  string
	profile  string
	settings adapter.Settings
}

// prepare resolves the runtime and the inputs of an Artisan step, or the inputs alone of a Director stage, which
// runs no adapter; both are checked, the runtime with CheckAdapter and Engine.CheckSettings, before anything is
// recorded.
func (r *run) prepare(s workflow.Step) (artisanRuntime, []render.Input, error) {
	if s.Actor() == workflow.DirectorActor {
		inputs, err := r.inputs(s)
		return artisanRuntime{}, inputs, err
	}
	p := s.Profile()
	profile, err := r.e.Config.Profile(p)
	if err != nil {
		return artisanRuntime{}, nil, err
	}
	rt := artisanRuntime{adapter: profile.Adapter, profile: p, settings: adapter.Settings{Model: profile.Model, Effort: r.e.Config.EffectiveEffort(profile.Effort)}}
	if err := CheckAdapter(rt.adapter); err != nil {
		return artisanRuntime{}, nil, err
	}
	if r.e.CheckSettings != nil {
		if err := r.e.CheckSettings(rt.adapter, rt.settings); err != nil {
			return artisanRuntime{}, nil, fmt.Errorf("profile %s: %w", rt.profile, err)
		}
	}
	inputs, err := r.inputs(s)
	return rt, inputs, err
}

// retryContext is what attempt n of step is told about the Director's retry of attempt n-1, or nil when the walk
// does not follow such a retry. Only that latest retry is told; the summary is read from the retried attempt's
// result, and is empty when it has none that reads.
func (r *run) retryContext(step string, n int) *retryContext {
	ev := r.retry
	if ev == nil || ev.Step != step || ev.Attempt+1 != n {
		return nil
	}
	a := r.attempt(step, ev.Attempt)
	if a == nil {
		return nil
	}
	c := &retryContext{Attempt: a.Number, Outcome: string(a.Outcome), Reason: ev.Reason, Rejection: a.IntakeRejection}
	if res, err := adapter.ReadResult(r.resultFile(step, a.Number), r.identity(step, a.Number)); err == nil {
		c.Summary = res.Summary
	}
	return c
}

// retryLaunched sends the reason of the retry that attempt n of step follows once its launch succeeded, so a
// refused start or launch sends nothing.
func (r *run) retryLaunched(step string, n int, err error) error {
	ev := r.retry
	if err != nil || ev == nil || ev.Step != step || ev.Attempt+1 != n {
		return err
	}
	r.retry = nil
	return r.retryReason(*ev)
}

// retryReason sends the Director's reason for a retry under an id derived from the retried attempt, so a replay
// resends it. The reason is the one bound to the next attempt when there is one, as its prompt quotes it, so a
// retry repeated with another reason after a refusal tells the timeline what the prompt says. A replay with a
// different reason conflicts with the one already sent and is ignored.
func (r *run) retryReason(ev Event) error {
	if b, err := r.binding(ev.Step, ev.Attempt+1); err == nil && b.Retry != nil {
		ev.Reason = b.Retry.Reason
	}
	if ev.Reason == "" {
		return nil
	}
	err := r.notify(fmt.Sprintf("attempt-retried.%s-%d", ev.Step, ev.Attempt), store.MessageAttemptRetried, ev.Reason)
	if errors.Is(err, store.ErrMutationConflict) {
		return nil
	}
	return err
}

func (r *run) admissionHeld() (bool, error) {
	if r.e.Hold == nil {
		return false, nil
	}
	held, err := r.e.Hold()
	if err != nil {
		return false, fmt.Errorf("hold: %w", err)
	}
	return held, nil
}

// start records the step's binding and its next attempt, then launches it with its wired inputs plus
// the engine's extras (previous outcome, loop feedback).
func (r *run) start(s workflow.Step) (Next, error) {
	if held, err := r.admissionHeld(); err != nil {
		return Next{}, err
	} else if held {
		return Next{Wait: WaitPaused, Step: s.ID}, nil
	}
	rt, inputs, err := r.prepare(s)
	if err != nil {
		return Next{}, err
	}
	n := 1
	if a := r.latestAttempt(s.ID); a != nil {
		n = a.Number + 1
	}
	if r.document(bindingName(s.ID, n)) == nil {
		var b binding
		if isReview(s) {
			want, err := r.want(s, inputs)
			if err != nil {
				return Next{}, err
			}
			b.Revisions = &want
		}
		extras, err := r.extras(s, n)
		if err != nil {
			return Next{}, err
		}
		inputs = append(inputs, extras...)
		if s.Type == "review" {
			resolution, err := r.rebase(s, n, b.Revisions.Subject.CodeHead, &b)
			if err != nil {
				return Next{}, err
			}
			inputs = append(inputs, resolution...)
		}
		b.Retry = r.retryContext(s.ID, n)
		if err := r.bind(s, n, inputs, b); err != nil {
			return Next{}, err
		}
	}
	b, err := r.binding(s.ID, n)
	if err != nil {
		return Next{}, err
	}
	inputs = r.boundInputs(b)
	rc, err := r.e.Records.StartCappedAttempt(r.mutation("attempt", s.ID, strconv.Itoa(n)), r.task.Repository, r.task.ID, s.ID, r.e.Caps)
	if errors.Is(err, store.ErrDirectorHeld) {
		return Next{Wait: WaitPaused, Step: s.ID}, nil
	}
	if refusal, ok := errors.AsType[*store.CapRefusal](err); ok {
		return Next{}, r.budgetRefused(s, n, refusal, err)
	}
	if err != nil {
		return Next{}, err
	}
	if rc.Attempt != n {
		return Next{}, fmt.Errorf("recorded attempt %d, expected %d: another writer advanced the task", rc.Attempt, n)
	}
	r.addAttempt(store.StageAttempt{Repository: r.task.Repository, TaskID: r.task.ID, Step: s.ID, Number: n})
	return Next{Wait: waitFor(s), Step: s.ID, Attempt: n}, r.launch(s, n, rt, inputs)
}

// budgetRefused tells the Director that a cap refused attempt n of step s, once per task and UTC day: a later
// refusal that day, at either cap, conflicts with the message already sent and sends nothing. The message names
// the command that starts the refused attempt: step retry for a refused retry, task start otherwise. It returns
// the refusal named by its guard rail's setting.
func (r *run) budgetRefused(s workflow.Step, n int, refusal *store.CapRefusal, err error) error {
	rail := config.MaxRunningAttemptsKey
	if refusal.Cap == store.CapDaily {
		rail = config.MaxAttemptsPerDayKey
	}
	err = fmt.Errorf("guard rail %s: %w", rail, err)
	again := "guildhall task start for the task"
	if r.retry != nil && r.retry.Step == s.ID && r.retry.Attempt+1 == n {
		again = fmt.Sprintf("guildhall step retry for step %s attempt %d", s.ID, r.retry.Attempt)
	}
	text := fmt.Sprintf("Step %s was not started: guard rail %s, %v. Run %s again when capacity returns.", s.ID, rail, refusal, again)
	if serr := r.notify(store.MessageBudgetRefused+"."+refusal.Day.Format(time.DateOnly), store.MessageBudgetRefused, text); serr != nil && !errors.Is(serr, store.ErrMutationConflict) {
		return errors.Join(err, serr)
	}
	return err
}

// launchedFile marks an attempt directory whose attempt Launch accepted.
const launchedFile = "launched.json"

// launch launches attempt n from its published preparation, rendering and publishing it first when none was;
// a Director stage's attempt is handed to the Director instead.
func (r *run) launch(s workflow.Step, n int, rt artisanRuntime, inputs []render.Input) error {
	work, err := r.workDir()
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	if err := r.recordBase(s, n); err != nil {
		return err
	}
	p, err := r.preparation(s, n, inputs)
	if err != nil {
		return err
	}
	if s.Actor() == workflow.DirectorActor {
		return r.hand(s, n, p)
	}
	h, err := r.e.Launcher.Launch(r.ctx, Spec{
		Identity:   r.identity(s.ID, n),
		Adapter:    rt.adapter,
		WorkDir:    work,
		PromptFile: p.promptFile,
		ResultFile: r.resultFile(s.ID, n),
		Manifest:   p.manifest,
		Copy:       r.e.Copy, Profile: rt.profile,
		Settings: rt.settings,
	})
	if err != nil {
		return fmt.Errorf("launch attempt %d: %w", n, err)
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(r.attemptDir(s.ID, n), launchedFile), data)
}

// directorLauncher is launched.json's launcher for a Director stage's attempt, which runs no process.
const directorLauncher = "director"

// directorMark is launched.json for a Director stage's attempt.
type directorMark struct {
	Launcher string           `json:"launcher"`
	Identity adapter.Identity `json:"identity"`
}

// HandedToDirector reports whether the engine handed the attempt in attempt directory dir to the Director as a
// Director stage: its launched.json names the director launcher. An attempt not yet marked launched, or launched
// by any other launcher, is not.
func HandedToDirector(dir string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, launchedFile))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var m directorMark
	return json.Unmarshal(data, &m) == nil && m.Launcher == directorLauncher, nil
}

// hand gives attempt n of a Director stage to the Director: it sends the Director the stage message, under an id
// derived from the attempt so a replay resends nothing new, and only then marks the attempt launched by the
// Director, so a crash between the two sends the message again as a replay (docs/interpreter.md#director-stages).
func (r *run) hand(s workflow.Step, n int, p prepared) error {
	kind, _ := workflow.LookupKind(s.Type, s.Version)
	var ports, outputs []string
	for _, port := range kind.Outputs {
		ports = append(ports, fmt.Sprintf("%s (%s)", port.Name, port.Type))
		outputs = append(outputs, fmt.Sprintf("--output %s=<file>", port.Name))
	}
	text := fmt.Sprintf("Step %s (%s version %d), attempt %d, is a Director stage for you to run.\nPrompt: %s\nOutput ports: %s\nFinish it with: guildhall step finish --repo %s --task %s --step %s --attempt %d --summary <text> %s, or --failed to abandon it.",
		s.ID, s.Type, s.Version, n, p.promptFile, strings.Join(ports, ", "), r.task.Repository, r.task.ID, s.ID, n, strings.Join(outputs, " "))
	if err := r.notify(fmt.Sprintf("stage.%s-%d", s.ID, n), store.MessageDirectorStage, text); err != nil {
		return err
	}
	data, err := json.Marshal(directorMark{directorLauncher, r.identity(s.ID, n)})
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(r.attemptDir(s.ID, n), launchedFile), data)
}

func (r *run) taken(s workflow.Step, n int) bool {
	return exists(filepath.Join(r.attemptDir(s.ID, n), "intake.json"))
}

// takeResult records a finished attempt: its outputs as documents when it succeeded, then its outcome.
// The store's first finish stands, so an equal outcome the launcher already recorded is not an error;
// unfinished intake still replays
// documents, ruling contests and stall notifications before marking completion.
func (r *run) takeResult(s workflow.Step, a *store.StageAttempt) error {
	res, err := adapter.ReadResult(r.resultFile(s.ID, a.Number), r.identity(s.ID, a.Number))
	if err != nil {
		if errors.Is(err, adapter.ErrMalformedResult) || errors.Is(err, adapter.ErrIdentityMismatch) {
			return r.rejectResult(s, a, err)
		}
		return fmt.Errorf("interp: result of %s attempt %d: %w", s.ID, a.Number, err)
	}
	outcome := store.AttemptFailed
	if res.Status == adapter.StatusSucceeded {
		if err := r.recordOutputs(s, a.Number, res.Outputs); err != nil {
			var rejection *resultRejection
			if errors.As(err, &rejection) {
				return r.rejectResult(s, a, err)
			}
			return fmt.Errorf("interp: outputs of %s attempt %d: %w", s.ID, a.Number, err)
		}
		if err := r.contest(s, a.Number); err != nil {
			return fmt.Errorf("interp: %w", err)
		}
		if err := r.stall(s, a.Number); err != nil {
			return fmt.Errorf("interp: %w", err)
		}
		outcome = store.AttemptSucceeded
	}
	if a.Outcome == "" {
		n := strconv.Itoa(a.Number)
		if _, err := r.e.Records.FinishAttempt(r.mutation("attempt-finish", s.ID, n), r.task.Repository, r.task.ID, s.ID, a.Number, outcome); err != nil {
			return fmt.Errorf("interp: %w", err)
		}
		a.Outcome = outcome
	}
	return writeFile(filepath.Join(r.attemptDir(s.ID, a.Number), "intake.json"), []byte("{}\n"))
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
		if port.Type == workflow.CodeHead {
			return r.recordWorkHead(s, n, port.Name)
		}
		return rejectOutput(fmt.Errorf("a succeeded result lists no output for port %s", port.Name))
	}
	root, err := os.OpenRoot(r.workPath())
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	contents := make([][]byte, len(outputs))
	var review *store.Review
	for i, o := range outputs {
		if !fs.ValidPath(o.Path) || o.Path == "." || strings.ContainsAny(o.Path, `\:`) {
			return rejectOutput(fmt.Errorf("output path %q is not a clean relative slash-separated path", o.Path))
		}
		data, err := root.ReadFile(filepath.FromSlash(o.Path))
		if err != nil {
			return rejectOutput(fmt.Errorf("read output %q: %w", o.Path, err))
		}
		if i == 0 {
			if err := checkPort(port.Type, data); err != nil {
				return rejectOutput(err)
			}
			// A head already recorded stands on replay, even after the branch moved on.
			if port.Type == workflow.CodeHead && r.document(documentName(place{step: s.ID, round: n, part: port.Name})) == nil {
				if err := r.checkHead(s, n, strings.TrimSpace(string(data))); err != nil {
					return err
				}
			}
			if isReview(s) {
				o, err := r.checkOutcome(s, n, data)
				if err != nil {
					return err
				}
				review = reviewOf(*o)
			}
		}
		contents[i] = data
	}
	for i, o := range outputs {
		data := contents[i]
		p := place{step: s.ID, round: n, part: port.Name}
		if i > 0 {
			p.index = i + 1
		}
		name := documentName(p)
		if r.document(name) != nil {
			continue
		}
		d := store.DocumentRef{
			Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
			Path:   path.Join("tasks", r.task.Repository, r.task.ID, r.key(s.ID, n).AttemptID(), o.Path),
			SHA256: digest.Of(data).Hex(),
		}
		if i == 0 {
			d.Review = review
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
		if !digest.ValidHead(strings.TrimSpace(string(data))) {
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
		if digest.Of(prior).Hex() != d.SHA256 {
			return fmt.Errorf("document file %s already holds other content", d.Path)
		}
	} else if err := writeFile(file, data); err != nil {
		return err
	}
	if _, err := r.e.Records.PutDocument(r.mutation("document", d.Name), d); err != nil {
		return err
	}
	r.addDocument(d)
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
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: path.Join("tasks", r.task.Repository, r.task.ID, d.ID, "outcome.json"), SHA256: digest.Of(data).Hex(), Review: reviewOf(o)}, data)
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
	d, err := digest.ParseHex(inputs[i].SHA256)
	return &store.Subject{Kind: store.SubjectDocument, Digest: string(d)}, err
}

func (r *run) open(s workflow.Step, k int, inputs []render.Input, ask render.Ask) (Next, error) {
	sp := subjectPorts[s.Type]
	subject, err := r.subject(s, inputs)
	if err != nil {
		return Next{}, err
	}
	d := store.Decision{
		Repository: r.task.Repository, TaskID: r.task.ID, ID: decisionID(s.ID, k, false),
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
						spec, err := digest.ParseHex(in.SHA256)
						if err != nil {
							return Next{}, err
						}
						revisions.SpecDigest = string(spec)
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
			if err := r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: path.Join("tasks", r.task.Repository, r.task.ID, d.ID, "revisions.json"), SHA256: digest.Of(data).Hex()}, data); err != nil {
				return Next{}, err
			}
		}
	}
	if _, err := r.e.Records.OpenDecision(r.mutation("decision", d.ID), d); err != nil {
		return Next{}, err
	}
	r.addDecision(d)
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
	if spec, err := digest.ParseHex(inputs[i].SHA256); err != nil || d.Subject == nil || string(spec) != d.Subject.Digest {
		return fmt.Errorf("%w: decision %s approved another document than %s", ErrSpecGate, d.ID, inputs[i].Name)
	}
	doc := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: inputs[i].Name, SHA256: inputs[i].SHA256}
	if _, err := r.e.Records.PutDocument(r.mutation("document", name), doc); err != nil {
		return err
	}
	r.addDocument(doc)
	return nil
}

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
	spec, err := digest.ParseHex(doc.SHA256)
	switch {
	case d == nil || !d.Answered || (d.Kind != store.DecisionApproval || d.Choice != string(workflow.VerdictApproved)) && (d.Kind != store.DecisionLoopExit || d.Choice != "accept"):
		return fmt.Errorf("%w: %s has no approved decision", ErrSpecGate, doc.Name)
	case d.Subject == nil || d.Subject.Digest != string(spec) || err != nil:
		return fmt.Errorf("%w: accepted spec %s is sha256:%s, decision %s approved %v", ErrSpecGate, doc.Name, doc.SHA256, d.ID, d.Subject)
	}
	return nil
}

// head reads a code head document, checking its recorded digest.
func (r *run) head(in render.Input) (string, error) {
	data, err := r.read(store.DocumentRef{Name: in.Name, Path: in.Name, SHA256: in.SHA256})
	if err != nil {
		return "", err
	}
	h := strings.TrimSpace(string(data))
	if !digest.ValidHead(h) {
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
	if digest.Of(data).Hex() != d.SHA256 {
		return nil, fmt.Errorf("document %s does not match its recorded digest", d.Path)
	}
	return data, nil
}

func (r *run) notifyDecision(d store.Decision, step string) error {
	return r.notify("decision."+d.ID, store.MessageDecision, fmt.Sprintf("Decision %s is open for step %s: %s", d.ID, step, d.Kind))
}

// notify sends the Director one message about the task, with an id derived from the task and what
// happened so resending it replays. It names no attempt, so it is accepted whatever the attempt's state.
func (r *run) notify(what, typ, text string) error {
	m, err := directorMessage(r.task.Repository, r.task.ID, what, typ, text)
	if err != nil {
		return err
	}
	_, err = r.e.Outbox.Send(store.Recipient{Role: store.RecipientDirector}, m)
	return err
}

// directorMessage is the Director message about task repo/task with an id derived from both and what, hashing
// ids longer than 100 bytes. It refuses a type not in store.MessageTypes, the registry the wake line counts by.
func directorMessage(repo, task, what, typ, text string) (messaging.Envelope, error) {
	if _, ok := store.MessageTypes[typ]; !ok {
		return messaging.Envelope{}, fmt.Errorf("interp: director message type %q is not in store.MessageTypes", typ)
	}
	id := fmt.Sprintf("%s.%s.%s", repo, task, what)
	if len(id) > 100 {
		id = "advance." + digest.Of([]byte(id)).Hex()
	}
	return messaging.Envelope{
		Schema: messaging.SchemaPrefix + strconv.Itoa(messaging.SchemaVersion), ID: id, Recipient: store.RecipientDirector,
		Repository: repo, Task: task, Type: typ, Text: text,
	}, nil
}

// mutation derives a write's mutation id from the task and the record, hashing ids longer than the store allows.
func (r *run) mutation(kind string, parts ...string) string {
	id := strings.Join(append([]string{kind, r.task.Repository, r.task.ID}, parts...), ":")
	if len(id) <= 128 {
		return id
	}
	return kind + ":" + string(digest.Of([]byte(id)))
}

func (r *run) key(step string, n int) artisan.AttemptKey {
	return artisan.AttemptKey{Repository: r.task.Repository, TaskID: r.task.ID, Step: step, Attempt: n}
}

func (r *run) identity(step string, n int) adapter.Identity {
	return adapter.Identity{TaskID: r.task.ID, AttemptID: r.key(step, n).AttemptID()}
}

func (r *run) attemptDir(step string, n int) string { return r.key(step, n).Dir(r.e.StateDir) }

func (r *run) resultFile(step string, n int) string {
	return filepath.Join(r.attemptDir(step, n), artisan.ResultFile)
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
	return atomicfile.Write(name, data)
}
