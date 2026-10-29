package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/launch/launchtest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// The Director safety checks (docs/director.md#safety-checks) drive the built binary through `director start` with a stand-in runtime playing the Director from its role prompt's script: the fake Director and
// the Claude stub, this test binary on PATH as claude, play the same script, so every check runs on both. They
// make no model calls. Each check waits on events with ceilings sized for a slow runner and launches few
// processes: one build of the binary serves them all.

// ceiling bounds every wait for something the binary does.
const ceiling = 3 * time.Minute

// safetyRuntimes are the session runtimes every check runs on.
var safetyRuntimes = []string{"fake", "claude"}

var (
	toolsOnce sync.Once
	toolsDir  string
	toolsErr  error
)

func TestMain(m *testing.M) {
	runHerdrStubIf()
	claudetest.RunIfStub()
	// A plain director start binds the Herdr pane its HERDR_* names, so checks run inside a Herdr pane would bind
	// it; every check runs as outside Herdr, as on CI, and those that need a pane pass it.
	for _, k := range []string{"HERDR_SESSION", "HERDR_SOCKET_PATH", "HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID", "HERDR_ENV"} {
		_ = os.Unsetenv(k)
	}
	code := testenv.RunWithClaudeGuard(m)
	if toolsDir != "" {
		_ = os.RemoveAll(toolsDir)
	}
	os.Exit(code)
}

// sessionPath is a PATH holding the built binary as guildhall and this test binary as claude and herdr, the
// stand-ins, ahead of the inherited PATH; the binary is built once for every check.
func sessionPath(t *testing.T) string {
	t.Helper()
	toolsOnce.Do(func() {
		gobin, err := exec.LookPath("go")
		if err != nil {
			toolsErr = err
			return
		}
		if toolsDir, toolsErr = os.MkdirTemp("", "gt"); toolsErr != nil {
			return
		}
		exe := ""
		if runtime.GOOS == "windows" {
			exe = ".exe"
		}
		if out, err := exec.Command(gobin, "build", "-o", filepath.Join(toolsDir, "guildhall"+exe), ".").CombinedOutput(); err != nil {
			toolsErr = fmt.Errorf("go build: %w\n%s", err, out)
			return
		}
		self, err := os.Executable()
		if err != nil {
			toolsErr = err
			return
		}
		for _, name := range []string{claude.Program, "herdr"} {
			stub := filepath.Join(toolsDir, name+exe)
			if err := os.Link(self, stub); err != nil {
				b, rerr := os.ReadFile(self)
				if rerr != nil {
					toolsErr = rerr
					return
				}
				if toolsErr = os.WriteFile(stub, b, 0o755); toolsErr != nil {
					return
				}
			}
		}
	})
	if toolsErr != nil {
		t.Fatal(toolsErr)
	}
	return toolsDir + string(os.PathListSeparator) + os.Getenv("PATH")
}

// safetyCopy is one check's Director copy: internal/cli/testdata/run pointed at a real repository and remote,
// local settings added under [local], an empty store, and the plan and build stages' fake Artisans recording
// their processes in the copy's inventory directory, with plan's extra directives. The Herdr stand-in records
// the lines typed into the Director's pane in the copy's herdr directory.
type safetyCopy struct {
	t                *testing.T
	dir, path, turns string
	inventory, herdr string
	// rt is the runtime the last session started with.
	rt string
}

