package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

// runDirector is a Director over testdata/run: a store holding task app/t-1 on the fake-straight workflow,
// its config and a short state directory, with every launcher it starts played by this test binary.
type runDirector struct {
	store, config, state string
	// forge, when set, is the App's forge.
	forge interp.Forge
	// workspaces is the App's workspaces: the double, with app's remote's default branch set.
	workspaces *gittest.Workspaces
	// panes, when set, is the App's Director pane.
	panes *typedPanes
}

func newRunDirector(t *testing.T) runDirector {
	t.Helper()
	return newRunDirectorWith(t, "config.toml")
}

// newRunDirectorWith is newRunDirector with the config file name in testdata/run.
func newRunDirectorWith(t *testing.T, config string) runDirector {
	t.Helper()
	cfg, err := filepath.Abs(filepath.Join("testdata", "run", config))
	if err != nil {
		t.Fatal(err)
	}
	return newRunDirectorAt(t, cfg)
}

// appRepo is repository app's path in the run configs, in this machine's form: a Director opens only a path
// absolute on its own machine (D11), so on Windows the configs are copied with a drive path.
var appRepo = func() string {
	if runtime.GOOS == "windows" {
		return `C:\srv\app`
	}
	return "/srv/app"
}()

// newRunDirectorAt is newRunDirector over the absolute config file cfg, with its baseline and workflows beside it.
func newRunDirectorAt(t *testing.T, cfg string) runDirector {
	t.Helper()
	if appRepo != "/srv/app" {
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS(filepath.Dir(cfg))); err != nil {
			t.Fatal(err)
		}
		cfg = filepath.Join(dir, filepath.Base(cfg))
		b, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfg, bytes.Replace(b, []byte(`path = "/srv/app"`), []byte("path = '"+appRepo+"'"), 1), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(roleEnv, "guildhall")
	state, err := os.MkdirTemp("", "gr")
	if err != nil {
		t.Fatal(err)
	}
	ws := gittest.NewWorkspaces()
	ws.SetDefault("git@github.com:example/app.git", strings.Repeat("d", 40))
	d := runDirector{store: emptyStore(t), config: cfg, state: state, workspaces: ws}
	t.Cleanup(func() {
		d.stopAll(t)
		_ = os.RemoveAll(state)
	})
	// The Director-local workflow has no validate step, so it is below the review floor and needs its approval.
	approveRepo(t, d.store, cfg, "app")
	newTask(t, d.store, cfg, "app", "Sort the task list", "t-1")
	return d
}

// run runs the CLI in this process with the test binary as the guildhall executable.
func (d runDirector) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Executable = os.Executable
	app.InstallSkills = func(string) error { return nil }
	app.InstallBaseline = baseline.Install
	app.Forge = d.forge
	if d.workspaces != nil {
		app.Workspaces = d.workspaces
	}
	if d.panes != nil {
		app.DirectorPane = d.panes
	}
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

// stopAll fails the test for every leftover, so no process outlives the test.
func (d runDirector) stopAll(t *testing.T) {
	t.Helper()
	for _, l := range d.leftovers(t, "") {
		t.Error(l)
	}
}

