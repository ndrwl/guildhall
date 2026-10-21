package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
)

// runDirector is a Director over testdata/run: a store holding task app/t-1 on the fake-straight workflow,
// its config and a short state directory, with every launcher it starts played by this test binary.
type runDirector struct {
	store, config, state string
}

func newRunDirector(t *testing.T) runDirector {
	t.Helper()
	t.Setenv(roleEnv, "guildhall")
	cfg, err := filepath.Abs(filepath.Join("testdata", "run", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.MkdirTemp("", "gr")
	if err != nil {
		t.Fatal(err)
	}
	d := runDirector{store: emptyStore(t), config: cfg, state: state}
	t.Cleanup(func() {
		d.stopAll(t)
		_ = os.RemoveAll(state)
	})
	newTask(t, d.store, cfg, "app", "Sort the task list", "t-1")
	return d
}

// run runs the CLI in this process with the test binary as the guildhall executable.
func (d runDirector) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Executable = os.Executable
	app.InstallSkills = func(string) error { return nil }
	code := app.Run(args)
	return code, stdout.String(), stderr.String()
}

func (d runDirector) flags(extra ...string) []string {
	return append([]string{"--store", d.store, "--config", d.config, "--state", d.state, "--repo", "app"}, extra...)
}

func (d runDirector) result(step string, n int) string {
	return filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: step, Attempt: n}.Dir(d.state), artisan.ResultFile)
}

// waitResult waits for the attempt's launcher to publish a valid result.
func (d runDirector) waitResult(t *testing.T, step string, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := adapter.WaitResult(ctx, d.result(step, n), adapter.Identity{TaskID: "t-1", AttemptID: artisan.AttemptKey{Step: step, Attempt: n}.AttemptID()}, adapter.WaitOptions{Poll: 20 * time.Millisecond}); err != nil {
		t.Fatalf("result of %s-%d: %v", step, n, err)
	}
}

// stopAll proves every attempt's launcher stopped, so no process outlives the test.
func (d runDirector) stopAll(t *testing.T) {
	t.Helper()
	s, err := store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Error(err)
		return
	}
	attempts, err := s.Attempts("app", "t-1")
	_ = s.Close()
	if err != nil {
		t.Error(err)
	}
	for _, a := range attempts {
		// A launcher that has just recorded its outcome may still be exiting: stop proves it only once gone.
		for end := time.Now().Add(time.Minute); ; time.Sleep(50 * time.Millisecond) {
			code, out, stderr := d.run(t, "artisan", "stop", "--store", d.store, "--state", d.state, "--repository", "app", "--task", "t-1", "--step", a.Step, "--attempt", strconv.Itoa(a.Number))
			if code == 0 {
				break
			}
			if time.Now().After(end) {
				t.Errorf("artisan stop %s-%d: exit %d, %s %s", a.Step, a.Number, code, out, stderr)
				break
			}
		}
	}
}

func (d runDirector) attempts(t *testing.T) []store.StageAttempt {
	t.Helper()
	s, err := store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	a, err := s.Attempts("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTaskStartAndStepDone(t *testing.T) {
	d := newRunDirector(t)
	code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...)
	if code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	// A repeated start replays and records no second attempt; once the launcher has recorded the plan
	// succeeded it may already take its outputs and open the approval.
	if code, again, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 ||
		again != out && again != `{"wait":"decision","step":"accept","decision":"accept-1"}`+"\n" {
		t.Errorf("repeated task start: exit %d, %q, stderr %s", code, again, stderr)
	}
	if got := d.attempts(t); len(got) != 1 {
		t.Errorf("attempts after two starts = %+v", got)
	}
	d.waitResult(t, "plan", 1)
	done := append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--result", d.result("plan", 1))...)
	code, out, stderr = d.run(t, done...)
	if code != 0 || out != `{"wait":"decision","step":"accept","decision":"accept-1"}`+"\n" {
		t.Fatalf("step done: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, again, stderr := d.run(t, done...); code != 0 || again != out {
		t.Errorf("repeated step done: exit %d, %q, stderr %s", code, again, stderr)
	}
	// The answer alone advances nothing; start with the decision does.
	if code, _, stderr := d.run(t, append([]string{"task", "start"}, d.flags("--decision", "accept-1", "t-1")...)...); code != 1 || !strings.Contains(stderr, "not answered") {
		t.Errorf("start on an unanswered decision: exit %d, stderr %s", code, stderr)
	}
}

func TestStepDoneRefusesAResultItDoesNotTake(t *testing.T) {
	d := newRunDirector(t)
	if code, _, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 {
		t.Fatalf("task start: exit %d, stderr %s", code, stderr)
	}
	d.waitResult(t, "plan", 1)
	other := filepath.Join(t.TempDir(), artisan.ResultFile)
	if err := adapter.WriteResult(other, adapter.Result{Identity: adapter.Identity{TaskID: "t-1", AttemptID: "plan-1"}, Status: adapter.StatusSucceeded, Summary: "elsewhere", Outputs: []adapter.Output{}}); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"another path":    d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--result", other),
		"no result yet":   d.flags("--task", "t-1", "--step", "plan", "--attempt", "2", "--result", d.result("plan", 2)),
		"another attempt": d.flags("--task", "t-1", "--step", "build", "--attempt", "1", "--result", d.result("plan", 1)),
	} {
		if code, out, stderr := d.run(t, append([]string{"step", "done"}, args...)...); code != 1 || out != "" {
			t.Errorf("%s: exit %d, %q, stderr %s; want 1 and nothing printed", name, code, out, stderr)
		}
	}
	got := d.attempts(t)
	s, err := store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := s.Documents("app", "t-1")
	_ = s.Close()
	if err != nil || len(docs) != 1 || len(got) != 1 {
		t.Errorf("after refusals: attempts %+v, documents %+v, %v; want one attempt and only its binding", got, docs, err)
	}
}

func TestTaskStartAndStepDoneUsage(t *testing.T) {
	d := runDirector{store: "s", config: "c", state: "st"}
	for name, args := range map[string][]string{
		"start without store":    {"task", "start", "--config", "c", "--state", "st", "--repo", "app", "t-1"},
		"start without task":     append([]string{"task", "start"}, d.flags()...),
		"start with two tasks":   append([]string{"task", "start"}, d.flags("t-1", "t-2")...),
		"start with a bad id":    append([]string{"task", "start"}, d.flags("T 1")...),
		"step without done":      {"step"},
		"step other command":     {"step", "undo"},
		"done without result":    append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1")...),
		"done with a bad number": append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "0", "--result", "r")...),
		"done with an argument":  append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--result", "r", "extra")...),
	} {
		if code, out, _ := d.run(t, args...); code != 2 || out != "" {
			t.Errorf("%s: exit %d, %q; want 2", name, code, out)
		}
	}
}
