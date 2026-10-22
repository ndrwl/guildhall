package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// watchDouble is checksWatch's waiting, advance and clock: it lists tasks and records each advance, holding each
// advance until release is closed when release is set, and its rounds end at ended.
type watchDouble struct {
	mu       sync.Mutex
	ended    time.Time
	tasks    []taskRef
	listErr  error
	fail     map[taskRef]error
	advanced []taskRef
	reported []string
	release  chan struct{}
}

func (d *watchDouble) watch(every time.Duration) *checksWatch {
	return &checksWatch{every: every,
		waiting: func() ([]taskRef, error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			return slices.Clone(d.tasks), d.listErr
		},
		advance: func(r taskRef) error {
			if d.release != nil {
				<-d.release
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			d.advanced = append(d.advanced, r)
			return d.fail[r]
		},
		report: func(r taskRef, err error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.reported = append(d.reported, r.repo+"/"+r.task+": "+err.Error())
		},
		now: func() time.Time {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.ended
		},
	}
}

func (d *watchDouble) got() ([]taskRef, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.advanced), slices.Clone(d.reported)
}

var (
	t1 = taskRef{"app", "t-1"}
	t2 = taskRef{"app", "t-2"}
)

// TestChecksWatchPacesRoundsByTheRefresh: the first tick advances every waiting task at once; a tick before the
// refresh has passed advances nothing; one after it advances them again.
func TestChecksWatchPacesRoundsByTheRefresh(t *testing.T) {
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	d := &watchDouble{tasks: []taskRef{t1, t2}, ended: at}
	w := d.watch(time.Minute)
	w.tick(at)
	w.wait()
	if got, _ := d.got(); !slices.Equal(got, []taskRef{t1, t2}) {
		t.Fatalf("first round advanced %v, want t-1 then t-2", got)
	}
	w.tick(at.Add(59 * time.Second))
	w.wait()
	if got, _ := d.got(); len(got) != 2 {
		t.Fatalf("a tick within the refresh advanced %v", got[2:])
	}
	w.tick(at.Add(time.Minute))
	w.wait()
	if got, _ := d.got(); !slices.Equal(got, []taskRef{t1, t2, t1, t2}) {
		t.Fatalf("after the refresh advanced %v, want a second round", got)
	}
}

// TestChecksWatchPacesRoundsFromTheirEnd: a round that ends after it started is followed by the next only once the
// refresh has passed since it ended, so each read it made is a refresh old by then.
func TestChecksWatchPacesRoundsFromTheirEnd(t *testing.T) {
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	d := &watchDouble{tasks: []taskRef{t1}, ended: at.Add(5 * time.Second)}
	w := d.watch(time.Minute)
	w.tick(at)
	w.wait()
	w.tick(at.Add(time.Minute))
	w.wait()
	if got, _ := d.got(); len(got) != 1 {
		t.Fatalf("a tick a refresh after the round started but not after it ended advanced %v", got[1:])
	}
	w.tick(at.Add(time.Minute + 5*time.Second))
	w.wait()
	if got, _ := d.got(); !slices.Equal(got, []taskRef{t1, t1}) {
		t.Fatalf("a refresh after the round ended advanced %v, want a second round", got)
	}
}

// TestChecksWatchRefreshHasAFloor: a session's watcher rounds at its config's checks_refresh, but a refresh under
// 30 seconds, "0s" included, rounds every 30 seconds, not at every tick.
func TestChecksWatchRefreshHasAFloor(t *testing.T) {
	for _, c := range []struct {
		refresh string
		quiet   time.Duration
		every   time.Duration
	}{
		{"0s", 29 * time.Second, 30 * time.Second},
		{"1s", 29 * time.Second, 30 * time.Second},
		{"2m", 119 * time.Second, 2 * time.Minute},
	} {
		t.Run(c.refresh, func(t *testing.T) {
			app, _, _ := newTestApp(nil, nil)
			p := startPlan{cfg: &config.Config{Local: map[string]string{config.ChecksRefreshKey: c.refresh}}}
			dir := t.TempDir()
			w, err := app.watchChecks(p, 1, dir, dir, filepath.Join(dir, "store.db"), dir)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			d := &watchDouble{tasks: []taskRef{t1}, ended: at}
			dw := d.watch(0)
			w.waiting, w.advance, w.report, w.now = dw.waiting, dw.advance, dw.report, dw.now
			for _, after := range []time.Duration{0, time.Second, c.quiet, c.every} {
				w.tick(at.Add(after))
				w.wait()
			}
			if got, _ := d.got(); !slices.Equal(got, []taskRef{t1, t1}) {
				t.Errorf("ticks at 0, 1s, %v and %v advanced %v, want a round at 0 and at %v", c.quiet, c.every, got, c.every)
			}
		})
	}
}