// leftovers proves every attempt's launcher stopped with artisan stop and its
// worker tree empty, and describes each process or attempt it could not prove
// so. The processes come from artisan harness inventory, with the fake's
// inventory directory inv when it is not empty, taken before the stop, since
// stop may settle the attempt and its record then names no worker; each must
// probe gone within a few seconds afterwards, well inside the orphaning fake's
// lifetime, so a process still exiting is not a leftover and an orphan is.
func (d runDirector) leftovers(t *testing.T, inv string) []string {
	t.Helper()
	s, err := store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		return []string{err.Error()}
	}
	attempts, err := s.Attempts("app", "t-1")
	_ = s.Close()
	var out []string
	if err != nil {
		out = append(out, err.Error())
	}
	for _, a := range attempts {
		at := []string{"--store", d.store, "--state", d.state, "--repo", "app", "--task", "t-1", "--step", a.Step, "--attempt", strconv.Itoa(a.Number)}
		var procs []harnessProcess
		args := append([]string{"artisan", "harness", "inventory", "--lab"}, at...)
		if inv != "" {
			args = append(args, "--inventory", inv)
		}
		code, stdout, stderr := d.run(t, args...)
		if err := json.Unmarshal([]byte(stdout), &procs); code != 0 || err != nil {
			out = append(out, fmt.Sprintf("artisan harness inventory %s-%d: exit %d, %v, %s %s", a.Step, a.Number, code, err, stdout, stderr))
		}
		// A launcher that has just recorded its outcome may still be exiting: stop proves it only once gone.
		// Once it is gone stop is not retried, so a surviving worker tree is reported rather than waited out.
		exiting := func() bool {
			i := slices.IndexFunc(procs, func(p harnessProcess) bool { return p.Role == "launcher" })
			if i < 0 {
				return false
			}
			lv, _ := artisan.Probe(artisan.Identity{PID: procs[i].PID, Token: procs[i].Token})
			return lv != artisan.Gone
		}
		for end := time.Now().Add(time.Minute); ; time.Sleep(50 * time.Millisecond) {
			code, stdout, stderr := d.run(t, append([]string{"artisan", "stop"}, at...)...)
			if code == 0 {
				break
			}
			// A launcher that released its attempt at setup, before a worker, recorded no containment where
			// its unit is not named (a process group), so stop cannot prove it stopped; once that launcher is
			// gone nothing of the attempt is left.
			var rep struct {
				Endpoint       string
				Classification struct{ State, Launcher string }
			}
			if json.Unmarshal([]byte(stdout), &rep) == nil && rep.Endpoint == "not asked: ownership released" &&
				rep.Classification.State == "exited" && rep.Classification.Launcher == "gone" {
				break
			}
			if !exiting() || time.Now().After(end) {
				out = append(out, fmt.Sprintf("artisan stop %s-%d: exit %d, %s %s", a.Step, a.Number, code, stdout, stderr))
				break
			}
		}
		// The recorded launcher outlives its released attempt while it runs the task's advance (--advance), a
		// child it waits for, so the test binary that plays both is in use until that launcher is gone.
		if s, err := store.Open(d.store, store.Options{ReadOnly: true}); err == nil {
			o, err := s.Ownership("app", "t-1", a.Step, a.Number)
			_ = s.Close()
			id := artisan.Identity(o.LauncherIdentity)
			for end := time.Now().Add(time.Minute); err == nil && id.PID != 0; time.Sleep(50 * time.Millisecond) {
				lv, perr := artisan.Probe(id)
				if perr == nil && lv == artisan.Gone {
					break
				}
				if time.Now().After(end) {
					out = append(out, fmt.Sprintf("%s-%d: recorded launcher %d is %v (%v)", a.Step, a.Number, id.PID, lv, perr))
					break
				}
			}
		}
		settle := time.Now().Add(10 * time.Second)
		for _, p := range procs {
			id := artisan.Identity{PID: p.PID, Token: p.Token}
			lv, err := artisan.Probe(id)
			for err == nil && lv != artisan.Gone && time.Now().Before(settle) {
				time.Sleep(50 * time.Millisecond)
				lv, err = artisan.Probe(id)
			}
			if err != nil || lv != artisan.Gone {
				out = append(out, fmt.Sprintf("%s-%d: %s %d is %v (%v)", a.Step, a.Number, p.Role, p.PID, lv, err))
			}
		}
	}
	return out
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

