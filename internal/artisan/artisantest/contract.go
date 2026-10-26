// Package artisantest is the containment contract every artisan.Unit passes:
// the backends on their native runners and the test double the launcher's
// tests use. Real processes are played by the test binary; a consumer calls
// MainIfWorker first in its TestMain.
//
// Harness rules: every case records the identities it caused and checks them
// by PID plus token, never by name; an unrelated sentinel process must
// survive every case; every wait has a deadline and says what it waited for.
package artisantest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// deadline bounds every wait for a process to appear or end.
var deadline = testenv.Ceiling(time.Minute)

// Harness is one Unit implementation under test.
type Harness struct {
	// New returns a fresh, unstarted unit.
	New func(t *testing.T) artisan.Unit
	// Probe reports an identity's liveness, as artisan.Probe does for real
	// processes.
	Probe func(artisan.Identity) (artisan.Liveness, error)
	// Kill ends one identity the case owns and Probe reported alive, as
	// KillVerified does for real processes; only cleanup uses it.
	Kill func(artisan.Identity) error
	// Terminate is whether the unit has the terminate rung (Unix).
	Terminate bool
	// SetsidEscapes is whether a grandchild that calls setsid leaves the unit
	// (Unix process group): it is then an escape, not a member.
	SetsidEscapes bool
	// SetsidContained is whether a grandchild that calls setsid stays in the
	// unit (Linux cgroup): it is then a member and Kill ends it.
	SetsidContained bool
	// Kind is the Containment kind every unit must report.
	Kind string
	// BreakawayRefused is whether a breakaway request is refused (Windows job).
	BreakawayRefused bool
	// Ladder bounds the stop contract's rungs; zero keeps stopLadder. A unit
	// whose interrupt is modelled rather than delivered needs no real waits.
	Ladder artisan.Ladder
	// Files returns the attempt files of a stop-contract case's attempt
	// directory; nil means FilesOf(NewFiles(dir)), the byte-comparing double.
	Files func(dir string) StopFiles
	// Worker returns the command that plays w as a stop-contract case's
	// worker, recording every process it starts in the inventory directory
	// dir as Command's worker does, with f the case's attempt files: a real
	// Artisan, such as the fake adapter's command. Nil runs the test binary
	// with Command. Only the stop contract uses it.
	Worker func(t *testing.T, dir string, w Worker, f StopFiles) *exec.Cmd
}

// Native is the harness for a backend containing real processes.
func Native(newUnit func(t *testing.T) artisan.Unit) Harness {
	return Harness{New: newUnit, Probe: artisan.Probe, Kill: KillVerified}
}