func newSafetyCopy(t *testing.T, local, plan string) *safetyCopy {
	t.Helper()
	path := sessionPath(t)
	// Short, for the launchers' endpoint sockets under state/.
	dir, err := os.MkdirTemp("", "gs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "internal", "cli", "testdata", "run"))); err != nil {
		t.Fatal(err)
	}
	repository, remote, base := gittest.Repository(t)
	c := &safetyCopy{t: t, dir: dir, path: path, turns: filepath.Join(dir, "turns.log"), inventory: filepath.Join(dir, "inventory"),
		herdr: filepath.Join(dir, "herdr")}
	if err := os.Mkdir(c.herdr, 0o700); err != nil {
		t.Fatal(err)
	}
	c.edit("config.toml", func(b string) string {
		b = strings.Replace(b, `path = "/srv/app"`, "path = '"+repository+"'", 1)
		b = strings.Replace(b, `remote = "git@github.com:example/app.git"`, "remote = '"+remote+"'", 1)
		return strings.Replace(b, "[local]\n", "[local]\n"+local, 1)
	})
	c.setPlan(plan)
	c.edit(filepath.Join("baseline", "stages", "build.md"), func(b string) string {
		// The fake build commits nothing, so the head it reports is the task branch's tip, the default branch.
		return strings.Replace(b, fixtureHead, base, 1) + "guildhall-fake: inventory=" + c.inventory + "\n"
	})
	// store init --copy makes the empty store and gives the copy its marker, as a Guildmaster's new copy gets it.
	if out, err := c.command(nil, "store", "init", "--copy", dir).CombinedOutput(); err != nil {
		t.Fatalf("store init --copy: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".guildhall-copy")); err != nil {
		t.Fatalf("store init --copy wrote no copy marker: %v", err)
	}
	if live, turns := c.liveProcesses(), c.turnLines(); len(live) != 0 || len(turns) != 0 {
		t.Fatalf("before the check: owned processes %v, stand-in turns %q", live, turns)
	}
	t.Log("stand-in turns 0, owned processes 0")
	return c
}

func (c *safetyCopy) edit(name string, fn func(string) string) {
	c.t.Helper()
	p := filepath.Join(c.dir, name)
	b, err := os.ReadFile(p)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(fn(string(b))), 0o600); err != nil {
		c.t.Fatal(err)
	}
}

// setPlan makes the plan stage's prompt the shipped one with the inventory directive and the directives in extra.
func (c *safetyCopy) setPlan(extra string) {
	c.t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "cli", "testdata", "run", "baseline", "stages", "plan.md"))
	if err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "baseline", "stages", "plan.md"), append(b, "guildhall-fake: inventory="+c.inventory+"\n"+extra...), 0o600); err != nil {
		c.t.Fatal(err)
	}
}

// script makes the copy's Director role prompt play steps on both stand-ins.
func (c *safetyCopy) script(steps ...string) {
	c.t.Helper()
	var b strings.Builder
	b.WriteString("# Director\n\n")
	for _, prefix := range []string{fake.SessionDirective, claudetest.Directive} {
		for _, s := range steps {
			b.WriteString(prefix + " " + s + "\n")
		}
	}
	p := filepath.Join(c.dir, "baseline", "roles", "director.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		c.t.Fatal(err)
	}
}

func (c *safetyCopy) command(stdin io.Reader, args ...string) *exec.Cmd {
	cmd := exec.Command(filepath.Join(strings.SplitN(c.path, string(os.PathListSeparator), 2)[0], "guildhall"), args...)
	cmd.Env = append(os.Environ(), "PATH="+c.path, fake.TurnLogEnv+"="+c.turns, HerdrStubEnv+"="+c.herdr)
	cmd.Stdin = stdin
	// Outside any copy, so a command finds a copy only from --copy.
	cmd.Dir = c.t.TempDir()
	return cmd
}

// guildhall runs the binary as the Guildmaster would, in the copy, which it finds by copy discovery, and fails
// the check unless it exits 0.
func (c *safetyCopy) guildhall(stdin string, args ...string) string {
	c.t.Helper()
	cmd := c.command(strings.NewReader(stdin), args...)
	// Run as the Guildmaster's shell would, whatever session or attempt runs these tests (d33).
	cmd.Env = append(cmd.Env, "GUILDHALL_SESSION=", "GUILDHALL_ATTEMPT=")
	cmd.Dir = c.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		c.t.Fatalf("guildhall %s: %v\nstdout %s\nstderr %s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// approve approves the copy's workflow for app, which has no validate step and so is below the review floor.
func (c *safetyCopy) approve() {
	c.t.Helper()
	c.guildhall("Approved for the safety checks.\n", "workflow", "approve", "--repo", "app", "--words-file", "-")
}

// session is one `director start <runtime>` of the copy in this terminal: the test holds its standard input open, as
// a terminal does, and collects its output. Lines written to stdin are what the terminal types to the runtime.
type session struct {
	c      *safetyCopy
	cmd    *exec.Cmd
	stdin  *os.File
	done   chan struct{}
	err    error
	mu     sync.Mutex
	output bytes.Buffer
}

func (s *session) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.Write(p)
}

func (s *session) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.String()
}

