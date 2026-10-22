package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// runtimeTreeCommand, run as `guildhall test-runtime-tree` by a fake Director's step, plays a tool the runtime
// started: an artisantest worker with one grandchild, recording both in the inventory directory runtimeTreeEnv
// names, that runs until its release file exists. It is the test binary's, never the CLI's.
const (
	runtimeTreeCommand = "test-runtime-tree"
	runtimeTreeEnv     = "GUILDHALL_TEST_RUNTIME_TREE"
)

// runRuntimeTree runs runtimeTreeCommand and returns its exit status.
func runRuntimeTree() int {
	inv := os.Getenv(runtimeTreeEnv)
	if inv == "" {
		_, _ = fmt.Fprintln(os.Stderr, runtimeTreeCommand+": "+runtimeTreeEnv+" is not set")
		return 2
	}
	if err := artisantest.Command(inv, artisantest.Worker{Grandchildren: 1, IgnoreInterrupt: true, Lifetime: 10 * time.Minute}).Run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, runtimeTreeCommand+":", err)
		return 1
	}
	return 0
}

// runtimeTree waits until the inventory inv records the worker and its grandchild and returns them, killing whatever
// it recorded and releasing the worker when the test ends.
func runtimeTree(t *testing.T, inv string) []artisan.Identity {
	t.Helper()
	var tree []artisan.Identity
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(inv, "release"), nil, 0o600)
		entries, _ := artisantest.Inventory(inv)
		for _, e := range entries {
			_ = artisan.Kill(e.Identity)
		}
	})
	for end := time.Now().Add(testenv.Ceiling(2 * time.Minute)); len(tree) < 2; time.Sleep(20 * time.Millisecond) {
		entries, err := artisantest.Inventory(inv)
		if err != nil {
			t.Fatal(err)
		}
		tree = tree[:0]
		for _, e := range entries {
			tree = append(tree, e.Identity)
		}
		if time.Now().After(end) {
			t.Fatalf("the runtime's tree never started: %+v", entries)
		}
	}
	return tree
}

// waitGone waits until every identity in ids is proven gone.
func waitGone(t *testing.T, what string, ids ...artisan.Identity) {
	t.Helper()
	for end := time.Now().Add(testenv.Ceiling(2 * time.Minute)); ; time.Sleep(20 * time.Millisecond) {
		alive := 0
		for _, id := range ids {
			if lv, _ := artisan.Probe(id); lv != artisan.Gone {
				alive++
			}
		}
		if alive == 0 {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("%d of %v still run %s", alive, ids, what)
		}
	}
}

// assertGone checks every identity in ids is proven gone now.
func assertGone(t *testing.T, what string, ids ...artisan.Identity) {
	t.Helper()
	for _, id := range ids {
		if lv, err := artisan.Probe(id); lv != artisan.Gone {
			t.Errorf("process %d is %v (%v) %s", id.PID, lv, err, what)
		}
	}
}

// stopCopy runs director stop over dir in this process and returns its exit status, the session it printed and its
// standard error.
func stopCopy(t *testing.T, dir string) (int, store.DirectorSession, string) {
	t.Helper()
	app, stdout, stderr := supervisorApp(nil, time.Hour)
	code := app.Run([]string{"director", "stop", "--copy", dir})
	var printed store.DirectorSession
	if code == 0 {
		if err := json.Unmarshal(stdout.Bytes(), &printed); err != nil {
			t.Fatalf("director stop printed %q: %v", stdout, err)
		}
	}
	return code, printed, stderr.String()
}

