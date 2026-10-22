package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/launch/launchtest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// binDirector runs the built binary over one store, config and state directory, recording each attempt it
// takes so that cleanup proves every launcher stopped.
type binDirector struct {
	t                      *testing.T
	bin, store, cfg, state string
	flags                  []string
	attempts               []artisan.AttemptKey
	// repository is repository app's path, a real git repository whose remote's default branch is at base.
	repository, base string
}

// newBinDirector builds the binary and creates an empty store and a short state directory for cfg, a copy of a
// run config that it points at a new real repository and remote (gittest.Repository) in place.
func newBinDirector(t *testing.T, cfg string) *binDirector {
	t.Helper()
	repository, remote, base := gittest.Repository(t)
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte(`path = "/srv/app"`), []byte("path = '"+repository+"'"), 1)
	b = bytes.Replace(b, []byte(`remote = "git@github.com:example/app.git"`), []byte("remote = '"+remote+"'"), 1)
	if err := os.WriteFile(cfg, b, 0o600); err != nil {
		t.Fatal(err)
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "guildhall")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command(gobin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	state, err := os.MkdirTemp("", "ge")
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "director.store")
	s, err := store.Open(storePath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	d := &binDirector{t: t, bin: bin, store: storePath, cfg: cfg, state: state, repository: repository, base: base,
		flags: []string{"--store", storePath, "--config", cfg, "--state", state, "--repo", "app"}}
	t.Cleanup(func() {
		d.stopAll()
		_ = os.RemoveAll(state)
	})
	return d
}

func (d *binDirector) try(stdin string, args ...string) (string, string, error) {
	cmd := exec.Command(d.bin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (d *binDirector) run(stdin string, args ...string) string {
	d.t.Helper()
	out, stderr, err := d.try(stdin, args...)
	if err != nil {
		d.t.Fatalf("guildhall %s: %v\nstdout %s\nstderr %s", strings.Join(args, " "), err, out, stderr)
	}
	return out
}

// advance runs task start, step done or step retry with the director flags before args.
func (d *binDirector) advance(cmd []string, args ...string) string {
	d.t.Helper()
	return d.run("", append(append(cmd, d.flags...), args...)...)
}

func (d *binDirector) want(got, want string) {
	d.t.Helper()
	if strings.TrimSpace(got) != want {
		d.t.Fatalf("got %s, want %s", strings.TrimSpace(got), want)
	}
}

// launched records attempt n of step so that cleanup stops its launcher.
func (d *binDirector) launched(step string, n int) {
	d.attempts = append(d.attempts, artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: step, Attempt: n})
}

// stopAll proves every attempt's launcher stopped by launchtest's rule, so no owned process outlives the test.
func (d *binDirector) stopAll() {
	d.t.Helper()
	launchtest.StopAll(d.t, d.attempts, func(k artisan.AttemptKey) (launch.StopReport, error) {
		return launchtest.Report(d.try("", "artisan", "stop", "--store", d.store, "--state", d.state, "--repo", k.Repository, "--task", k.TaskID,
			"--step", k.Step, "--attempt", strconv.Itoa(k.Attempt)))
	})
}

// done waits for the attempt's result and takes it.
func (d *binDirector) done(step string, n int) string {
	d.t.Helper()
	d.launched(step, n)
	k := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: step, Attempt: n}
	result := filepath.Join(k.Dir(d.state), artisan.ResultFile)
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(2*time.Minute))
	defer cancel()
	if _, err := adapter.WaitResult(ctx, result, adapter.Identity{TaskID: "t-1", AttemptID: k.AttemptID()}, adapter.WaitOptions{Poll: 50 * time.Millisecond}); err != nil {
		log, _ := os.ReadFile(filepath.Join(k.Dir(d.state), "launcher.log"))
		d.t.Fatalf("result of %s: %v\nlauncher log:\n%s", k, err, log)
	}
	return d.advance([]string{"step", "done"}, "--task", "t-1", "--step", step, "--attempt", strconv.Itoa(n), "--result", result)
}

// newTask approves the repository's workflow, which has no validate step and so is below the review floor,
// and creates task t-1.
func (d *binDirector) newTask() {
	d.t.Helper()
	d.run("Approved for the acceptance run.\n", "workflow", "approve", "--store", d.store, "--config", d.cfg, "--repo", "app", "--words-file", "-")
	d.run("", "task", "new", "--store", d.store, "--config", d.cfg, "--repo", "app", "--title", "Sort the task list", "t-1")
}

