package reconcile_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const (
	beat     = 50 * time.Millisecond
	stale    = 500 * time.Millisecond
	deadline = 20 * time.Second
	// launcherEnv makes the test binary a launcher process (mainIfLauncher).
	launcherEnv = "GUILDHALL_RECONCILE_LAUNCHER"
)

var key = artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}

func TestMain(m *testing.M) {
	store.SkipSyncForTests()
	artisantest.MainIfWorker()
	mainIfLauncher()
	os.Exit(m.Run())
}

// launcherProbe is the owner probe a launcher passes to TakeOwnership: the
// store's conservative policy over Artisan's native probe.
var launcherProbe = store.ProbeLauncher(func(id store.ProcessIdentity) (store.Liveness, error) {
	lv, err := artisan.Probe(artisan.Identity(id))
	return store.Liveness(lv.String()), err
})

func owner(launcher artisan.Identity) *store.Ownership {
	return &store.Ownership{Repository: key.Repository, TaskID: key.TaskID, Step: key.Step, Attempt: key.Attempt, LauncherIdentity: store.ProcessIdentity(launcher), State: store.OwnerRunning}
}

func classify(dir string, o *store.Ownership) reconcile.Classification {
	return reconcile.Classify(context.Background(), reconcile.Attempt{Dir: dir, Files: artisantest.NewFiles(dir), Owner: o}, reconcile.Options{StaleAfter: stale})
}

func want(t *testing.T, c reconcile.Classification, s reconcile.State) {
	t.Helper()
	if c.State != s {
		t.Fatalf("state %q (%s), want %q: %+v", c.State, c.Reason, s, c)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("classification: %s", b)
}

// await classifies until the state is s.
func await(t *testing.T, dir string, o *store.Ownership, s reconcile.State) reconcile.Classification {
	t.Helper()
	for end := time.Now().Add(deadline); ; time.Sleep(beat) {
		c := classify(dir, o)
		if c.State == s {
			want(t, c, s)
			return c
		}
		if time.Now().After(end) {
			t.Fatalf("waited %v for %q; last %q (%s): %+v", deadline, s, c.State, c.Reason, c)
		}
	}
}

func self(t *testing.T) artisan.Identity {
	t.Helper()
	id, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeHeartbeat(t *testing.T, dir string, h artisan.Heartbeat) {
	t.Helper()
	if err := artisan.WriteHeartbeat(dir, h); err != nil {
		t.Fatal(err)
	}
}

// shortDir is a state directory short enough for an AF_UNIX path.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "rc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// started runs a worker tree in a fresh native unit, returning the unit,
// its recovery description and the inventory directory.
func started(t *testing.T, w artisantest.Worker) (artisan.Unit, store.Containment, string) {
	t.Helper()
	u, err := artisan.NewUnit("reconcile-" + rand.Text()[:8])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	inv := t.TempDir()
	worker, err := u.Start(artisantest.Command(inv, w))
	if err != nil {
		t.Fatal(err)
	}
	c := u.Containment()
	return u, store.Containment{Kind: c.Kind, Name: c.Name, Reason: c.Reason, Worker: store.ProcessIdentity(worker)}, inv
}

// exitedIdentity is the identity of a process that has exited.
func exitedIdentity(t *testing.T) artisan.Identity {
	t.Helper()
	inv := t.TempDir()
	w := artisantest.Worker{Lifetime: time.Millisecond}
	cmd := artisantest.Command(inv, w)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	entries, err := artisantest.Inventory(inv)
	if err != nil || len(entries) != 1 {
		t.Fatalf("inventory %v, %v", entries, err)
	}
	return entries[0].Identity
}

// assertGone checks that every process in the inventory directory has exited.
func assertGone(t *testing.T, inv string) {
	t.Helper()
	entries, err := artisantest.Inventory(inv)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		for end := time.Now().Add(deadline); ; time.Sleep(beat) {
			if lv, _ := artisan.Probe(e.Identity); lv == artisan.Gone {
				break
			}
			if time.Now().After(end) {
				t.Fatalf("owned %s %v still alive", e.Role, e.Identity)
			}
		}
	}
}

// releaseAll uses the worker protocol rather than identity-bound signaling,
// which is unavailable on macOS. For crash cleanup, stop and wait for the
// launcher first so it cannot start more workers after the release.
func releaseAll(t *testing.T, inv string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(inv, "release"), nil, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Errorf("release workers: %v", err)
	}
	assertGone(t, inv)
}