// TestDirectorStopEndsAGoneSupervisorsRuntime kills a real `director start` whose fake Director has said it is
// ready and started a tool with a child of its own (docs/director.md#the-runtimes-record). The session records the
// runtime's unit. On Unix the tool and its child outlive the supervisor; director start is refused while they run,
// naming them and director stop, and director stop ends them before it records the session stopped as superseded.
// On Windows the Job Object ends them with the supervisor, and director stop records the session superseded.
func TestDirectorStopEndsAGoneSupervisorsRuntime(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "guildhall director ready", "guildhall "+runtimeTreeCommand)
	inv := t.TempDir()
	t.Setenv(runtimeTreeEnv, inv)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stdin, typing, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = typing.Close() })
	// Files, not pipes, so that waiting for the killed supervisor does not wait for the runtime's processes too.
	out, err := os.Create(filepath.Join(t.TempDir(), "supervisor.out"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	sup := exec.Command(exe, "director", "start", "--copy", dir)
	sup.Stdin, sup.Stdout, sup.Stderr = stdin, out, out
	if err := sup.Start(); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	t.Cleanup(func() { _ = sup.Process.Kill(); _ = sup.Wait() })
	tree := runtimeTree(t, inv)
	for end := time.Now().Add(testenv.Ceiling(2 * time.Minute)); ; time.Sleep(20 * time.Millisecond) {
		if got := sessions(t, dir); len(got) == 1 && got[0].State == store.DirectorRunning {
			break
		}
		if time.Now().After(end) {
			b, _ := os.ReadFile(out.Name())
			t.Fatalf("the session never became ready: %+v\n%s", sessions(t, dir), b)
		}
	}
	d := sessions(t, dir)[0]
	t.Logf("running session with recorded runtime: %+v", d)
	rt := artisan.Identity(d.Runtime.Worker)
	if d.Runtime.Kind == "" || rt.PID == 0 {
		t.Errorf("session 1 records no runtime unit: %+v", d)
	} else {
		t.Cleanup(func() {
			_ = artisan.Kill(rt)
			_, _ = artisan.Sweep(artisan.Containment{Kind: d.Runtime.Kind, Name: d.Runtime.Name, Reason: d.Runtime.Reason}, rt)
		})
	}
	if err := sup.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = sup.Wait()
	// No later step of the test runs a tree.
	_ = os.Unsetenv(runtimeTreeEnv)

	if runtime.GOOS == "windows" {
		waitGone(t, "after the supervisor's Job Object closed", tree...)
	} else {
		for _, id := range tree {
			if lv, err := artisan.Probe(id); lv != artisan.Alive {
				t.Fatalf("process %d of the runtime's tree is %v (%v) right after the supervisor died; want it orphaned", id.PID, lv, err)
			}
		}
		app, stdout, stderr := supervisorApp(nil, time.Hour)
		code := app.Run([]string{"director", "start", "--copy", dir})
		t.Logf("director start beside orphan tree %v: exit=%d stderr=%s", tree, code, stderr)
		if msg := stderr.String(); code != 1 || !strings.Contains(msg, "director: live") || !strings.Contains(msg, strconv.Itoa(tree[0].PID)) ||
			!strings.Contains(msg, "guildhall director stop") {
			t.Errorf("director start beside the orphaned runtime: exit %d, stdout %s, stderr %s; want director: live naming process %d and director stop",
				code, stdout, msg, tree[0].PID)
		}
		if got := sessions(t, dir); len(got) != 1 {
			t.Fatalf("the refused start recorded sessions %+v", got)
		}
	}
	code, printed, stderr := stopCopy(t, dir)
	t.Logf("director stop: exit=%d session=%+v stderr=%s", code, printed, stderr)
	if code != 0 || printed.State != store.DirectorStopped || printed.EndReason != store.EndSuperseded || printed.Sweeping {
		t.Fatalf("director stop: exit %d, %+v, stderr %s; want session 1 stopped as superseded", code, printed, stderr)
	}
	assertGone(t, "after director stop", tree...)
	t.Logf("verified orphan tree gone: %v", tree)
	if rt.PID != 0 {
		assertGone(t, "after director stop", rt)
	}
}

// TestDirectorStartAndStopOverARecordedRuntimeUnit: over a session whose supervisor is gone and whose recorded
// runtime unit, here one this test holds, still runs a process tree, director start is refused naming the
// survivors and director stop, recording nothing, and director stop ends the tree before it records the session
// stopped as superseded; director start is then admitted past it.
func TestDirectorStartAndStopOverARecordedRuntimeUnit(t *testing.T) {
	t.Run("running", func(t *testing.T) { checkRecordedRuntimeRecovery(t, false) })
	t.Run("previously stopped", func(t *testing.T) { checkRecordedRuntimeRecovery(t, true) })
}

func checkRecordedRuntimeRecovery(t *testing.T, stopped bool) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n")
	path := filepath.Join(dir, "director.store")
	recordSession(t, path, true, store.DirectorRunning)
	u, err := artisan.NewSessionUnit(fmt.Sprintf("test-runtime-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	inv := t.TempDir()
	worker, err := u.Start(artisantest.Command(inv, artisantest.Worker{Grandchildren: 1, IgnoreInterrupt: true, Lifetime: 10 * time.Minute}))
	if err != nil {
		t.Fatal(err)
	}
	tree := runtimeTree(t, inv)
	c := u.Containment()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.RecordDirectorRuntime(1, store.Containment{Kind: c.Kind, Name: c.Name, Reason: c.Reason, Worker: store.ProcessIdentity(worker)})
	}); err != nil {
		t.Fatal(err)
	}
	expected := store.DirectorRunning
	if stopped {
		if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
			d, err := s.LatestDirectorSession()
			if err != nil {
				return store.Receipt{}, err
			}
			return s.EndDirectorSession(1, store.EndSuperseded, d.StateSequence)
		}); err != nil {
			t.Fatal(err)
		}
		expected = store.DirectorStopped
	}
	app, stdout, stderr := supervisorApp(nil, time.Hour)
	code := app.Run([]string{"director", "start", "--copy", dir})
	if msg := stderr.String(); code != 1 || !strings.Contains(msg, "director: live") || !strings.Contains(msg, strconv.Itoa(worker.PID)) ||
		!strings.Contains(msg, "guildhall director stop") {
		t.Errorf("director start over the live unit: exit %d, stdout %s, stderr %s; want director: live naming process %d and director stop",
			code, stdout, msg, worker.PID)
	}
	if got := sessions(t, dir); len(got) != 1 || got[0].State != expected {
		t.Fatalf("the refused start changed the sessions: %+v", got)
	}
	if !stopped {
		if err := stopSession(path, 1); err != nil {
			t.Fatal(err)
		}
		if d := sessions(t, dir)[0]; d.State != store.DirectorStopping || !d.Sweeping {
			t.Fatalf("unproven cleanup released lifecycle: %+v", d)
		}
	}
	code, printed, errs := stopCopy(t, dir)
	if code != 0 || printed.State != store.DirectorStopped || printed.EndReason != store.EndSuperseded || printed.Sweeping {
		t.Fatalf("director stop: exit %d, %+v, stderr %s; want session 1 stopped as superseded", code, printed, errs)
	}
	assertGone(t, "after director stop", tree...)
	if newest, err := withStore(path, true, checkSupersede); err != nil || newest != 1 {
		t.Errorf("director start's check after the stop = %d, %v; want it admitted after session 1", newest, err)
	}
}
