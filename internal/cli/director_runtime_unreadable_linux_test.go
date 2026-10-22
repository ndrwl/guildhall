package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// An unreadable persisted containment record must fail closed, not declare a
// successful stop. The directory is disposable data, not a real host cgroup.
func TestDirectorUnreadableRuntimeKeepsStopHold(t *testing.T) {
	d := newRunDirector(t)
	recordSession(t, d.store, true, store.DirectorRunning)
	unit := filepath.Join(t.TempDir(), "recorded-unit")
	if err := os.MkdirAll(filepath.Join(unit, "cgroup.procs"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.RecordDirectorRuntime(1, store.Containment{Kind: store.ContainmentCgroup, Name: unit, Worker: store.ProcessIdentity{PID: 99999999, Token: "disposable-runtime"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := d.run(t, "director", "stop", "--store", d.store, "--state", d.state)
	t.Logf("director stop: exit=%d stdout=%s stderr=%s", code, out, stderr)
	if code != 1 || !strings.Contains(stderr, "run guildhall director stop again") {
		t.Fatalf("unproven sweep: %d %s %s", code, out, stderr)
	}
	current, _ := latestSession(t, d.store)
	t.Logf("persisted session after failed sweep: %+v", current)
	if current.State != store.DirectorStopping || !current.Sweeping {
		t.Fatalf("failed sweep released hold: %+v", current)
	}
	code, out, stderr = d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...)
	t.Logf("task start during failed sweep: exit=%d stdout=%s stderr=%s", code, out, stderr)
	if code != 0 || out != "{\"wait\":\"paused\",\"step\":\"plan\"}\n" {
		t.Fatalf("admission escaped hold: %d %s %s", code, out, stderr)
	}
	if err := os.RemoveAll(unit); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = d.run(t, "director", "stop", "--store", d.store, "--state", d.state)
	t.Logf("director stop after unit disappears: exit=%d stdout=%s stderr=%s", code, out, stderr)
	if code != 0 {
		t.Fatalf("recovery: %d %s %s", code, out, stderr)
	}
	current, _ = latestSession(t, d.store)
	if current.State != store.DirectorStopped || current.Sweeping || current.EndReason != store.EndSuperseded {
		t.Fatalf("recovery did not finish: %+v", current)
	}
}