// TestClassifyFiles pins the scratch rules that the files decide first (L22).
func TestClassifyFiles(t *testing.T) {
	live := owner(self(t))
	t.Run("L22 the outcome decides before the owner", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, artisan.OutcomeFile), `{"schema":"guildhall.attempt-outcome/1","state":"failed","detail":"worker exited without a result"}`)
		c := classify(dir, live)
		want(t, c, reconcile.Failed)
		if c.Outcome == nil || c.Outcome.Detail != "worker exited without a result" || c.Launcher != "" {
			t.Errorf("classification %+v", c)
		}
	})
	t.Run("L22 the cancel marker decides before a result", func(t *testing.T) {
		dir := t.TempDir()
		f := artisantest.NewFiles(dir)
		write(t, f.Result, artisantest.ResultBody)
		write(t, f.Marker, "")
		want(t, classify(dir, live), reconcile.Cancelled)
	})
	t.Run("L22 a valid result is finished", func(t *testing.T) {
		dir := t.TempDir()
		write(t, artisantest.NewFiles(dir).Result, artisantest.ResultBody)
		want(t, classify(dir, live), reconcile.Finished)
	})
	t.Run("no ownership record is absent", func(t *testing.T) {
		want(t, classify(t.TempDir(), nil), reconcile.Absent)
	})
	// A Director stage runs no launcher, so its attempt has no ownership record and is the Director's (F46).
	t.Run("a handed attempt with no ownership record is handed", func(t *testing.T) {
		dir := t.TempDir()
		c := reconcile.Classify(context.Background(), reconcile.Attempt{Dir: dir, Files: artisantest.NewFiles(dir), Handed: true}, reconcile.Options{StaleAfter: stale})
		want(t, c, reconcile.Handed)
		if c.Reason != "handed to the Director" {
			t.Errorf("reason %q, want handed to the Director", c.Reason)
		}
	})
	t.Run("a handed attempt's valid result is finished", func(t *testing.T) {
		dir := t.TempDir()
		write(t, artisantest.NewFiles(dir).Result, artisantest.ResultBody)
		want(t, reconcile.Classify(context.Background(), reconcile.Attempt{Dir: dir, Files: artisantest.NewFiles(dir), Handed: true}, reconcile.Options{StaleAfter: stale}), reconcile.Finished)
	})
	// An outcome that cannot be read is unknown and never falls through to
	// the marker, result or owner; a newer schema says so, apart from damage.
	for name, c := range map[string]struct{ body, reason string }{
		"a newer outcome":          {`{"schema":"guildhall.attempt-outcome/2","state":"finished","detail":""}`, "newer outcome: "},
		"a damaged outcome":        {`{"schema":"guildhall.attempt-outcome/1","state":"finished","detail":"","extra":true}`, "unreadable outcome: "},
		"an unknown outcome state": {`{"schema":"guildhall.attempt-outcome/1","state":"running","detail":""}`, "unreadable outcome: "},
	} {
		t.Run(name+" is unknown", func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, artisan.OutcomeFile), c.body)
			write(t, artisantest.NewFiles(dir).Result, artisantest.ResultBody)
			got := classify(dir, live)
			want(t, got, reconcile.Unknown)
			if !strings.HasPrefix(got.Reason, c.reason) || got.Outcome != nil || got.Launcher != "" {
				t.Errorf("classification %+v, want reason %q and no outcome or owner check", got, c.reason)
			}
		})
	}
}

type blockedMarkerFiles struct {
	artisantest.Files
	entered chan struct{}
	release chan struct{}
}

func (f *blockedMarkerFiles) Mark() error {
	close(f.entered)
	<-f.release
	return f.Files.Mark()
}