// newRunCopy is newRunDirector over a Director copy holding testdata/run's config, baseline and workflows, its
// director.store and a short state directory, returned with the copy.
func newRunCopy(t *testing.T) (runDirector, string) {
	t.Helper()
	t.Setenv(roleEnv, "guildhall")
	dir, err := os.MkdirTemp("", "gc")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "run"))); err != nil {
		t.Fatal(err)
	}
	d := runDirector{store: filepath.Join(dir, "director.store"), config: filepath.Join(dir, "config.toml"), state: filepath.Join(dir, "state")}
	// A launcher's advance runs as guildhall, with real git workspaces, so app is a real repository and remote.
	repository, remote, _ := gittest.Repository(t)
	b, err := os.ReadFile(d.config)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte(`path = "/srv/app"`), []byte("path = '"+repository+"'"), 1)
	b = bytes.Replace(b, []byte(`remote = "git@github.com:example/app.git"`), []byte("remote = '"+remote+"'"), 1)
	if err := os.WriteFile(d.config, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(d.store, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.stopAll(t)
		_ = os.RemoveAll(dir)
	})
	approveRepo(t, d.store, d.config, "app")
	newTask(t, d.store, d.config, "app", "Sort the task list", "t-1")
	return d, dir
}

// waitDecision waits for the store to record decision id of task app/t-1.
func (d runDirector) waitDecision(t *testing.T, id string) {
	t.Helper()
	for end := time.Now().Add(time.Minute); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		s, err := store.Open(d.store, store.Options{ReadOnly: true})
		if err != nil {
			continue
		}
		ds, err := s.Decisions("app", "t-1")
		_ = s.Close()
		if err == nil && slices.ContainsFunc(ds, func(d store.Decision) bool { return d.ID == id }) {
			return
		}
	}
	log, _ := os.ReadFile(filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}.Dir(d.state), launch.LogFile))
	t.Fatalf("decision %s was not opened within a minute; plan-1 launcher log:\n%s", id, log)
}

// TestTaskStartAndStepDoneByTheWatcher is TestTaskStartAndStepDone from a copy (D3): task start launches the plan
// attempt with --advance, and the attempt's launcher, once it records the result, runs step done itself, so the
// task reaches the accept-1 decision without step done typed by hand.
func TestTaskStartAndStepDoneByTheWatcher(t *testing.T) {
	d, dir := newRunCopy(t)
	code, out, stderr := d.run(t, "task", "start", "--copy", dir, "--repo", "app", "t-1")
	if code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitDecision(t, "accept-1")
	if code, out, stderr := d.run(t, "task", "start", "--copy", dir, "--repo", "app", "t-1"); code != 0 || out != `{"wait":"decision","step":"accept","decision":"accept-1"}`+"\n" {
		t.Errorf("task start after the launcher's advance: exit %d, %q, stderr %s", code, out, stderr)
	}
	if got := d.attempts(t); len(got) != 1 || got[0].Outcome != store.AttemptSucceeded {
		t.Errorf("attempts %+v, want plan-1 succeeded", got)
	} // Each advance leaves what the task waits for beside its task lock, for director status.
	if b, err := os.ReadFile(filepath.Join(d.state, "app", "t-1", "next.json")); err != nil || string(b) != `{"wait":"decision","step":"accept","decision":"accept-1"}`+"\n" {
		t.Errorf("next.json = %q, %v", b, err)
	}
}

// TestTheWatcherReportsASetupFailure: from a copy, a plan attempt whose launcher cannot listen on its endpoint
// fails at setup; task start returns that error at once rather than waiting out the launcher, so the launcher's
// task start takes the task lock and sends the Director attempt-failed.
func TestTheWatcherReportsASetupFailure(t *testing.T) {
	d, dir := newRunCopy(t)
	k := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}
	path, err := artisan.EndpointPath(d.state, k)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	begun := time.Now()
	if code, out, stderr := d.run(t, "task", "start", "--copy", dir, "--repo", "app", "t-1"); code == 0 || !strings.Contains(stderr, "plan") {
		t.Fatalf("task start over a setup failure: exit %d, %q, stderr %s; want the launch error", code, out, stderr)
	}
	if took := time.Since(begun); took > 10*time.Second {
		t.Errorf("task start took %v to report the setup failure; want no wait for the launcher's advance", took)
	}
	var msg store.Message
	for end := time.Now().Add(time.Minute); ; time.Sleep(50 * time.Millisecond) {
		s, err := store.Open(d.store, store.Options{ReadOnly: true})
		if err == nil {
			msg, err = s.Message("app.t-1.attempt-failed.plan-1")
			_ = s.Close()
		}
		if err == nil {
			break
		}
		if time.Now().After(end) {
			log, _ := os.ReadFile(filepath.Join(k.Dir(d.state), launch.LogFile))
			t.Fatalf("no attempt-failed message within a minute: %v; plan-1 launcher log:\n%s", err, log)
		}
	}
	if msg.Type != "attempt-failed" {
		t.Errorf("message %+v, want attempt-failed", msg)
	}
	if got := d.attempts(t); len(got) != 1 || got[0].Outcome != store.AttemptFailed {
		t.Errorf("attempts %+v, want plan-1 failed", got)
	}
}