func (d *binDirector) answer(decision string) string {
	d.t.Helper()
	d.run("approved\nLooks right.\n", "decide", "answer", "--store", d.store, "--repo", "app", "--task", "t-1", "--decision", decision, "--answer-file", "-")
	return d.advance([]string{"task", "start"}, "--decision", decision, "t-1")
}

// TestTaskRunsEndToEnd is the S7 acceptance: the built binary runs task app/t-1 on the fake-straight workflow
// (internal/cli/testdata/run) from task new to its terminal step, the merge decision, through the real commands:
// task start launches each Artisan stage as its own owned launcher process, the fake Artisan writes its result,
// step done takes it, decide answer and task start --decision cross each Guildmaster step. Afterwards every
// attempt's launcher is proven stopped and the timeline records the run in order.
func TestTaskRunsEndToEnd(t *testing.T) {
	d := newBinDirector(t, filepath.Join(runCopy(t), "config.toml"))
	d.newTask()
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"attempt","step":"plan","attempt":1}`)
	d.want(d.done("plan", 1), `{"wait":"decision","step":"accept","decision":"accept-1"}`)
	d.want(d.answer("accept-1"), `{"wait":"attempt","step":"build","attempt":1}`)
	d.want(d.done("build", 1), `{"wait":"decision","step":"sign-off","decision":"sign-off-1"}`)
	d.want(d.answer("sign-off-1"), `{"wait":"decision","step":"deliver","decision":"deliver-1"}`)

	d.stopAll()

	// Both attempts ran in one worktree of the repository, on the task branch, started at the remote's default
	// branch; the fake Artisan's outputs are untracked files in it.
	work := filepath.Join(d.state, "app", "t-1", "work")
	if got := d.git(work, "rev-parse", "HEAD"); got != d.base {
		t.Errorf("worktree at %s, want the remote's default branch at %s", got, d.base)
	}
	if got := d.git(work, "symbolic-ref", "--short", "HEAD"); got != "guildhall/t-1" {
		t.Errorf("worktree on branch %s, want guildhall/t-1", got)
	}
	if list := d.git(d.repository, "worktree", "list", "--porcelain"); !strings.Contains(filepath.ToSlash(list), filepath.ToSlash(filepath.Base(work))) {
		t.Errorf("the repository does not list the worktree %s:\n%s", work, list)
	}

	var tl struct {
		Attempts []struct {
			Step    string `json:"step"`
			Number  int    `json:"number"`
			Outcome string `json:"outcome"`
		} `json:"attempts"`
		Decisions []struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			Answered bool   `json:"answered"`
			Choice   string `json:"choice"`
		} `json:"decisions"`
		Events []store.Receipt `json:"events"`
	}
	if err := json.Unmarshal([]byte(d.run("", "task", "timeline", "--store", d.store, "--repo", "app", "t-1", "--json")), &tl); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range tl.Attempts {
		got = append(got, a.Step+"-"+strconv.Itoa(a.Number)+" "+a.Outcome)
	}
	for _, d := range tl.Decisions {
		got = append(got, d.ID+" "+d.Kind+" "+strconv.FormatBool(d.Answered)+" "+d.Choice)
	}
	if exp := []string{"plan-1 succeeded", "build-1 succeeded", "accept-1 approval true approved", "sign-off-1 review true approved", "deliver-1 merge false "}; !slices.Equal(got, exp) {
		t.Errorf("timeline records:\n%q\nwant\n%q", got, exp)
	}
	var order []string
	for _, e := range tl.Events {
		switch e.Operation {
		case "create-task", "start-attempt", "finish-attempt", "open-decision", "answer-decision":
			order = append(order, e.Operation+" "+e.Record)
		}
	}
	exp := []string{
		"create-task task",
		"start-attempt attempt/plan/1", "finish-attempt attempt/plan/1", "open-decision decision/accept-1", "answer-decision decision/accept-1",
		"start-attempt attempt/build/1", "finish-attempt attempt/build/1", "open-decision decision/sign-off-1", "answer-decision decision/sign-off-1",
		"open-decision decision/deliver-1",
	}
	if !slices.Equal(order, exp) {
		t.Errorf("timeline events:\n%q\nwant\n%q", order, exp)
	}
}

// newBinCopy is newBinDirector over a Director copy holding the config directory cfgDir's files, its
// director.store and state/, with the director flags --copy and --repo.
func newBinCopy(t *testing.T, cfgDir string) *binDirector {
	t.Helper()
	dir, err := os.MkdirTemp("", "gk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.CopyFS(dir, os.DirFS(cfgDir)); err != nil {
		t.Fatal(err)
	}
	d := newBinDirector(t, filepath.Join(dir, "config.toml"))
	d.store, d.state = filepath.Join(dir, "director.store"), filepath.Join(dir, "state")
	d.flags = []string{"--copy", dir, "--repo", "app"}
	d.run("", "store", "init", d.store)
	return d
}

// waitDecision waits until the store records decision id of task app/t-1, as the watcher's advance opens it.
func (d *binDirector) waitDecision(id string) {
	d.t.Helper()
	for end := time.Now().Add(testenv.Ceiling(2 * time.Minute)); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
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
	d.t.Fatalf("decision %s was not opened within two minutes", id)
}

// TestTaskRunsEndToEndWithTheWatcher is TestTaskRunsEndToEnd from a Director copy with no step done typed: each
// attempt's launcher, the per-task watcher (D3), takes its own result and opens the next decision.
func TestTaskRunsEndToEndWithTheWatcher(t *testing.T) {
	d := newBinCopy(t, filepath.Join("..", "..", "internal", "cli", "testdata", "run"))
	d.newTask()
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"attempt","step":"plan","attempt":1}`)
	d.launched("plan", 1)
	d.waitDecision("accept-1")
	d.want(d.answer("accept-1"), `{"wait":"attempt","step":"build","attempt":1}`)
	d.launched("build", 1)
	d.waitDecision("sign-off-1")
	d.want(d.answer("sign-off-1"), `{"wait":"decision","step":"deliver","decision":"deliver-1"}`)

	d.stopAll()
	var tl struct {
		Attempts []struct {
			Step    string `json:"step"`
			Number  int    `json:"number"`
			Outcome string `json:"outcome"`
		} `json:"attempts"`
	}
	if err := json.Unmarshal([]byte(d.run("", "task", "timeline", "--store", d.store, "--repo", "app", "t-1", "--json")), &tl); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range tl.Attempts {
		got = append(got, a.Step+"-"+strconv.Itoa(a.Number)+" "+a.Outcome)
	}
	if exp := []string{"plan-1 succeeded", "build-1 succeeded"}; !slices.Equal(got, exp) {
		t.Errorf("timeline attempts %q, want %q", got, exp)
	}
}

