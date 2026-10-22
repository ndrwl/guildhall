package artisan_test

import (
	"crypto/rand"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// platformHarness: the detected Linux backend decides whether a setsid
// grandchild is a member (cgroup) or an escape (subreaper and process group).
func platformHarness(t *testing.T, h *artisantest.Harness) {
	kind, reason := artisan.DetectedContainment()
	t.Logf("detected containment %s: %s", kind, reason)
	h.Terminate = true
	h.Kind = kind
	if kind == artisan.KindCgroup {
		h.SetsidContained = true
	} else {
		h.SetsidEscapes = true
	}
}

// TestCgroupUnitContract runs the contract against the cgroup backend when
// this host delegates a cgroup v2 subtree, and records why when it does not.
func TestCgroupUnitContract(t *testing.T) {
	if kind, reason := artisan.DetectedContainment(); kind != artisan.KindCgroup {
		t.Skipf("cgroup backend not available: %s", reason)
	}
	h := artisantest.Native(func(t *testing.T) artisan.Unit {
		u, reason, ok, err := artisan.NewCgroupUnit("contract-" + rand.Text()[:8])
		if err != nil || !ok {
			t.Fatalf("NewCgroupUnit = %v, %v (%s)", err, ok, reason)
		}
		return u
	})
	h.Terminate, h.SetsidContained, h.Kind = true, true, artisan.KindCgroup
	artisantest.RunUnitContract(t, h)
}

// TestSubreaperUnitContract runs the contract against the fallback on every
// Linux host, whether or not a cgroup is delegated.
func TestSubreaperUnitContract(t *testing.T) {
	h := artisantest.Native(func(t *testing.T) artisan.Unit {
		u, err := artisan.NewSubreaperUnit("contract-" + rand.Text()[:8])
		if err != nil {
			t.Fatal(err)
		}
		return u
	})
	h.Terminate, h.SetsidEscapes, h.Kind = true, true, artisan.KindSubreaper
	artisantest.RunUnitContract(t, h)
}

// TestSubreaperRecordsReparent: under the fallback, a grandchild that left
// the group and was orphaned reparents to the launcher (this test process)
// and Escapes reports it, while Members does not.
func TestSubreaperRecordsReparent(t *testing.T) {
	u, err := artisan.NewSubreaperUnit("reparent-" + rand.Text()[:8])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	esc, ok := u.(interface {
		Escapes() ([]artisan.Identity, error)
	})
	if !ok {
		t.Fatalf("%T does not report escapes", u)
	}
	dir := t.TempDir()
	if _, err := u.Start(artisantest.Command(dir, artisantest.Worker{Grandchildren: 1, SetsidGrandchild: true, ExitAfterSpawn: true})); err != nil {
		t.Fatal(err)
	}
	var gc artisan.Identity
	t.Cleanup(func() {
		if gc.PID != 0 {
			_ = artisantest.KillVerified(gc)
		}
		_ = os.WriteFile(dir+"/release", nil, 0o600)
	})
	select {
	case <-u.Exited():
	case <-time.After(testenv.Ceiling(10 * time.Second)):
		t.Fatal("worker did not exit after spawning")
	}
	for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); gc.PID == 0 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		inv, _ := artisantest.Inventory(dir)
		for _, e := range inv {
			if e.Role == "grandchild-setsid" {
				gc = e.Identity
			}
		}
	}
	if gc.PID == 0 {
		t.Fatal("grandchild did not record itself")
	}
	var escapes []artisan.Identity
	for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if escapes, err = esc.Escapes(); err != nil {
			t.Fatalf("Escapes() = %v", err)
		}
		if slices.Contains(escapes, gc) {
			break
		}
	}
	if !slices.Contains(escapes, gc) {
		t.Fatalf("Escapes() = %v, want the orphaned setsid grandchild %v", escapes, gc)
	}
	if m, err := u.Members(); err != nil || slices.Contains(m, gc) {
		t.Errorf("Members() = %v, %v; the escape must not be a member", m, err)
	}
	t.Logf("reparented escape recorded: %v", gc)
}