func TestClassifyResultMarkerRace(t *testing.T) {
	for _, op := range []string{"cancel", "stop"} {
		t.Run(op, func(t *testing.T) {
			dir, inv := t.TempDir(), t.TempDir()
			files := &blockedMarkerFiles{Files: artisantest.NewFiles(dir), entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(files.release) }) }
			u, err := artisan.NewUnit("reconcile-" + rand.Text()[:8])
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = u.Close() })
			l, err := artisan.NewLauncher(artisan.LauncherConfig{Dir: dir, Unit: u, Files: files, Key: key})
			if err != nil {
				t.Fatal(err)
			}
			runDone := make(chan struct{})
			go func() {
				defer close(runDone)
				_, _ = l.Run(artisantest.Command(inv, artisantest.Worker{CancelMarker: files.Marker, Lifetime: deadline}), nil)
			}()
			actionDone := make(chan error, 1)
			t.Cleanup(func() {
				release()
				_, _ = l.Stop()
				<-runDone
				if err := <-actionDone; err != nil {
					t.Errorf("%s: %v", op, err)
				}
			})
			go func() {
				var err error
				if op == "cancel" {
					_, err = l.Cancel()
				} else {
					_, err = l.Stop()
				}
				actionDone <- err
			}()
			select {
			case <-files.entered:
			case <-time.After(deadline):
				t.Fatal("launcher did not reach marker publication")
			}
			write(t, files.Result, artisantest.ResultBody)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			c := reconcile.Classify(ctx, reconcile.Attempt{Dir: dir, Files: files.Files}, reconcile.Options{})
			want(t, c, reconcile.Unknown)
			if !strings.Contains(c.Reason, "attempt lock: context deadline exceeded") {
				t.Fatalf("lock contention reason: %s", c.Reason)
			}
			release()
			want(t, classify(dir, nil), reconcile.Cancelled)
		})
	}
}

func TestClassifyLockBound(t *testing.T) {
	dir := t.TempDir()
	release, err := artisan.LockAttempt(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	c := classify(dir, nil)
	want(t, c, reconcile.Unknown)
	if !strings.Contains(c.Reason, "attempt lock: context deadline exceeded") {
		t.Fatalf("lock contention reason: %s", c.Reason)
	}
}

// TestClassifyOwner pins the scratch owner rules against live processes.
func TestClassifyOwner(t *testing.T) {
	t.Run("L13 L14 a launcher proven exited with an emptied unit is exited", func(t *testing.T) {
		u, c, inv := started(t, artisantest.Worker{Grandchildren: 1})
		if err := u.Kill(); err != nil {
			t.Fatal(err)
		}
		assertGone(t, inv)
		o := owner(exitedIdentity(t))
		o.Containment = c
		cl := await(t, t.TempDir(), o, reconcile.Exited)
		if cl.Launcher != "gone" || len(cl.Survivors) != 0 {
			t.Errorf("classification %+v", cl)
		}
	})
	t.Run("L13 a launcher proven exited with contained survivors is unknown", func(t *testing.T) {
		_, c, inv := started(t, artisantest.Worker{Grandchildren: 1})
		for end := time.Now().Add(deadline); ; time.Sleep(beat) {
			if e, _ := artisantest.Inventory(inv); len(e) == 2 {
				break
			}
			if time.Now().After(end) {
				t.Fatal("worker tree did not start")
			}
		}
		o := owner(exitedIdentity(t))
		o.Containment = c
		cl := classify(t.TempDir(), o)
		want(t, cl, reconcile.Unknown)
		if len(cl.Survivors) != 2 {
			t.Errorf("survivors %v, want the worker and grandchild", cl.Survivors)
		}
		releaseAll(t, inv)
	})
	t.Run("L19 a live launcher whose endpoint is broken is unknown, not exited", func(t *testing.T) {
		dir := t.TempDir()
		o := owner(self(t))
		o.Endpoint = filepath.Join(shortDir(t), "closed.sock")
		now := time.Now()
		writeHeartbeat(t, dir, artisan.Heartbeat{Repository: key.Repository, TaskID: key.TaskID, Step: key.Step, Attempt: 1, Sequence: 1, Time: now, LastOutputAt: now, WorkerState: "running"})
		c := classify(dir, o)
		want(t, c, reconcile.Unknown)
		if c.Launcher != "alive" || c.Endpoint == "answers" || c.Endpoint == "" || c.Heartbeat == nil {
			t.Errorf("classification %+v", c)
		}
	})
	t.Run("L20 a reused PID proves the recorded launcher exited", func(t *testing.T) {
		id := self(t)
		id.Token += "-earlier"
		want(t, classify(t.TempDir(), owner(id)), reconcile.Exited)
	})
	t.Run("L21 a leftover endpoint file does not keep an exited owner alive", func(t *testing.T) {
		o := owner(exitedIdentity(t))
		o.Endpoint = filepath.Join(t.TempDir(), "left.sock")
		write(t, o.Endpoint, "")
		want(t, classify(t.TempDir(), o), reconcile.Exited)
		if _, err := os.Stat(o.Endpoint); err != nil {
			t.Errorf("leftover endpoint file: %v", err)
		}
	})
}

// TestClassifyWithoutAnAttemptDirectory: an owner recorded before its
// launcher made the attempt directory is classified from the owner checks
// alone, which say so, and classifying creates nothing (F20).
func TestClassifyWithoutAnAttemptDirectory(t *testing.T) {
	_, c, inv := started(t, artisantest.Worker{Grandchildren: 1})
	tree := func() {
		for end := time.Now().Add(deadline); ; time.Sleep(beat) {
			if e, _ := artisantest.Inventory(inv); len(e) == 2 {
				return
			}
			if time.Now().After(end) {
				t.Fatal("worker tree did not start")
			}
		}
	}
	tree()
	defer releaseAll(t, inv)
	for _, tc := range []struct {
		name        string
		launcher    artisan.Identity
		containment store.Containment
		state       reconcile.State
	}{
		{"gone without containment", exitedIdentity(t), store.Containment{}, reconcile.Exited},
		{"gone with contained survivors", exitedIdentity(t), c, reconcile.Unknown},
		{"alive with no endpoint", self(t), store.Containment{}, reconcile.Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "never-made")
			o := owner(tc.launcher)
			o.Containment = tc.containment
			cl := classify(dir, o)
			want(t, cl, tc.state)
			if !strings.Contains(cl.Reason, "no attempt directory") || cl.Launcher == "" {
				t.Errorf("classification %+v; want the owner checks and no attempt directory in its reason", cl)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("classifying made the attempt directory: %v", err)
			}
		})
	}
}