// TestChecksWatchRunsOneRoundAtATime: a tick while a round is still advancing starts no second round, even once
// the refresh has passed.
func TestChecksWatchRunsOneRoundAtATime(t *testing.T) {
	d := &watchDouble{tasks: []taskRef{t1}, release: make(chan struct{})}
	w := d.watch(time.Nanosecond)
	at := time.Now()
	w.tick(at)
	w.tick(at.Add(time.Hour))
	close(d.release)
	w.wait()
	if got, _ := d.got(); !slices.Equal(got, []taskRef{t1}) {
		t.Fatalf("advanced %v, want one round", got)
	}
}

// TestChecksWatchReportsAndRetriesAFailure: a failed advance is reported and the round goes on to the next task;
// the next round tries it again. A failed listing is reported and advances nothing.
func TestChecksWatchReportsAndRetriesAFailure(t *testing.T) {
	at := time.Now()
	d := &watchDouble{tasks: []taskRef{t1, t2}, fail: map[taskRef]error{t1: errors.New("forge unreachable")}, ended: at}
	w := d.watch(time.Minute)
	w.tick(at)
	w.wait()
	w.tick(at.Add(time.Minute))
	w.wait()
	got, reported := d.got()
	if !slices.Equal(got, []taskRef{t1, t2, t1, t2}) {
		t.Errorf("advanced %v, want both tasks in both rounds", got)
	}
	if !slices.Equal(reported, []string{"app/t-1: forge unreachable", "app/t-1: forge unreachable"}) {
		t.Errorf("reported %q", reported)
	}
	d = &watchDouble{tasks: []taskRef{t1}, listErr: errors.New("store locked")}
	w = d.watch(time.Minute)
	w.tick(at)
	w.wait()
	if got, reported := d.got(); len(got) != 0 || !slices.Equal(reported, []string{"/: store locked"}) {
		t.Errorf("a failed listing advanced %v and reported %q", got, reported)
	}
}

// TestChecksWatchWithNothingWaitingAdvancesNothing: a round over no waiting task advances nothing.
func TestChecksWatchWithNothingWaitingAdvancesNothing(t *testing.T) {
	d := &watchDouble{}
	w := d.watch(0)
	w.tick(time.Now())
	w.wait()
	if got, reported := d.got(); len(got) != 0 || len(reported) != 0 {
		t.Errorf("advanced %v, reported %q", got, reported)
	}
}

// checksItem is the needs_me item of task app/t-1 waiting for its checks.
var checksItem = needsMeItem{Kind: "checks", Repository: "app", TaskID: "t-1", Step: "validate",
	Reason:  "step validate waits for the checks of its published head; the session supervisor reads them again every checks_refresh, never more often than every 30 seconds, and wakes you when they finish",
	Command: "guildhall task start --repo app t-1"}

// TestStatusListsATaskWaitingForChecks: a task whose next.json waits for checks is listed in needs_me, after the
// work that is the Director's, with the task start that reads them again.
func TestStatusListsATaskWaitingForChecks(t *testing.T) {
	d := newRunDirector(t)
	writeNext(t, d.state, "t-1", `{"wait":"checks","step":"validate"}`+"\n")
	st, err := readDirectorStatus(context.Background(), d.store, d.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.NeedsMe) != 1 || st.NeedsMe[0] != checksItem {
		t.Fatalf("needs_me = %+v, want [%+v]", st.NeedsMe, checksItem)
	}
}