// TestArtisanStartAdvanceNeedsACopy: --advance advances over a copy, so without one it is a usage error.
func TestArtisanStartAdvanceNeedsACopy(t *testing.T) {
	d := runDirector{store: "s", config: "c", state: "st"}
	args := []string{"artisan", "start", "--store", "s", "--state", "st", "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1",
		"--adapter", "fake", "--prompt", "p", "--workdir", "w", "--advance"}
	if code, out, stderr := d.run(t, args...); code != 2 || out != "" || !strings.Contains(stderr, "--advance") {
		t.Errorf("artisan start --advance without a copy: exit %d, %q, stderr %s; want 2 naming --advance", code, out, stderr)
	}
}

// TestStepRetryRefusesASucceededAttempt: step retry reaches the engine, which refuses an attempt that did not
// fail or stop and records nothing; help lists the command.
func TestStepRetryRefusesASucceededAttempt(t *testing.T) {
	d := newRunDirector(t)
	if code, _, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 {
		t.Fatalf("task start: exit %d, stderr %s", code, stderr)
	}
	d.waitResult(t, "plan", 1)
	if code, _, stderr := d.run(t, append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--result", d.result("plan", 1))...)...); code != 0 {
		t.Fatalf("step done: exit %d, stderr %s", code, stderr)
	}
	retry := append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--reason", "again")...)
	if code, out, stderr := d.run(t, retry...); code != 1 || out != "" || !strings.Contains(stderr, "only a failed or stopped attempt is retried") {
		t.Errorf("retry of a succeeded attempt: exit %d, %q, stderr %s; want 1", code, out, stderr)
	}
	if got := d.attempts(t); len(got) != 1 {
		t.Errorf("attempts after a refused retry = %+v", got)
	}
	if _, out, _ := d.run(t, "help"); !strings.Contains(out, "step retry") {
		t.Errorf("help does not list step retry:\n%s", out)
	}
}

// TestTaskStartLaunchesClaude: a profile naming the claude adapter makes task start launch the attempt
// through launch.Process as an owned claude attempt, over the stub Claude Code, whose draft the
// wrapper publishes as the attempt's result.
func TestTaskStartLaunchesClaude(t *testing.T) {
	claudetest.Install(t)
	d := newRunDirectorWith(t, "claude.toml")
	code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...)
	if code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "plan", 1)
	r, err := adapter.ReadResult(d.result("plan", 1), adapter.Identity{TaskID: "t-1", AttemptID: "plan-1"})
	if err != nil || r.Summary != claudetest.Summary {
		t.Errorf("plan-1 result %+v, %v; want the stub Claude's", r, err)
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
		"start without store":        {"task", "start", "--config", "c", "--state", "st", "--repo", "app", "t-1"},
		"start without task":         append([]string{"task", "start"}, d.flags()...),
		"start with two tasks":       append([]string{"task", "start"}, d.flags("t-1", "t-2")...),
		"start with a bad id":        append([]string{"task", "start"}, d.flags("T 1")...),
		"step without done":          {"step"},
		"step other command":         {"step", "undo"},
		"done without result":        append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1")...),
		"done with a bad number":     append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "0", "--result", "r")...),
		"done with an argument":      append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--result", "r", "extra")...),
		"retry without store":        {"step", "retry", "--config", "c", "--state", "st", "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1"},
		"retry without attempt":      append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", "plan")...),
		"retry without step":         append([]string{"step", "retry"}, d.flags("--task", "t-1", "--attempt", "1")...),
		"retry with a bad id":        append([]string{"step", "retry"}, d.flags("--task", "T 1", "--step", "plan", "--attempt", "1")...),
		"retry with a bad number":    append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "x")...),
		"retry with an argument":     append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "extra")...),
		"retry with an unknown flag": append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", "plan", "--attempt", "1", "--force")...),
	} {
		if code, out, _ := d.run(t, args...); code != 2 || out != "" {
			t.Errorf("%s: exit %d, %q; want 2", name, code, out)
		}
	}
}