// RunUnitContract runs the containment contract against h.
func RunUnitContract(t *testing.T, h Harness) {
	t.Run("start reports the worker and a spawned grandchild as members", func(t *testing.T) {
		c := h.begin(t, Worker{Grandchildren: 1})
		worker := c.start()
		gc := c.role("grandchild", 1)
		c.waitMembers(append([]artisan.Identity{worker}, ids(gc)...)...)
		if w := c.role("worker", 1); w[0].Identity != worker {
			t.Errorf("Start returned %v, the worker recorded %v", worker, w[0].Identity)
		}
		c.kill()
	})
	t.Run("an ignored interrupt leaves members", func(t *testing.T) {
		c := h.begin(t, Worker{Grandchildren: 1, IgnoreInterrupt: true})
		worker := c.start()
		all := append([]artisan.Identity{worker}, ids(c.role("grandchild", 1))...)
		c.waitMembers(all...)
		_ = c.u.Interrupt()                // delivery may fail; only membership decides a rung
		time.Sleep(500 * time.Millisecond) // timing:allow negative observation of an ignored signal after worker readiness, not a progress deadline.
		c.waitMembers(all...)
		c.kill()
	})
	t.Run("kill empties the unit and exited closes", func(t *testing.T) {
		c := h.begin(t, Worker{Grandchildren: 2, IgnoreInterrupt: true})
		worker := c.start()
		c.waitMembers(append([]artisan.Identity{worker}, ids(c.role("grandchild", 2))...)...)
		c.kill()
		c.waitExited()
		c.assertGone(c.owned()...)
	})
	t.Run("exited closes when the worker ends on its own", func(t *testing.T) {
		release := filepath.Join(t.TempDir(), "release")
		c := h.begin(t, Worker{Release: release})
		c.start()
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		c.waitExited()
		c.waitEmpty()
	})
	t.Run("terminate empties the unit or is refused", func(t *testing.T) {
		c := h.begin(t, Worker{Grandchildren: 1})
		worker := c.start()
		all := append([]artisan.Identity{worker}, ids(c.role("grandchild", 1))...)
		c.waitMembers(all...)
		err := c.u.Terminate()
		if !h.Terminate {
			if !errors.Is(err, artisan.ErrUnsupportedRung) || !errors.Is(err, errors.ErrUnsupported) {
				t.Errorf("Terminate() = %v, want ErrUnsupportedRung", err)
			}
			c.waitMembers(all...)
			c.kill()
			return
		}
		if err != nil {
			t.Fatalf("Terminate() = %v", err)
		}
		c.waitEmpty()
		c.waitExited()
	})
	t.Run("close kills what remains and refuses later use", func(t *testing.T) {
		c := h.begin(t, Worker{Grandchildren: 1, IgnoreInterrupt: true})
		worker := c.start()
		c.waitMembers(append([]artisan.Identity{worker}, ids(c.role("grandchild", 1))...)...)
		if err := c.u.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		c.assertGone(c.owned()...)
		if err := c.u.Close(); err != nil {
			t.Errorf("second Close() = %v, want nil", err)
		}
		if _, err := c.u.Members(); !errors.Is(err, artisan.ErrClosed) {
			t.Errorf("Members() after Close = %v, want ErrClosed", err)
		}
		if err := c.u.Kill(); !errors.Is(err, artisan.ErrClosed) {
			t.Errorf("Kill() after Close = %v, want ErrClosed", err)
		}
		if _, err := c.u.Start(Command(c.dir, Worker{})); !errors.Is(err, artisan.ErrClosed) {
			t.Errorf("Start() after Close = %v, want ErrClosed", err)
		}
	})
	t.Run("start is refused twice and rungs before start", func(t *testing.T) {
		c := h.begin(t, Worker{})
		for name, rung := range map[string]func() error{"Interrupt": c.u.Interrupt, "Kill": c.u.Kill} {
			if err := rung(); !errors.Is(err, artisan.ErrNotStarted) {
				t.Errorf("%s() before Start = %v, want ErrNotStarted", name, err)
			}
		}
		if _, err := c.u.Members(); !errors.Is(err, artisan.ErrNotStarted) {
			t.Errorf("Members() before Start = %v, want ErrNotStarted", err)
		}
		c.start()
		if _, err := c.u.Start(Command(c.dir, Worker{})); !errors.Is(err, artisan.ErrStarted) {
			t.Errorf("second Start() = %v, want ErrStarted", err)
		}
		c.kill()
	})
	t.Run("a program that cannot start is an error", func(t *testing.T) {
		c := h.begin(t, Worker{})
		if _, err := c.u.Start(exec.Command(filepath.Join(c.dir, "no-such-program"))); err == nil {
			t.Fatal("Start() of a missing program succeeded")
		}
		if err := c.u.Close(); err != nil {
			t.Errorf("Close() after a failed Start = %v", err)
		}
	})
	if h.SetsidEscapes {
		t.Run("a setsid grandchild is an escape, not a member", func(t *testing.T) {
			c := h.begin(t, Worker{Grandchildren: 1, SetsidGrandchild: true})
			worker := c.start()
			gc := c.role("grandchild-setsid", 1)[0]
			c.waitMembers(worker)
			c.kill()
			if m := c.members(); slices.Contains(m, gc.Identity) {
				t.Errorf("setsid grandchild %v is a member", gc.Identity)
			}
			if lv, err := h.Probe(gc.Identity); lv != artisan.Alive {
				t.Errorf("setsid grandchild is %v (%v) after kill emptied the unit; want an escape observed", lv, err)
			}
			t.Logf("escape recorded: setsid grandchild %v outside the unit", gc.Identity)
			c.escaped(gc.Identity)
		})
	}
	if h.SetsidContained {
		t.Run("a setsid grandchild is a member and kill ends it", func(t *testing.T) {
			c := h.begin(t, Worker{Grandchildren: 1, SetsidGrandchild: true})
			worker := c.start()
			gc := c.role("grandchild-setsid", 1)[0]
			c.waitMembers(worker, gc.Identity)
			c.kill()
			c.assertGone(gc.Identity)
		})
	}
	t.Run("containment reports its kind", func(t *testing.T) {
		c := h.begin(t, Worker{})
		if got := c.u.Containment(); got.Kind != h.Kind {
			t.Errorf("Containment() = %+v, want kind %q", got, h.Kind)
		}
	})
	if h.BreakawayRefused {
		t.Run("a breakaway request is refused", func(t *testing.T) {
			c := h.begin(t, Worker{Grandchildren: 1, BreakawayGrandchild: true})
			worker := c.start()
			c.role("breakaway-refused", 1)
			c.waitMembers(append([]artisan.Identity{worker}, ids(c.role("grandchild", 1))...)...)
			c.kill()
		})
	}
}

// run is one case: a unit, its inventory, its sentinel and what it owns.
type run struct {
	t               *testing.T
	h               Harness
	u               artisan.Unit
	w               Worker
	dir             string
	sentinelRelease string

	mu      sync.Mutex
	seen    map[artisan.Identity]bool
	escapes map[artisan.Identity]bool
}

