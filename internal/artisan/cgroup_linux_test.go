package artisan_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/testenv"
	"golang.org/x/sys/unix"
)

func TestCgroupKillEmpties(t *testing.T) {
	u, reason, ok, err := artisan.NewCgroupUnit("kill-" + t.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("cgroup backend not available: %s", reason)
	}
	t.Cleanup(func() { _ = u.Close() })
	dir := u.Containment().Name
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("cgroup %s: %v", dir, err)
	}
	inventory := t.TempDir()
	worker, err := u.Start(artisantest.Command(inventory, artisantest.Worker{Grandchildren: 1, SetsidGrandchild: true}))
	if err != nil {
		t.Fatal(err)
	}
	var gc artisantest.Entry
	for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); gc.PID == 0 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		entries, err := artisantest.Inventory(inventory)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Role == "grandchild-setsid" {
				gc = e
			}
		}
	}
	if gc.PID == 0 {
		t.Fatalf("grandchild did not acknowledge setsid: %+v", gc)
	}
	if pgid, err := unix.Getpgid(gc.PID); err != nil || pgid != gc.PID || pgid == worker.PID {
		t.Fatalf("grandchild group = %d, %v; worker = %v", pgid, err, worker)
	}
	if live, err := artisan.Probe(gc.Identity); err != nil || live != artisan.Alive {
		t.Fatalf("grandchild identity = %v, %v", live, err)
	}
	m, err := u.Members()
	if err != nil || !slices.Contains(m, worker) || !slices.Contains(m, gc.Identity) {
		t.Fatalf("Members() = %v, %v; want worker %v and grandchild %v", m, err, worker, gc.Identity)
	}
	t.Logf("containment=%+v worker=%+v setsid-grandchild=%+v members=%+v", u.Containment(), worker, gc, m)
	if err := u.Kill(); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if m, err = u.Members(); err == nil && len(m) == 0 {
			// Members excludes zombies; cgroup.procs can retain an exiting
			// task briefly after its identity is already reported gone.
			if b, readErr := os.ReadFile(filepath.Join(dir, "cgroup.procs")); readErr == nil && len(strings.TrimSpace(string(b))) == 0 {
				break
			}
		}
	}
	if err != nil || len(m) != 0 {
		t.Fatalf("Members() after Kill = %v, %v", m, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cgroup.procs")); err != nil || len(strings.TrimSpace(string(b))) != 0 {
		t.Fatalf("cgroup.procs after Kill = %q, %v", b, err)
	}
	t.Logf("after Kill: members=%v; cgroup.procs verified empty", m)
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cgroup %s after Close: %v, want removed", dir, err)
	}
}