// TestTaskDelivers: on a workflow with publish, the sign-off's answer pushes the build's head to guildhall/t-1
// through the App's forge and opens the merge decision; answering merge squash-merges it and the task is done.
func TestTaskDelivers(t *testing.T) {
	d := newRunDirectorWith(t, "publish.toml")
	const remote, head = "git@github.com:example/app.git", "0123456789abcdef0123456789abcdef01234567"
	f := forgetest.NewFake()
	f.SetBranch(remote, "main", strings.Repeat("a", 40))
	f.AddCommit(appRepo, head)
	d.forge = f
	// The fake build reports the remote's default branch as its head, so its worktree holds nothing undelivered.
	d.workspaces.SetDefault(remote, head)
	step := func(args ...string) string {
		t.Helper()
		code, out, stderr := d.run(t, args...)
		if code != 0 {
			t.Fatalf("guildhall %q: exit %d, %s", args, code, stderr)
		}
		return strings.TrimSpace(out)
	}
	done := func(name string) string {
		d.waitResult(t, name, 1)
		return step(append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", name, "--attempt", "1", "--result", d.result(name, 1))...)...)
	}
	answer := func(decision, words string) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), "answer")
		if err := os.WriteFile(file, []byte(words), 0o600); err != nil {
			t.Fatal(err)
		}
		step("decide", "answer", "--store", d.store, "--repo", "app", "--task", "t-1", "--decision", decision, "--answer-file", file)
		return step(append([]string{"task", "start"}, d.flags("--decision", decision, "t-1")...)...)
	}
	step(append([]string{"task", "start"}, d.flags("t-1")...)...)
	done("plan")
	answer("accept-1", "approved\n")
	done("build")
	if got := answer("sign-off-1", "approved\n"); got != `{"wait":"decision","step":"deliver","decision":"deliver-1"}` {
		t.Fatalf("after sign-off: %s", got)
	}
	if prs := f.PullRequests(remote); len(prs) != 1 || prs[0].Branch != "guildhall/t-1" || prs[0].Head != head {
		t.Fatalf("pull requests = %+v", prs)
	}
	if got := answer("deliver-1", "merge\n"); got != `{"wait":"done","step":"deliver"}` {
		t.Fatalf("after the merge answer: %s", got)
	}
	if prs := f.PullRequests(remote); prs[0].State != forge.Merged {
		t.Errorf("pull request = %+v, want merged", prs[0])
	}
	// Each attempt ran in the task's workspace, a worktree of the repository on the branch publish pushes.
	ws := git.Workspace{Repository: appRepo, Remote: remote, Branch: "guildhall/t-1", Dir: filepath.Join(d.state, "app", "t-1", "work")}
	if calls := d.workspaces.Calls(); len(calls) != 2 || calls[0] != ws || calls[1] != ws {
		t.Errorf("workspaces = %+v, want %+v for the plan and build attempts", calls, ws)
	}
	// The merge retired it.
	if _, err := os.Stat(ws.Dir); !os.IsNotExist(err) {
		t.Errorf("workspace after the merge: %v, want it removed", err)
	}
}

// sweep runs artisan resume over an attempt whose launcher is gone and fails the test unless it succeeds.
func (d runDirector) sweep(t *testing.T, step string, n int) {
	t.Helper()
	if code, out, stderr := d.run(t, "artisan", "resume", "--store", d.store, "--state", d.state, "--repo", "app", "--task", "t-1", "--step", step, "--attempt", strconv.Itoa(n)); code != 0 {
		t.Errorf("artisan resume %s-%d: exit %d, %s %s", step, n, code, out, stderr)
	}
}
