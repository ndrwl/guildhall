package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// stageOutput is one --output of step finish: a file for an output port.
type stageOutput struct{ port, file string }

// stageOutputs is step finish's repeatable --output <port>=<file>, each port at most once.
type stageOutputs []stageOutput

func (o *stageOutputs) String() string { return "" }

func (o *stageOutputs) Set(v string) error {
	port, file, ok := strings.Cut(v, "=")
	switch {
	case !ok || file == "":
		return fmt.Errorf("--output %q is not <port>=<file>", v)
	case !workflow.ValidID(port):
		return fmt.Errorf("--output port %q is not a port name", port)
	case slices.ContainsFunc(*o, func(s stageOutput) bool { return s.port == port }):
		return fmt.Errorf("--output names port %s twice", port)
	}
	*o = append(*o, stageOutput{port, file})
	return nil
}

// stepFinish runs `guildhall step finish`, the Director's verb for a Director stage: it copies each output into the
// task's working directory, writes the attempt's result and feeds attempt-result (docs/running.md#step-finish).
func (a *App) stepFinish(args []string) int {
	fl := flag.NewFlagSet("step finish", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	task := fl.String("task", "", "task id")
	step := fl.String("step", "", "step id")
	attempt := fl.String("attempt", "", "attempt number")
	summary := fl.String("summary", "", "what the stage agreed or why it was abandoned")
	failed := fl.Bool("failed", false, "abandon the stage")
	var outputs stageOutputs
	fl.Var(&outputs, "output", "<port>=<file>, once per output port")
	if err := fl.Parse(args); err != nil {
		return a.usageError("step finish: " + err.Error())
	}
	n, err := strconv.Atoi(*attempt)
	switch ferr := f.resolve(a.Getenv); {
	case ferr != nil:
		return a.usageError("step finish: " + ferr.Error())
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("step finish: unexpected argument %q", fl.Arg(0)))
	case *task == "" || *step == "" || *attempt == "":
		return a.usageError("step finish: --task, --step and --attempt are required")
	case !workflow.ValidID(*task) || !workflow.ValidID(*step):
		return a.usageError("step finish: --task and --step must be ids")
	case err != nil || n < 1:
		return a.usageError(fmt.Sprintf("step finish: --attempt %q must be a positive attempt number", *attempt))
	case strings.TrimSpace(*summary) == "":
		return a.usageError("step finish: --summary is required")
	case *failed && len(outputs) > 0:
		return a.usageError("step finish: --failed takes no --output")
	case !*failed && len(outputs) == 0:
		return a.usageError("step finish: --output <port>=<file> is required unless --failed")
	}
	k := artisan.AttemptKey{Repository: f.repo, TaskID: *task, Step: *step, Attempt: n}
	if err := finishStage(f, k, *summary, outputs, *failed); err != nil {
		return a.fail(fmt.Errorf("step finish: %w", err))
	}
	return a.advanced(a.advance(f, interp.Event{Kind: interp.AttemptResult, Repository: f.repo, Task: *task, Step: *step, Attempt: n}))
}