// TestTheWatcherTellsTheDirectorOfAKilledWorker: from a copy, killing the plan attempt's worker fails the
// attempt, and its launcher's advance sends the Director attempt-failed with nobody typing task start.
func TestTheWatcherTellsTheDirectorOfAKilledWorker(t *testing.T) {
	d := newBinCopy(t, neverPlanned(t))
	d.newTask()
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"attempt","step":"plan","attempt":1}`)
	d.launched("plan", 1)

	var worker int
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); worker == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the plan attempt's launcher did not record its worker")
		}
		s, err := store.Open(d.store, store.Options{ReadOnly: true})
		if err != nil {
			continue
		}
		own, err := s.Ownership("app", "t-1", "plan", 1)
		_ = s.Close()
		if err == nil && own.State == store.OwnerRunning {
			worker = own.Containment.Worker.PID
		}
	}
	p, err := os.FindProcess(worker)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	read := []string{"messages", "read", "--store", d.store, "app.t-1.attempt-failed.plan-1"}
	var msg struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); ; time.Sleep(100 * time.Millisecond) {
		out, stderr, err := d.try("", read...)
		if err == nil {
			if err := json.Unmarshal([]byte(out), &msg); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(end) {
			log, _ := os.ReadFile(filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}.Dir(d.state), "launcher.log"))
			t.Fatalf("no attempt-failed message: %v\n%s%s\nlauncher log:\n%s", err, out, stderr, log)
		}
	}
	if msg.Type != "attempt-failed" || !strings.Contains(msg.Text, "Attempt 1 of step plan failed") {
		t.Errorf("message %+v, want attempt-failed for plan-1", msg)
	}
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"director","step":"plan","attempt":1}`)
}