// TestChecksWaitingListsOpenTasksWhileTheSessionWorks: the watcher's list holds the open tasks whose next.json
// waits for checks while the newest session is recovering or running, and nothing while it is paused or stopping.
func TestChecksWaitingListsOpenTasksWhileTheSessionWorks(t *testing.T) {
	for _, c := range []struct {
		states []store.DirectorState
		want   []taskRef
	}{
		{nil, []taskRef{t1}},
		{[]store.DirectorState{store.DirectorRunning}, []taskRef{t1}},
		{[]store.DirectorState{store.DirectorRunning, store.DirectorPaused}, nil},
		{[]store.DirectorState{store.DirectorRunning, store.DirectorStopping}, nil},
	} {
		d := newRunDirector(t)
		newTask(t, d.store, d.config, "app", "Another", "t-2")
		newTask(t, d.store, d.config, "app", "Cancelled", "t-3")
		newTask(t, d.store, d.config, "app", "Elsewhere", "t-4")
		if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
			return s.SetTaskState("cancel-t-3", "app", "t-3", store.TaskCancelled)
		}); err != nil {
			t.Fatal(err)
		}
		recordSession(t, d.store, false, c.states...)
		writeNext(t, d.state, "t-1", `{"wait":"checks","step":"validate"}`+"\n")
		writeNext(t, d.state, "t-2", `{"wait":"attempt","step":"plan","attempt":1}`+"\n")
		writeNext(t, d.state, "t-3", `{"wait":"checks","step":"validate"}`+"\n")
		got, err := checksWaiting(d.store, d.state)()
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("after %v: waiting = %v, %v; want %v", c.states, got, err, c.want)
		}
	}
}

// validateCopy is a short-pathed Director copy on the fake-validate workflow whose fake Director says it is ready
// and then waits on its input, over forge f, with task app/t-1 driven by the test to its validate step, where its
// published head's checks are pending.
func validateCopy(t *testing.T, f *forgetest.Fake) (string, runDirector) {
	t.Helper()
	long := directorCopy(t, "[director]\nadapter = \"fake\"\n", "guildhall director ready")
	dir, err := os.MkdirTemp("", "gv")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dir, os.DirFS(long)); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	conf := strings.Replace(string(b), `workflow = "fake-straight"`, `workflow = "fake-validate"`, 1)
	writeFile(t, cfg, strings.Replace(conf, `path = "/srv/app"`, "path = '"+appRepo+"'", 1))
	const remote, head = "git@github.com:example/app.git", "0123456789abcdef0123456789abcdef01234567"
	f.SetBranch(remote, "main", strings.Repeat("a", 40))
	f.AddCommit(appRepo, head)
	f.SetChecks(remote, head, forge.Check{Name: "ci", State: forge.CheckPending})
	ws := gittest.NewWorkspaces()
	ws.SetDefault(remote, head)
	d := runDirector{store: filepath.Join(dir, "director.store"), config: cfg, state: filepath.Join(dir, "state"), workspaces: ws, forge: f}
	t.Cleanup(func() {
		d.stopAll(t)
		_ = os.RemoveAll(dir)
	})
	approveRepo(t, d.store, cfg, "app")
	newTask(t, d.store, cfg, "app", "Sort the task list", "t-1")
	step := func(args ...string) string {
		t.Helper()
		code, out, stderr := d.run(t, args...)
		if code != 0 {
			t.Fatalf("guildhall %q: exit %d, %s", args, code, stderr)
		}
		return strings.TrimSpace(out)
	}
	done := func(name string) {
		d.waitResult(t, name, 1)
		step(append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", name, "--attempt", "1", "--result", d.result(name, 1))...)...)
	}
	answer := func(decision, words string) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), "answer")
		writeFile(t, file, words)
		step("decide", "answer", "--store", d.store, "--repo", "app", "--task", "t-1", "--decision", decision, "--answer-file", file)
		return step(append([]string{"task", "start"}, d.flags("--decision", decision, "t-1")...)...)
	}
	step(append([]string{"task", "start"}, d.flags("t-1")...)...)
	done("plan")
	answer("accept-1", "approved\n")
	done("build")
	if got := answer("sign-off-1", "approved\n"); got != `{"wait":"checks","step":"validate"}` {
		t.Fatalf("after sign-off: %s, want the checks wait", got)
	}
	return dir, d
}

