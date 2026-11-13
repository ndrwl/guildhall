package artisan_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// The identity contract against the real OS; pinned from the P0 scratch tests.
func TestIdentityContract(t *testing.T) {
	self, err := artisan.Self()
	if err != nil {
		t.Fatal(err)
	}
	if lv, err := artisan.Probe(self); lv != artisan.Alive || err != nil {
		t.Errorf("Probe(self) = %v, %v; want alive", lv, err)
	}
	if lv, err := artisan.Probe(artisan.Identity{PID: self.PID, Token: self.Token + "x"}); lv != artisan.Gone || err != nil {
		t.Errorf("Probe of a reused PID (token mismatch) = %v, %v; want gone", lv, err)
	}

	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	cmd := artisantest.Command(dir, artisantest.Worker{Release: release})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	var id artisan.Identity
	for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); id.PID == 0 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if inv, _ := artisantest.Inventory(dir); len(inv) == 1 {
			id = inv[0].Identity
		}
	}
	if id.PID != cmd.Process.Pid {
		t.Fatalf("worker recorded %v, started pid %d", id, cmd.Process.Pid)
	}
	if got, err := artisan.Lookup(id.PID); got != id || err != nil {
		t.Errorf("Lookup(%d) from another process = %v, %v; want %v", id.PID, got, err, id)
	}
	if err := artisantest.KillVerified(artisan.Identity{PID: id.PID, Token: id.Token + "x"}); err != nil {
		t.Fatal(err)
	}
	if lv, err := artisan.Probe(id); lv != artisan.Alive || err != nil {
		t.Fatalf("mismatched cleanup affected worker: %v, %v", lv, err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Exited but not yet reaped: a zombie is gone.
	lv, err := artisan.Probe(id)
	for end := time.Now().Add(testenv.Ceiling(10 * time.Second)); lv == artisan.Alive && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		lv, err = artisan.Probe(id)
	}
	if lv != artisan.Gone || err != nil {
		t.Errorf("Probe of an exited, unreaped process = %v, %v; want gone", lv, err)
	}
	_ = cmd.Wait()
	if lv, err := artisan.Probe(id); lv != artisan.Gone || err != nil {
		t.Errorf("Probe of an exited, reaped process = %v, %v; want gone", lv, err)
	}
}

// LookupChild reads the caller's own unreaped child, live or exited, with the identity Lookup gave it alive; once
// reaped it is eventually gone.
func TestLookupChildReadsAnExitedUnreapedChild(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	cmd := artisantest.Command(dir, artisantest.Worker{Release: release})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	pid := cmd.Process.Pid
	alive, err := artisan.Lookup(pid)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := artisan.LookupChild(pid); got != alive || err != nil {
		t.Errorf("LookupChild of a live child = %v, %v; want %v", got, err, alive)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	testenv.WaitFor(t, "the child to exit", func() bool {
		_, err := artisan.Lookup(pid)
		return err != nil
	})
	if got, err := artisan.LookupChild(pid); got != alive || err != nil {
		t.Errorf("LookupChild of an exited, unreaped child = %v, %v; want %v", got, err, alive)
	}
	_ = cmd.Wait()
	reaped = true
	// On Windows the process object, with its creation time, outlives the reap while another process, such as a
	// scanner, still holds a handle to it (d239), so wait for it to go; WaitFor still fails one that never does.
	testenv.WaitFor(t, "the reaped child to be gone", func() bool {
		got, err := artisan.LookupChild(pid)
		return err != nil || got != alive
	})
}