// TestTheWatcherTellsTheDirectorOfAFailedAdvance (F35): from a copy, the test holds task app/t-1's lock while the
// plan attempt ends and past both of its launcher's advances, each waiting the task lock's wait; the launcher then
// sends the Director advance-failed, director status lists the attempt nobody took as an advance item first, and once
// the lock is released task start takes the attempt and opens the accept decision.
func TestTheWatcherTellsTheDirectorOfAFailedAdvance(t *testing.T) {
	dir := runCopy(t)
	plan := filepath.Join(dir, "baseline", "stages", "plan.md")
	f, err := os.OpenFile(plan, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The delay lets the test take the task lock before the attempt ends and its launcher advances.
	if _, err := f.WriteString("guildhall-fake: delay=5s\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	d := newBinCopy(t, dir)
	d.newTask()
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"attempt","step":"plan","attempt":1}`)
	d.launched("plan", 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	release, err := artisan.LockAttempt(ctx, filepath.Join(d.state, "app", "t-1"))
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	unlock := func() { once.Do(release) }
	defer unlock()

	read := []string{"messages", "read", "--store", d.store, "app.t-1.advance-failed.plan-1"}
	var msg struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	for end := time.Now().Add(5 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
		out, stderr, err := d.try("", read...)
		if err == nil {
			if err := json.Unmarshal([]byte(out), &msg); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(end) {
			log, _ := os.ReadFile(filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}.Dir(d.state), "launcher.log"))
			t.Fatalf("no advance-failed message: %v\n%s%s\nlauncher log:\n%s", err, out, stderr, log)
		}
	}
	if msg.Type != "advance-failed" || !strings.Contains(msg.Text, "step done") || !strings.Contains(msg.Text, "lock task app/t-1") {
		t.Errorf("message %+v, want advance-failed naming step done and the lock error", msg)
	}
	var status struct {
		NeedsMe []struct {
			Kind, TaskID, Step, Command string
			Attempt                     int
		} `json:"needs_me"`
	}
	if err := json.Unmarshal([]byte(d.run("", "director", "status", "--copy", d.flags[1], "--json")), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.NeedsMe) == 0 || status.NeedsMe[0].Kind != "advance" || status.NeedsMe[0].Step != "plan" || status.NeedsMe[0].Attempt != 1 ||
		!strings.HasPrefix(status.NeedsMe[0].Command, "guildhall step done --repo app --task t-1 --step plan --attempt 1 --result ") {
		t.Errorf("needs_me %+v, want first the plan attempt's advance with its step done", status.NeedsMe)
	}

	unlock()
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"decision","step":"accept","decision":"accept-1"}`)
}

// TestRetryAfterAKilledLauncher is F27 through the binary: the plan attempt's launcher is killed outright,
// artisan resume settles the attempt failed, task start waits for the Director, and step retry starts
// attempt 2 with its reason recorded; repeating the retry changes nothing.
func TestRetryAfterAKilledLauncher(t *testing.T) {
	// The plan attempts never publish a result, so the launcher is still running when it is killed.
	d := newBinDirector(t, filepath.Join(neverPlanned(t), "config.toml"))
	d.newTask()
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"attempt","step":"plan","attempt":1}`)
	d.launched("plan", 1)

	var pid int
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); pid == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the plan attempt's launcher did not record itself running")
		}
		s, err := store.Open(d.store, store.Options{ReadOnly: true})
		if err != nil {
			continue
		}
		own, err := s.Ownership("app", "t-1", "plan", 1)
		_ = s.Close()
		if err == nil && own.State == store.OwnerRunning {
			pid = own.LauncherIdentity.PID
		}
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	// resume refuses while the killed launcher is still seen live.
	resume := []string{"artisan", "resume", "--store", d.store, "--state", d.state, "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1"}
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); ; time.Sleep(100 * time.Millisecond) {
		out, stderr, err := d.try("", resume...)
		if err == nil {
			if !strings.Contains(out, `"attempt_outcome":"failed"`) {
				t.Fatalf("resume settled %s", out)
			}
			break
		}
		if time.Now().After(end) {
			t.Fatalf("artisan resume: %v\n%s%s", err, out, stderr)
		}
	}

	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"director","step":"plan","attempt":1}`)
	retry := []string{"--task", "t-1", "--step", "plan", "--attempt", "1", "--reason", "The launcher was killed; try again."}
	d.want(d.advance([]string{"step", "retry"}, retry...), `{"wait":"attempt","step":"plan","attempt":2}`)
	d.launched("plan", 2)
	d.want(d.advance([]string{"step", "retry"}, retry...), `{"wait":"attempt","step":"plan","attempt":2}`)

	var tl struct {
		Attempts []struct {
			Step    string `json:"step"`
			Number  int    `json:"number"`
			Outcome string `json:"outcome"`
		} `json:"attempts"`
		Events []store.Receipt `json:"events"`
	}
	if err := json.Unmarshal([]byte(d.run("", "task", "timeline", "--store", d.store, "--repo", "app", "t-1", "--json")), &tl); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range tl.Attempts {
		got = append(got, a.Step+"-"+strconv.Itoa(a.Number)+" "+a.Outcome)
	}
	if exp := []string{"plan-1 failed", "plan-2 "}; !slices.Equal(got, exp) {
		t.Errorf("timeline attempts %q, want %q", got, exp)
	}
	var order []string
	for _, e := range tl.Events {
		switch e.Operation {
		case "start-attempt", "finish-attempt", "send-message":
			order = append(order, e.Operation+" "+e.Record)
		}
	}
	exp := []string{
		"start-attempt attempt/plan/1", "finish-attempt attempt/plan/1", "send-message message/app.t-1.attempt-failed.plan-1",
		"start-attempt attempt/plan/2", "send-message message/app.t-1.attempt-retried.plan-1",
	}
	if !slices.Equal(order, exp) {
		t.Errorf("timeline events:\n%q\nwant\n%q", order, exp)
	}
}