// finishStage checks that attempt k is a Director stage's current, unfinished attempt the engine handed to the
// Director, then, under the task's lock, copies each output into the task's working directory as <port><ext> and
// publishes the attempt's result once; a refusal writes nothing.
func finishStage(f advanceFlags, k artisan.AttemptKey, summary string, outputs stageOutputs, failed bool) error {
	cfg, err := config.Load(f.config)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	r, err := cfg.ListedRepository(f.repo)
	if err != nil {
		return err
	}
	def, err := effectiveWorkflow(filepath.Join(filepath.Dir(f.config), "workflows"), r)
	if err != nil {
		return fmt.Errorf("repository %s: %w", f.repo, err)
	}
	i := slices.IndexFunc(def.Steps, func(s workflow.Step) bool { return s.ID == k.Step })
	if i < 0 {
		return fmt.Errorf("workflow %s has no step %s", def.Name, k.Step)
	}
	if def.Steps[i].Actor() != workflow.DirectorActor {
		return fmt.Errorf("step %s is not a Director stage: it is run by the %s", k.Step, def.Steps[i].Actor())
	}
	kind, _ := workflow.LookupKind(def.Steps[i].Type, def.Steps[i].Version)
	for _, o := range outputs {
		if !slices.ContainsFunc(kind.Outputs, func(p workflow.Port) bool { return p.Name == o.port }) {
			return fmt.Errorf("step %s has no output port %s", k.Step, o.port)
		}
	}
	// Outputs are listed in the kind's port order, so the first is the step's first output port.
	slices.SortStableFunc(outputs, func(a, b stageOutput) int {
		return slices.IndexFunc(kind.Outputs, func(p workflow.Port) bool { return p.Name == a.port }) -
			slices.IndexFunc(kind.Outputs, func(p workflow.Port) bool { return p.Name == b.port })
	})
	contents := make([][]byte, len(outputs))
	for i, o := range outputs {
		if contents[i], err = os.ReadFile(o.file); err != nil {
			return fmt.Errorf("output %s: %w", o.port, err)
		}
	}

	state, err := filepath.Abs(f.state)
	if err != nil {
		return err
	}
	taskDir := filepath.Join(state, k.Repository, k.TaskID)
	if _, err := os.Stat(taskDir); err != nil {
		return fmt.Errorf("task %s/%s has no state: %w", k.Repository, k.TaskID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeWait)
	release, err := artisan.LockAttempt(ctx, taskDir)
	cancel()
	if err != nil {
		return fmt.Errorf("lock task %s/%s: %w", k.Repository, k.TaskID, err)
	}
	defer release()
	attempts, err := withStore(f.store, true, func(s *store.Store) ([]store.StageAttempt, error) { return s.Attempts(k.Repository, k.TaskID) })
	if err != nil {
		return err
	}
	var latest *store.StageAttempt
	for i := range attempts {
		if attempts[i].Step == k.Step {
			latest = &attempts[i]
		}
	}
	switch {
	case latest == nil || latest.Number < k.Attempt:
		return fmt.Errorf("attempt %s of %s/%s is not recorded", k.AttemptID(), k.Repository, k.TaskID)
	case latest.Number != k.Attempt:
		return fmt.Errorf("attempt %s is not current: attempt %d of %s follows it", k.AttemptID(), latest.Number, k.Step)
	case latest.Outcome != "":
		return fmt.Errorf("attempt %s is already finished: %s", k.AttemptID(), latest.Outcome)
	}
	dir := k.Dir(state)
	result := filepath.Join(dir, artisan.ResultFile)
	if _, err := os.Stat(result); err == nil {
		return fmt.Errorf("attempt %s is already finished: its result %s exists; take it with guildhall step done", k.AttemptID(), result)
	}
	switch handed, err := interp.HandedToDirector(dir); {
	case err != nil:
		return err
	case !handed:
		return fmt.Errorf("attempt %s was not handed to the Director; guildhall task start hands it", k.AttemptID())
	}

	work := filepath.Join(taskDir, "work")
	if _, err := os.Stat(work); err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	res := adapter.Result{Identity: adapter.Identity{TaskID: k.TaskID, AttemptID: k.AttemptID()}, Status: adapter.StatusSucceeded, Summary: summary}
	if failed {
		res.Status = adapter.StatusFailed
	}
	for i, o := range outputs {
		name := o.port + filepath.Ext(o.file)
		if err := atomicfile.Write(filepath.Join(work, name), contents[i]); err != nil {
			return fmt.Errorf("output %s: %w", o.port, err)
		}
		res.Outputs = append(res.Outputs, adapter.Output{Path: name})
	}
	if err := adapter.WriteResult(result, res); errors.Is(err, adapter.ErrResultExists) {
		return fmt.Errorf("attempt %s is already finished: %w", k.AttemptID(), err)
	} else if err != nil {
		return err
	}
	return nil
}