// TestTheSupervisorAdvancesATaskWhenItsChecksFinish: with a session supervisor running and task t-1 waiting at
// validate for pending checks, the checks turning green take the task on to its merge decision with no task start
// from the test, and send the Director the checks-passed message; while they stay pending, the supervisor's reads
// send nothing new. The test lowers the watcher's floor so several rounds read the pending checks quickly.
func TestTheSupervisorAdvancesATaskWhenItsChecksFinish(t *testing.T) {
	f := forgetest.NewFake()
	dir, d := validateCopy(t, f)
	turnLog := filepath.Join(dir, "director-turns.log")
	t.Setenv(fake.TurnLogEnv, turnLog)
	writeFile(t, d.config, strings.Replace(readFileString(t, d.config), "[local]\n", "[local]\nchecks_refresh = \"100ms\"\n", 1))
	pending := len(directorMessages(t, d.store))
	cf := &countingForge{Fake: f}
	done := superviseWith(t, dir, time.Nanosecond, func(app *App) {
		app.Forge, app.Workspaces, app.ChecksWatchFloor = cf, d.workspaces, time.Nanosecond
	})
	for end := time.Now().Add(testenv.Ceiling(time.Minute)); cf.reads.Load() < 3; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the supervisor read the pending checks %d times, want several", cf.reads.Load())
		}
	}
	if got := directorMessages(t, d.store); len(got) != pending {
		t.Fatalf("pending re-reads sent %v", got[pending:])
	}
	if got := turns(t, turnLog); !slices.Equal(got, []string{"start"}) {
		t.Fatalf("pending checks caused runtime turns: %q", got)
	}
	t.Logf("pending checks: forge reads=%d; runtime turns=%v; Director messages unchanged=%v", cf.reads.Load(), turns(t, turnLog), directorMessages(t, d.store))
	if code, out, stderr := d.run(t, "director", "status", "--json", "--copy", dir); code != 0 {
		t.Fatalf("status: %s %s", out, stderr)
	} else {
		t.Logf("pending status feed: %s", out)
	}
	if code, out, stderr := d.run(t, "director", "pause", "--copy", dir); code != 0 {
		t.Fatalf("pause: %s %s", out, stderr)
	}
	// Let a round already in flight finish, then challenge the paused guard with green CI.
	time.Sleep(300 * time.Millisecond)
	reads := cf.reads.Load()
	f.SetChecks("git@github.com:example/app.git", "0123456789abcdef0123456789abcdef01234567", forge.Check{Name: "ci", State: forge.CheckPassed})
	time.Sleep(400 * time.Millisecond)
	if cf.reads.Load() != reads || strings.TrimSpace(readFileString(t, filepath.Join(d.state, "app", "t-1", nextFile))) != `{"wait":"checks","step":"validate"}` {
		t.Fatal("paused supervisor observed or advanced green checks")
	}
	t.Logf("green while paused: forge reads remain %d; task still waits for checks", reads)
	if code, out, stderr := d.run(t, "director", "resume", "--copy", dir); code != 0 {
		t.Fatalf("resume: %s %s", out, stderr)
	}
	next := filepath.Join(d.state, "app", "t-1", nextFile)
	want := `{"wait":"decision","step":"deliver","decision":"deliver-1"}`
	for end := time.Now().Add(testenv.Ceiling(3 * time.Minute)); strings.TrimSpace(readFileString(t, next)) != want; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("next.json = %s, want %s once the checks passed", readFileString(t, next), want)
		}
	}
	t.Logf("autonomous continuation, without task start: next=%s; messages=%v", readFileString(t, next), directorMessages(t, d.store))
	if got := directorMessages(t, d.store); !slices.Contains(got, "app.t-1.checks-passed.validate-1") {
		t.Errorf("Director messages = %v, want checks-passed", got)
	}
	if code, out, stderr := d.run(t, "director", "stop", "--now", "--copy", dir); code != 0 {
		t.Fatalf("director stop --now: exit %d, %s %s", code, out, stderr)
	}
	if code := ended(t, done); code != 0 {
		t.Errorf("director start exited %d, want 0", code)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

// directorMessages lists the ids of every message to the Director in the store at path, in send order.
func directorMessages(t *testing.T, path string) []string {
	t.Helper()
	var ids []string
	_, err := withStore(path, true, func(s *store.Store) (struct{}, error) {
		page, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
		for _, m := range page {
			ids = append(ids, m.ID)
		}
		return struct{}{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// writeNext writes task app/<task>'s next.json under state.
func writeNext(t *testing.T, state, task, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(state, "app", task), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(state, "app", task, nextFile), body)
}

// countingForge is a forgetest.Fake that counts its checks reads.
type countingForge struct {
	*forgetest.Fake
	reads atomic.Int32
}

func (f *countingForge) Checks(ctx context.Context, c forge.Commit) ([]forge.Check, error) {
	f.reads.Add(1)
	return f.Fake.Checks(ctx, c)
}