// answering is an endpoint handler whose launcher is running.
type answering struct{}

func (answering) Status() (artisan.EndpointState, error) {
	return artisan.EndpointState{Phase: "running"}, nil
}
func (answering) Stop() (artisan.EndpointState, error)   { return answering{}.Status() }
func (answering) Cancel() (artisan.EndpointState, error) { return answering{}.Status() }

// TestClassifyRefusedHeartbeat pins that a heartbeat ReadHeartbeat refuses is
// reported, never read as no heartbeat: beside a live launcher whose endpoint
// answers it is unknown, not running, with the reader's error as the reason.
func TestClassifyRefusedHeartbeat(t *testing.T) {
	for _, tc := range []struct{ name, body, reason string }{
		{"a newer schema", `{"schema":"guildhall.attempt-heartbeat/2"}`, "guildhall.attempt-heartbeat/2"},
		{"a malformed file", `{"schema":"guildhall.attempt-heartbeat/1",`, "invalid heartbeat"},
	} {
		t.Run(tc.name+" beside a live launcher is unknown", func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, artisan.HeartbeatFile), tc.body)
			me := self(t)
			path, err := artisan.EndpointPath(shortDir(t), key)
			if err != nil {
				t.Fatal(err)
			}
			ep, err := artisan.Listen(path, key, me, func() (artisan.Identity, error) { return me, nil }, answering{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ep.Close() })
			o := owner(me)
			o.Endpoint = path
			c := classify(dir, o)
			want(t, c, reconcile.Unknown)
			if c.Launcher != "alive" || c.Endpoint != "answers" || c.Heartbeat != nil || !strings.Contains(c.Reason, tc.reason) {
				t.Errorf("classification %+v, want the refusal naming %q", c, tc.reason)
			}
		})
	}
}

// live is a launcher run in this process with its endpoint bound.
type live struct {
	dir, inv string
	owner    *store.Ownership
	files    artisantest.Files
}

