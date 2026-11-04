package artisan_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/testenv"
	"golang.org/x/sys/unix"
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

// TestSweepKillsProcessGroupRecords: on Linux, Contained and Sweep find a
// session unit's pgroup record by its worker's group, as they find a
// launcher's subreaper+pgroup record.
func TestSweepKillsProcessGroupRecords(t *testing.T) {
	for kind, newUnit := range map[string]func(string) (artisan.Unit, error){
		artisan.KindGroup:     artisan.NewSessionGroupUnit,
		artisan.KindSubreaper: artisan.NewSubreaperUnit,
	} {
		t.Run(kind, func(t *testing.T) {
			sweepsFromOutside(t, func(name string) (artisan.Unit, error) {
				u, err := newUnit(name)
				if err == nil && u.Containment().Kind != kind {
					t.Fatalf("Containment() = %+v, want kind %q", u.Containment(), kind)
				}
				return u, err
			})
		})
	}
}

// TestSessionUnitLeavesNoZombies:the session unit without a cgroup does not
// make its process a child subreaper, so a worker's orphan goes to the system,
// which reaps it, and never stays a zombie of the Director supervisor. The
// Artisan launcher's unit still adopts it. Each case runs in a helper process
// of its own, since the subreaper attribute is process-wide and permanent.
func TestSessionUnitLeavesNoZombies(t *testing.T) {
	if mode := os.Getenv("ARTISAN_ORPHAN_HELPER"); mode != "" {
		orphanHelper(t, mode)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"session", "launcher"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(20*time.Second))
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestSessionUnitLeavesNoZombies$", "-test.v")
			cmd.Env = append(os.Environ(), "ARTISAN_ORPHAN_HELPER="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s helper: %v\n%s", mode, err, out)
			}
			t.Logf("%s helper:\n%s", mode, out)
		})
	}
}

// orphanHelper runs a worker that leaves an orphan in a fresh unit of mode's
// kind, closes the unit, and checks this process's subreaper attribute and
// its zombie children.
func orphanHelper(t *testing.T, mode string) {
	newUnit := artisan.NewSubreaperUnit
	if mode == "session" {
		newUnit = artisan.NewSessionGroupUnit
	}
	u, err := newUnit("orphan-" + rand.Text()[:8])
	if err != nil {
		t.Fatal(err)
	}
	if c := u.Containment(); mode == "session" && (c.Kind != artisan.KindGroup || c.Reason != "forced by test") {
		t.Errorf("session Containment() = %+v; want kind %q with the detection reason", c, artisan.KindGroup)
	}
	if _, err := u.Start(exec.Command("sh", "-c", "sleep 0.2 & exit 0")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-u.Exited():
	case <-time.After(testenv.Ceiling(10 * time.Second)):
		t.Fatal("worker did not exit")
	}
	var subreaper int32
	if err := unix.Prctl(unix.PR_GET_CHILD_SUBREAPER, uintptr(unsafe.Pointer(&subreaper)), 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	var zombies []int
	if mode == "session" {
		time.Sleep(time.Second) // timing:allow negative observation: the orphan's exit has a second to leave a zombie child.
		zombies = zombieChildren(t)
	} else {
		for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); len(zombies) == 0 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			zombies = zombieChildren(t)
		}
	}
	t.Logf("%s: subreaper %d, zombie children %v", mode, subreaper, zombies)
	switch {
	case mode == "session" && (subreaper != 0 || len(zombies) != 0):
		t.Errorf("session unit: subreaper %d, zombie children %v; want 0 and none", subreaper, zombies)
	case mode == "launcher" && (subreaper != 1 || len(zombies) == 0):
		t.Errorf("launcher unit: subreaper %d, zombie children %v; want 1 and the adopted orphan", subreaper, zombies)
	}
}

// zombieChildren lists the state-Z processes whose parent is this process.
func zombieChildren(t *testing.T) []int {
	t.Helper()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		i := bytes.LastIndexByte(b, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(b[i+1:]))
		if len(f) < 2 || f[0] != "Z" {
			continue
		}
		if ppid, err := strconv.Atoi(f[1]); err == nil && ppid == self {
			out = append(out, pid)
		}
	}
	return out
}