// git runs git in dir and returns its trimmed output.
func (d *binDirector) git(dir string, args ...string) string {
	d.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		d.t.Fatalf("git %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// runCopy copies internal/cli/testdata/run to a temporary directory and returns it.
func runCopy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "internal", "cli", "testdata", "run"))); err != nil {
		t.Fatal(err)
	}
	return dir
}

// neverPlanned copies internal/cli/testdata/run to a temporary directory, which it returns, with the plan stage's
// fake Artisan set never to publish a result, so its launcher runs until it is stopped.
func neverPlanned(t *testing.T) string {
	t.Helper()
	dir := runCopy(t)
	plan := filepath.Join(dir, "baseline", "stages", "plan.md")
	f, err := os.OpenFile(plan, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("guildhall-fake: result=never\nguildhall-fake: lifetime=60s\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestRetryWithAnUnknownAdapterSendsNoReason pins T1 through the binary, where interp.CheckAdapter allows any
// name: a retry whose profile names an adapter the launcher does not run fails to launch attempt 2 and stores no
// reason, so once the profile is fixed a retry with another reason launches attempt 2 and stores that reason.
func TestRetryWithAnUnknownAdapterSendsNoReason(t *testing.T) {
	dir := neverPlanned(t)
	cfg := filepath.Join(dir, "claude.toml")
	profile := func(adapter string) {
		t.Helper()
		b, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		i := bytes.Index(b, []byte("[profile.planner]"))
		if err := os.WriteFile(cfg, append(b[:i:i], "[profile.planner]\nadapter = \""+adapter+"\"\n"...), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profile("fake")
	d := newBinDirector(t, cfg)
	d.newTask()
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"attempt","step":"plan","attempt":1}`)
	d.launched("plan", 1)
	d.run("", "artisan", "stop", "--store", d.store, "--state", d.state, "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1")

	profile("nosuch")
	retry := append(append([]string{"step", "retry"}, d.flags...), "--task", "t-1", "--step", "plan", "--attempt", "1")
	if out, stderr, err := d.try("", append(retry, "--reason", "Reason A")...); err == nil || !strings.Contains(stderr, "launch attempt 2") {
		t.Fatalf("retry with adapter nosuch: %v\nstdout %s\nstderr %s", err, out, stderr)
	}
	reason := []string{"messages", "read", "--store", d.store, "app.t-1.attempt-retried.plan-1"}
	if out, _, err := d.try("", reason...); err == nil {
		t.Fatalf("the refused retry stored its reason: %s", out)
	}

	profile("fake")
	d.want(d.run("", append(retry, "--reason", "Reason B")...), `{"wait":"attempt","step":"plan","attempt":2}`)
	d.launched("plan", 2)
	var msg struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(d.run("", reason...)), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Text != "Reason B" {
		t.Errorf("retry reason %q, want %q", msg.Text, "Reason B")
	}
}