func launch(t *testing.T, lastOutput func() time.Time, prepare func(dir string)) *live {
	t.Helper()
	c := &live{dir: t.TempDir(), inv: t.TempDir()}
	c.files = artisantest.NewFiles(c.dir)
	if prepare != nil {
		prepare(c.dir)
	}
	u, err := artisan.NewUnit("reconcile-" + rand.Text()[:8])
	if err != nil {
		t.Fatal(err)
	}
	l, err := artisan.NewLauncher(artisan.LauncherConfig{Dir: c.dir, Unit: u, Files: c.files, Key: key, HeartbeatInterval: beat, LastOutput: lastOutput})
	if err != nil {
		t.Fatal(err)
	}
	me := self(t)
	path, err := artisan.EndpointPath(shortDir(t), key)
	if err != nil {
		t.Fatal(err)
	}
	ep, err := artisan.Listen(path, key, me, func() (artisan.Identity, error) { return me, nil }, l)
	if err != nil {
		t.Fatal(err)
	}
	c.owner = owner(me)
	c.owner.Endpoint = path
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = l.Run(artisantest.Command(c.inv, artisantest.Worker{CancelMarker: c.files.Marker, Lifetime: deadline}), nil)
	}()
	t.Cleanup(func() {
		_, _ = l.Stop()
		<-done
		_ = ep.Close()
		_ = u.Close()
		assertGone(t, c.inv)
	})
	return c
}

// beating waits until the launcher runs and has written a heartbeat, so a
// later wedge or suspension stops beats rather than preventing the first.
func (c *live) beating(t *testing.T) {
	t.Helper()
	for cl := await(t, c.dir, c.owner, reconcile.Running); cl.Heartbeat == nil; cl = await(t, c.dir, c.owner, reconcile.Running) {
		time.Sleep(beat)
	}
}

// ownedStore is a real store holding the attempt with c's owner.
func ownedStore(t *testing.T, c *live) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "guildhall.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := s.CreateTask("task", store.Task{Repository: key.Repository, ID: key.TaskID, Title: "t", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}); err != nil {
		t.Fatal(err)
	}
	o := *c.owner
	o.Attempt, o.State, o.Endpoint = 0, "", ""
	if _, err := s.StartOwnedAttempt("own", o); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestHeartbeat is the four accepted heartbeat cases.