func (h Harness) begin(t *testing.T, w Worker) *run {
	t.Helper()
	c := &run{t: t, h: h, u: h.New(t), w: w, dir: t.TempDir(), seen: map[artisan.Identity]bool{}, escapes: map[artisan.Identity]bool{}}
	sentinel := c.sentinel()
	t.Cleanup(func() {
		_ = c.u.Close()
		for _, id := range c.owned() {
			// Job termination can remove a member before its process handle
			// becomes signaled. Allow exit to finish before reporting a leak.
			if !c.escapes[id] {
				c.assertGone(id)
			}
			if lv, _ := h.Probe(id); lv == artisan.Alive {
				_ = h.Kill(id)
			}
		}
		release := c.w.Release
		if release == "" {
			release = filepath.Join(c.dir, "release")
		}
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Error(err)
		}
		c.assertGone(c.owned()...)
		if lv, err := artisan.Probe(sentinel); lv != artisan.Alive {
			t.Errorf("unrelated sentinel %v is %v (%v) after the case", sentinel, lv, err)
		}
		if err := os.WriteFile(c.sentinelRelease, nil, 0o600); err != nil {
			t.Error(err)
		}
		end := time.Now().Add(deadline)
		for time.Now().Before(end) {
			if lv, _ := artisan.Probe(sentinel); lv == artisan.Gone {
				return
			}
			time.Sleep(20 * time.Millisecond) // timing:allow cleanup polls recorded identities within the overall ceiling.
		}
		t.Errorf("sentinel %v did not exit after release", sentinel)
	})
	return c
}

// sentinel starts a real process outside every unit.
func (c *run) sentinel() artisan.Identity {
	c.t.Helper()
	dir := c.t.TempDir()
	c.sentinelRelease = filepath.Join(dir, "release")
	cmd := Worker{Role: "sentinel", Inventory: dir, Lifetime: 2 * time.Minute, Release: c.sentinelRelease}.command()
	if err := cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	return entriesIn(c.t, dir, "sentinel", 1)[0].Identity
}

func (c *run) start() artisan.Identity {
	c.t.Helper()
	id, err := c.u.Start(Command(c.dir, c.w))
	if err != nil {
		c.t.Fatalf("Start() = %v", err)
	}
	c.own(id)
	return id
}

func (c *run) own(ids ...artisan.Identity) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		if id.Token != "" {
			c.seen[id] = true
		}
	}
}

func (c *run) escaped(id artisan.Identity) {
	c.mu.Lock()
	c.escapes[id] = true
	c.mu.Unlock()
}

func (c *run) owned() []artisan.Identity {
	inv, _ := Inventory(c.dir)
	for _, e := range inv {
		c.own(e.Identity)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []artisan.Identity
	for id := range c.seen {
		out = append(out, id)
	}
	return out
}

// role waits for n inventory entries of role and owns them.
func (c *run) role(role string, n int) []Entry {
	c.t.Helper()
	es := entriesIn(c.t, c.dir, role, n)
	for _, e := range es {
		c.own(e.Identity)
	}
	return es
}

func entriesIn(t *testing.T, dir, role string, n int) []Entry {
	t.Helper()
	var got []Entry
	for end := time.Now().Add(deadline); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		inv, err := Inventory(dir)
		if err != nil {
			t.Fatal(err)
		}
		got = got[:0]
		for _, e := range inv {
			if e.Role == role {
				got = append(got, e)
			}
		}
		if len(got) >= n {
			return got
		}
	}
	t.Fatalf("waited %v for %d %s inventory entries, have %v", deadline, n, role, got)
	return nil
}

func (c *run) members() []artisan.Identity {
	c.t.Helper()
	m, err := c.u.Members()
	if err != nil {
		c.t.Fatalf("Members() = %v", err)
	}
	c.own(m...)
	return m
}

func (c *run) waitMembers(want ...artisan.Identity) {
	c.t.Helper()
	var m []artisan.Identity
	for end := time.Now().Add(deadline); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		m = c.members()
		if containsAll(m, want) {
			return
		}
	}
	c.t.Fatalf("waited %v for members %v, have %v", deadline, want, m)
}

func (c *run) waitEmpty() {
	c.t.Helper()
	var m []artisan.Identity
	for end := time.Now().Add(deadline); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if m = c.members(); len(m) == 0 {
			return
		}
	}
	c.t.Fatalf("waited %v for the unit to empty, members %v", deadline, m)
}

func (c *run) waitExited() {
	c.t.Helper()
	select {
	case <-c.u.Exited():
	case <-time.After(deadline):
		c.t.Fatalf("waited %v for Exited to close", deadline)
	}
}

func (c *run) kill() {
	c.t.Helper()
	if err := c.u.Kill(); err != nil {
		c.t.Fatalf("Kill() = %v", err)
	}
	c.waitEmpty()
}

func (c *run) assertGone(ids ...artisan.Identity) {
	c.t.Helper()
	for _, id := range ids {
		var lv artisan.Liveness
		for end := time.Now().Add(deadline); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if lv, _ = c.h.Probe(id); lv == artisan.Gone {
				break
			}
		}
		if lv != artisan.Gone {
			c.t.Errorf("owned process %v is %v, want gone", id, lv)
		}
	}
}

func ids(es []Entry) []artisan.Identity {
	out := make([]artisan.Identity, len(es))
	for i, e := range es {
		out[i] = e.Identity
	}
	return out
}

func containsAll(have, want []artisan.Identity) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}