// start starts a session with runtime rt and the further director start args.
func (c *safetyCopy) start(rt string, args ...string) *session {
	c.t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		c.t.Fatal(err)
	}
	c.rt = rt
	s := &session{c: c, stdin: w, done: make(chan struct{})}
	s.cmd = c.command(r, append([]string{"director", "start", rt, "--copy", c.dir}, args...)...)
	s.cmd.Stdout, s.cmd.Stderr = s, s
	if err := s.cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	_ = r.Close()
	go func() {
		s.err = s.cmd.Wait()
		close(s.done)
	}()
	c.t.Cleanup(func() {
		_ = s.stdin.Close()
		select {
		case <-s.done:
		case <-time.After(ceiling):
			_ = s.cmd.Process.Kill()
			c.t.Errorf("director start did not end:\n%s", s.text())
		}
	})
	return s
}

// end closes the session's input and waits for the supervisor to exit, returning its exit status.
func (s *session) end() int {
	s.c.t.Helper()
	_ = s.stdin.Close()
	select {
	case <-s.done:
	case <-time.After(ceiling):
		s.c.t.Fatalf("director start did not end:\n%s", s.text())
	}
	s.c.t.Logf("session output:\n%s", s.text())
	return s.cmd.ProcessState.ExitCode()
}

// turnLines are the turns the stand-in runtimes recorded in the copy, one "<pid> <kind>" line each.
func (c *safetyCopy) turnLines() []string {
	c.t.Helper()
	b, err := os.ReadFile(c.turns)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		c.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// waitFor polls cond until it holds, failing the check with what after the ceiling.
func (c *safetyCopy) waitFor(what string, cond func() bool) {
	c.t.Helper()
	for end := time.Now().Add(ceiling); !cond(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(end) {
			c.t.Fatalf("%s: not within %s", what, ceiling)
		}
	}
}

// typed are the lines the Herdr stand-in accepted for the Director's pane.
func (c *safetyCopy) typed() []string { return c.stubLines("typed") }

// toasts are the notifications the Herdr stand-in showed, each its arguments joined by spaces.
func (c *safetyCopy) toasts() []string { return c.stubLines("toasts") }

// stubLines are the lines the Herdr stand-in recorded in its file name.
func (c *safetyCopy) stubLines(name string) []string {
	c.t.Helper()
	b, err := os.ReadFile(filepath.Join(c.herdr, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		c.t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// read runs fn over the copy's store opened read-only, retrying while a writer holds it.
func read[T any](c *safetyCopy, fn func(*store.Store) (T, error)) T {
	c.t.Helper()
	var last error
	for end := time.Now().Add(ceiling); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		s, err := store.Open(filepath.Join(c.dir, "director.store"), store.Options{ReadOnly: true})
		if err != nil {
			last = err
			continue
		}
		v, err := fn(s)
		_ = s.Close()
		if err == nil {
			return v
		}
		last = err
		if !errors.Is(err, store.ErrNotFound) {
			break
		}
	}
	c.t.Fatalf("read the store: %v", last)
	var zero T
	return zero
}

// has reports whether fn finds its record, an absent one being ErrNotFound.
func has(c *safetyCopy, fn func(*store.Store) error) bool {
	c.t.Helper()
	return read(c, func(s *store.Store) (bool, error) {
		err := fn(s)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return err == nil, err
	})
}

func (c *safetyCopy) hasTask(id string) bool {
	return has(c, func(s *store.Store) error { _, err := s.Task("app", id); return err })
}

func (c *safetyCopy) hasDecision(id string) bool {
	return read(c, func(s *store.Store) (bool, error) {
		ds, err := s.Decisions("app", "t-1")
		return slices.ContainsFunc(ds, func(d store.Decision) bool { return d.ID == id }), err
	})
}

func (c *safetyCopy) attempts(task string) []store.StageAttempt {
	return read(c, func(s *store.Store) ([]store.StageAttempt, error) { return s.Attempts("app", task) })
}

func (c *safetyCopy) inbox() []store.Message {
	return read(c, func(s *store.Store) ([]store.Message, error) {
		m, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
		return m, err
	})
}

func (c *safetyCopy) latestSession() store.DirectorSession {
	return read(c, func(s *store.Store) (store.DirectorSession, error) { return s.LatestDirectorSession() })
}

// liveProcesses are the inventory's fake Artisan processes still alive.
func (c *safetyCopy) liveProcesses() []artisan.ProcessRecord {
	c.t.Helper()
	entries, err := artisantest.Inventory(c.inventory)
	if err != nil {
		c.t.Fatal(err)
	}
	var live []artisan.ProcessRecord
	for _, e := range entries {
		if lv, _ := artisan.Probe(e.Identity); lv == artisan.Alive {
			live = append(live, e)
		}
	}
	return live
}

// stopAttempt stops attempt n of task's step and proves its launcher gone by launchtest's rule.
func (c *safetyCopy) stopAttempt(task, step string, n int) {
	c.t.Helper()
	k := artisan.AttemptKey{Repository: "app", TaskID: task, Step: step, Attempt: n}
	launchtest.StopAll(c.t, []artisan.AttemptKey{k}, func(k artisan.AttemptKey) (launch.StopReport, error) {
		cmd := c.command(nil, "artisan", "stop", "--copy", c.dir, "--repo", k.Repository, "--task", k.TaskID, "--step", k.Step, "--attempt", strconv.Itoa(k.Attempt))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return launchtest.Report(stdout.String(), stderr.String(), err)
	})
}

// finish proves no fake Artisan process outlives the check and records the counts the check ends with. A Claude
// session's pane is never typed into: no `pane run` reached the Herdr stand-in (d8 done-test 1).
func (c *safetyCopy) finish(wantTurns int) {
	c.t.Helper()
	if c.rt == "claude" {
		if typed := c.typed(); len(typed) != 0 {
			c.t.Errorf("pane run typed %q into a Claude session's pane", typed)
		}
	}
	c.waitFor("every owned process gone", func() bool { return len(c.liveProcesses()) == 0 })
	entries, _ := artisantest.Inventory(c.inventory)
	turns := c.turnLines()
	c.t.Logf("stand-in turns %d, owned processes recorded %d, alive 0", len(turns), len(entries))
	if len(turns) != wantTurns {
		c.t.Errorf("the stand-in took %d turns %q, want %d", len(turns), turns, wantTurns)
	}
}

// eachRuntime runs check on every session runtime, side by side.
func eachRuntime(t *testing.T, check func(t *testing.T, rt string)) {
	t.Parallel()
	for _, rt := range safetyRuntimes {
		t.Run(rt, func(t *testing.T) {
			t.Parallel()
			check(t, rt)
		})
	}
}

// TestSafetyApproval: the scripted Director's task new on a workflow below the review floor is refused, then it
// approves the workflow with words and the same task new succeeds.
func TestSafetyApproval(t *testing.T) {
	eachRuntime(t, func(t *testing.T, rt string) {
		c := newSafetyCopy(t, "", "")
		words := filepath.Join(c.dir, "words.md")
		if err := os.WriteFile(words, []byte("Approved: the fake stages are what this check runs.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c.script(
			"guildhall director ready",
			"guildhall task new --repo app --title Sort t-1",
			"guildhall workflow approve --repo app --words-file "+words,
			"guildhall task new --repo app --title Sort t-1",
			"/exit")
		s := c.start(rt)
		if code := s.end(); code != 0 {
			t.Fatalf("director start exited %d", code)
		}
		out := s.text()
		if n := strings.Count(out, "session script: guildhall task new"); n != 1 || !strings.Contains(out, "below the review floor") {
			t.Errorf("want one refused task new naming the review floor, got %d refusals:\n%s", n, out)
		}
		if !c.hasTask("t-1") {
			t.Error("the approved task new recorded no task")
		}
		c.finish(1)
	})
}

// TestSafetyGuardRails: with max_running_attempts 1, the scripted Director's task new for an unlisted repository
// is refused, its second task's start is queued while the first runs, shown in director status under start_queue,
// and started by the session's start-queue watcher within one watcher interval of that attempt being stopped, with no
// second task start and no budget-refused message.
func TestSafetyGuardRails(t *testing.T) {
	eachRuntime(t, func(t *testing.T, rt string) {
		c := newSafetyCopy(t, "max_running_attempts = \"1\"\n", "guildhall-fake: result=never\nguildhall-fake: lifetime=5m\n")
		c.approve()
		c.script(
			"guildhall director ready",
			"guildhall task new --repo other --title Elsewhere t-9",
			"guildhall task new --repo app --title One t-1",
			"guildhall task new --repo app --title Two t-2",
			"guildhall task start --repo app t-1",
			"guildhall task start --repo app t-2",
			"guildhall director status --json",
			"guildhall artisan stop --repo app --task t-1 --step plan --attempt 1")
		s := c.start(rt)
		stopped := func() bool {
			a := c.attempts("t-1")
			return len(a) == 1 && a[0].Outcome != ""
		}
		c.waitFor("t-1's plan attempt stopped by the script", stopped)
		c.waitFor("t-2's queued plan started by the start-queue watcher", func() bool { return len(c.attempts("t-2")) == 1 })
		code := s.end()
		c.stopAttempt("t-1", "plan", 1)
		c.stopAttempt("t-2", "plan", 1)
		if code != 0 {
			t.Fatalf("director start exited %d", code)
		}
		out := s.text()
		if !strings.Contains(out, `guard rail repositories: the config does not list repository "other"`) {
			t.Errorf("the unlisted repository was not refused by its guard rail:\n%s", out)
		}
		if !strings.Contains(out, `{"wait":"queued","step":"plan","attempt":1,"position":1}`) {
			t.Errorf("t-2's start was not queued:\n%s", out)
		}
		if !strings.Contains(out, `"start_queue":[{"repository":"app","task":"t-2","step":"plan","attempt":1,"request":"start","priority":"p3","position":1,`) {
			t.Errorf("director status did not list t-2's queued start under start_queue:\n%s", out)
		}
		if strings.Contains(out, "guard rail max_running_attempts") {
			t.Errorf("the running cap refused a launch:\n%s", out)
		}
		if has(c, func(s *store.Store) error { _, err := s.Task("other", "t-9"); return err }) {
			t.Error("the unlisted repository's task was recorded")
		}
		if got := c.attempts("t-2"); len(got) != 1 || got[0].Step != "plan" {
			t.Errorf("t-2 attempts %+v, want plan started once t-1's attempt stopped", got)
		}
		if slices.ContainsFunc(c.inbox(), func(m store.Message) bool { return m.Type == "budget-refused" }) {
			t.Error("a queued start sent budget-refused")
		}
		c.finish(1)
	})
}

// TestSafetyCrashRecovery: task t-1's plan attempt is running when the Director's runtime is killed and then its
// launcher; with a Guildmaster note unread, director start again renders a startup.md listing the attempt to
// resume and the note, refuses task new until the scripted Director runs ready, and the Director resumes and
// retries the attempt; the watcher then takes the task through the workflow, the check answering each decision as
// the Director would with decide answer and task start --decision, to the deliver step's merge decision. director
// status lists the killed launcher's attempt with resume: true.
func TestSafetyCrashRecovery(t *testing.T) {
	eachRuntime(t, func(t *testing.T, rt string) {
		c := newSafetyCopy(t, "", "guildhall-fake: result=never\nguildhall-fake: lifetime=5m\n")
		c.approve()
		c.guildhall("", "task", "new", "--repo", "app", "--title", "Sort", "t-1")
		c.script("guildhall director ready", "guildhall task start --repo app t-1")
		first := c.start(rt)
		var launcher store.ProcessIdentity
		c.waitFor("plan attempt 1 running", func() bool {
			own := read(c, func(s *store.Store) (store.Ownership, error) {
				own, err := s.Ownership("app", "t-1", "plan", 1)
				if errors.Is(err, store.ErrNotFound) {
					return own, nil
				}
				return own, err
			})
			launcher = own.LauncherIdentity
			return own.State == store.OwnerRunning
		})
		// The crash: the runtime first, then the attempt's launcher.
		turns := c.turnLines()
		if len(turns) != 1 {
			t.Fatalf("turns before the crash %q, want the first message's", turns)
		}
		pid, _, _ := strings.Cut(turns[0], " ")
		kill(t, pid)
		if code := first.end(); code == 0 {
			t.Error("director start exited 0 after its runtime was killed")
		}
		if d := c.latestSession(); d.Number != 1 || d.State != store.DirectorStopped {
			t.Errorf("session after the crash %+v, want 1 stopped", d)
		}
		kill(t, strconv.Itoa(launcher.PID))
		c.waitFor("the launcher gone", func() bool {
			lv, _ := artisan.Probe(artisan.Identity(launcher))
			return lv != artisan.Alive
		})
		c.guildhall(`{"schema":"guildhall.message/1","id":"n-1","recipient":"director","repository":"app","task":"t-1","type":"note","text":"Check on t-1."}`,
			"messages", "send", "director", "-")
		type needsMe struct {
			Repository string `json:"repository"`
			TaskID     string `json:"task_id"`
			Step       string `json:"step"`
			Attempt    int    `json:"attempt"`
			Resume     bool   `json:"resume"`
		}
		var status struct {
			NeedsMe []needsMe `json:"needs_me"`
		}
		if err := json.Unmarshal([]byte(c.guildhall("", "director", "status", "--json")), &status); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(status.NeedsMe, needsMe{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1, Resume: true}) {
			t.Errorf("director status needs_me %+v, want app t-1 plan attempt 1 with resume: true", status.NeedsMe)
		}

		c.setPlan("")
		c.script(
			"guildhall task new --repo app --title Early t-early",
			"guildhall director ready",
			"guildhall artisan resume --repo app --task t-1 --step plan --attempt 1",
			"guildhall step retry --repo app --task t-1 --step plan --attempt 1 --reason relaunch-after-the-crash",
			"guildhall task new --repo app --title Late t-late",
			"/exit")
		second := c.start(rt)
		if code := second.end(); code != 0 {
			t.Fatalf("second director start exited %d", code)
		}
		startup, err := os.ReadFile(filepath.Join(c.dir, "director", "session-2", "startup.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"run `guildhall artisan resume --repo app --task t-1 --step plan --attempt 1`", "1 unread Director message"} {
			if !strings.Contains(string(startup), want) {
				t.Errorf("startup.md does not name %q:\n%s", want, startup)
			}
		}
		if c.hasTask("t-early") || !c.hasTask("t-late") {
			t.Errorf("task new before ready recorded %v, after ready %v; want refused, then admitted", c.hasTask("t-early"), c.hasTask("t-late"))
		}
		// The watcher takes t-1 on through the fake-straight workflow, the check answering each decision as the
		// Director would, with decide answer and task start --decision, to the deliver step's merge decision, its
		// final wait.
		answer := filepath.Join(c.dir, "answer.md")
		if err := os.WriteFile(answer, []byte("approved\nLooks right.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, d := range []struct{ decision, next string }{
			{"accept-1", `{"wait":"attempt","step":"build","attempt":1}`},
			{"sign-off-1", `{"wait":"decision","step":"deliver","decision":"deliver-1"}`},
		} {
			c.waitFor("decision "+d.decision+" opened by the watcher", func() bool {
				return c.hasDecision(d.decision)
			})
			c.guildhall("", "decide", "answer", "--repo", "app", "--task", "t-1", "--decision", d.decision, "--answer-file", answer)
			if got := strings.TrimSpace(c.guildhall("", "task", "start", "--repo", "app", "--decision", d.decision, "t-1")); got != d.next {
				t.Fatalf("task start --decision %s: got %s, want %s", d.decision, got, d.next)
			}
		}
		var got []string
		for _, a := range c.attempts("t-1") {
			got = append(got, fmt.Sprintf("%s-%d %s", a.Step, a.Number, a.Outcome))
		}
		if want := []string{"build-1 succeeded", "plan-1 failed", "plan-2 succeeded"}; !slices.Equal(got, want) {
			t.Errorf("t-1 attempts %q, want %q", got, want)
		}
		c.stopAttempt("t-1", "plan", 2)
		c.stopAttempt("t-1", "build", 1)
		c.finish(2)
	})
}

// TestSafetyZeroIdleTurns: with one open decision and nothing else, a ready Director in a bound pane takes no turn
// and is sent no wake over a 30 s hold; a Guildmaster note then records exactly one wake, woke, which types one
// line into the fake runtime's pane or is handed to the Claude session's rewake hook, and the runtime takes exactly
// one turn from it (docs/director.md#safety-checks).
func TestSafetyZeroIdleTurns(t *testing.T) {
	eachRuntime(t, func(t *testing.T, rt string) {
		c := newSafetyCopy(t, "", "")
		c.approve()
		c.guildhall("", "task", "new", "--repo", "app", "--title", "Sort", "t-1")
		c.guildhall("", "task", "start", "--repo", "app", "t-1")
		c.waitFor("decision accept-1 opened by the watcher", func() bool {
			return c.hasDecision("accept-1")
		})
		c.stopAttempt("t-1", "plan", 1)
		for _, m := range c.inbox() {
			c.guildhall("", "messages", "ack", "director", m.ID)
		}
		c.script("guildhall director ready")
		s := c.start(rt, herdrBinding...)
		c.waitFor("the session ready", func() bool { return c.latestSession().State == store.DirectorRunning })
		wakes := func() []store.Wake {
			return read(c, func(s *store.Store) ([]store.Wake, error) {
				w, _, err := s.Wakes(0, store.MaxInboxLimit)
				return w, err
			})
		}
		before := wakes()

		time.Sleep(30 * time.Second) // timing:allow deliberately hung tool fixture; caller timeout terminates it.
		if got := c.turnLines(); len(got) != 1 {
			t.Errorf("turns over the idle hold %q, want only the first message's", got)
		}
		if got := wakes(); len(got) != len(before) {
			t.Errorf("wakes over the idle hold %+v, want none", got[len(before):])
		}

		c.guildhall(`{"schema":"guildhall.message/1","id":"n-1","recipient":"director","repository":"app","task":"t-1","type":"note","text":"Take t-2 next."}`,
			"messages", "send", "director", "-")
		var got []store.Wake
		for _, w := range wakes() {
			if w.MessageID == "n-1" {
				got = append(got, w)
			}
		}
		if len(got) != 1 || got[0].Decision != store.WakeWoke || got[0].Reason != "" {
			t.Errorf("wakes for the note %+v, want exactly one, woke", got)
		}
		// Guildhall surfaced the note to the Guildmaster on the session's Herdr server, outside the model (d33).
		if got, want := c.toasts(), []string{"guildhall: note from guildmaster about t-1 --body=Take t-2 next. · guildhall messages read n-1"}; !slices.Equal(got, want) {
			t.Errorf("toasts %q, want %q", got, want)
		}
		want := "guildhall: 1 unread (note 1 from guildmaster)"
		if rt == "claude" {
			// A Claude session is woken by its rewake hook, and nothing is typed into its pane.
			c.waitFor("the runtime's rewake turn", func() bool { return len(c.turnLines()) >= 2 })
			turns := c.turnLines()
			first, _, _ := strings.Cut(turns[0], " ")
			if len(turns) != 2 || turns[1] != first+" rewake: "+want {
				t.Errorf("turns after the note %q, want the first message's and one rewake turn on %q", turns, want)
			}
			if code := s.end(); code != 0 {
				t.Fatalf("director start exited %d", code)
			}
			c.finish(2)
			return
		}
		typed := c.typed()
		if len(typed) != 1 || typed[0] != want {
			t.Fatalf("typed into the pane %q, want only %q", typed, want)
		}
		// The terminal hands the typed line to the runtime in the pane, the session's standard input.
		if _, err := io.WriteString(s.stdin, typed[0]+"\n"); err != nil {
			t.Fatal(err)
		}
		c.waitFor("the runtime's turn on the typed line", func() bool { return len(c.turnLines()) >= 2 })
		turns := c.turnLines()
		first, _, _ := strings.Cut(turns[0], " ")
		if len(turns) != 2 || turns[1] != first+" input" {
			t.Errorf("turns after the note %q, want the first message's and one input turn of the same runtime", turns)
		}
		if code := s.end(); code != 0 {
			t.Fatalf("director start exited %d", code)
		}
		c.finish(2)
	})
}

// kill kills the process pid outright.
func kill(t *testing.T, pid string) {
	t.Helper()
	n, err := strconv.Atoi(pid)
	if err != nil {
		t.Fatal(err)
	}
	p, err := os.FindProcess(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
}