func TestHeartbeat(t *testing.T) {
	t.Run("a wedged launcher is stuck while its identity and endpoint live", func(t *testing.T) {
		var hold atomic.Bool
		c := launch(t, func() time.Time {
			for hold.Load() {
				time.Sleep(10 * time.Millisecond)
			}
			return time.Time{}
		}, nil)
		t.Cleanup(func() { hold.Store(false) })
		c.beating(t)
		hold.Store(true)
		cl := await(t, c.dir, c.owner, reconcile.Stuck)
		if cl.Launcher != "alive" || cl.Heartbeat == nil || cl.HeartbeatAge <= stale {
			t.Errorf("stuck without its evidence: %+v", cl)
		}
		hold.Store(false)
		await(t, c.dir, c.owner, reconcile.Running)
	})
	t.Run("a stalled worker's fresh heartbeat is not stuck", func(t *testing.T) {
		quiet := time.Now().Add(-time.Hour).UTC().Round(time.Millisecond)
		c := launch(t, func() time.Time { return quiet }, func(dir string) {
			writeHeartbeat(t, dir, artisan.Heartbeat{Repository: key.Repository, TaskID: key.TaskID, Step: key.Step, Attempt: 1, Sequence: 41, Time: quiet, LastOutputAt: quiet, WorkerState: "running"})
		})
		await(t, c.dir, c.owner, reconcile.Running)
		time.Sleep(stale)
		cl := classify(c.dir, c.owner)
		want(t, cl, reconcile.Running)
		h := *cl.Heartbeat
		if !h.LastOutputAt.Equal(quiet) || time.Since(h.Time) > stale || h.Sequence <= 41 || h.Key() != key || h.WorkerState != "running" {
			t.Fatalf("heartbeat %+v, want a fresh beat after sequence 41 with last output %v", h, quiet)
		}
	})
	t.Run("a stale heartbeat while alive is stuck, refuses a relaunch and recovers", func(t *testing.T) {
		var hold atomic.Bool
		c := launch(t, func() time.Time {
			for hold.Load() {
				time.Sleep(10 * time.Millisecond)
			}
			return time.Time{}
		}, nil)
		c.beating(t)
		s := ownedStore(t, c)
		t.Cleanup(func() { hold.Store(false) })
		hold.Store(true)
		await(t, c.dir, c.owner, reconcile.Stuck)
		next := *owner(artisan.Identity{PID: 1, Token: "relauncher"})
		next.State = ""
		if _, err := s.TakeOwnership("relaunch", next, launcherProbe); !errors.Is(err, store.ErrOwnerLive) {
			t.Errorf("take-over of a stuck attempt = %v, want ErrOwnerLive", err)
		} else {
			t.Logf("take-over of stuck attempt refused: %v", err)
		}
		hold.Store(false)
		await(t, c.dir, c.owner, reconcile.Running)
	})
	t.Run("a crashed launcher's frozen heartbeat is evidence; the identity decides", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), launcherEnv+"="+dir)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		inv := filepath.Join(dir, "inventory")
		var rec launcherRecord
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			if cmd.ProcessState == nil {
				_ = cmd.Wait()
			}
			releaseAll(t, inv)
		})
		for end := time.Now().Add(deadline); ; time.Sleep(beat) {
			b, err := os.ReadFile(filepath.Join(dir, "launcher.json"))
			e, _ := artisantest.Inventory(inv)
			_, herr := artisan.ReadHeartbeat(dir)
			if err == nil && json.Unmarshal(b, &rec) == nil && len(e) == 2 && herr == nil {
				break
			}
			if time.Now().After(end) {
				t.Fatal("launcher process did not start its worker tree")
			}
		}
		o := owner(rec.Launcher)
		o.Containment = store.Containment{Kind: rec.Containment.Kind, Name: rec.Containment.Name, Reason: rec.Containment.Reason}
		for _, e := range must(artisantest.Inventory(inv)) {
			if e.Role == "worker" {
				o.Containment.Worker = store.ProcessIdentity(e.Identity)
			}
		}
		if cl := classify(dir, o); cl.Launcher != "alive" || cl.State == reconcile.Exited {
			t.Fatalf("a live launcher process classified %+v", cl)
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
		frozen := must(artisan.ReadHeartbeat(dir))
		cl := classify(dir, o)
		t.Logf("%s launcher killed: %s (%s)", o.Containment.Kind, cl.State, cl.Reason)
		if len(cl.Survivors) > 0 && cl.State != reconcile.Unknown {
			t.Errorf("survivors %v classified %q, want unknown", cl.Survivors, cl.State)
		}
		releaseAll(t, inv)
		cl = await(t, dir, o, reconcile.Exited)
		if cl.Launcher != "gone" || cl.Heartbeat == nil || cl.Heartbeat.Sequence != frozen.Sequence {
			t.Errorf("exited without the frozen heartbeat %+v as evidence: %+v", frozen, cl)
		}
		if rec.Containment.Kind == artisan.KindCgroup {
			_ = os.Remove(rec.Containment.Name)
		}
	})
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// launcherRecord is what the launcher process reports about itself.
type launcherRecord struct {
	Launcher    artisan.Identity    `json:"launcher"`
	Containment artisan.Containment `json:"containment"`
}

// mainIfLauncher runs a launcher over a worker tree in the attempt directory
// named by launcherEnv, until it is killed.
func mainIfLauncher() {
	dir, ok := os.LookupEnv(launcherEnv)
	if !ok {
		return
	}
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "reconcile launcher:", err)
		os.Exit(2)
	}
	inv := filepath.Join(dir, "inventory")
	if err := os.Mkdir(inv, 0o700); err != nil {
		fail(err)
	}
	u, err := artisan.NewUnit("reconcile-" + rand.Text()[:8])
	if err != nil {
		fail(err)
	}
	l, err := artisan.NewLauncher(artisan.LauncherConfig{Dir: dir, Unit: u, Files: artisantest.NewFiles(dir), Key: key, HeartbeatInterval: beat})
	if err != nil {
		fail(err)
	}
	me, err := artisan.Self()
	if err != nil {
		fail(err)
	}
	b, _ := json.Marshal(launcherRecord{Launcher: me, Containment: u.Containment()})
	if err := os.WriteFile(filepath.Join(dir, "launcher.json"), b, 0o600); err != nil {
		fail(err)
	}
	_, err = l.Run(artisantest.Command(inv, artisantest.Worker{Grandchildren: 1, Lifetime: deadline}), nil)
	fail(errors.Join(err, errors.New("launcher returned before it was killed")))
}
