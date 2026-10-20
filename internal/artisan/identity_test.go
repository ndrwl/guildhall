package artisan_test

import (
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
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
	cmd := artisantest.Command(dir, artisantest.Worker{Lifetime: 300 * time.Millisecond})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var id artisan.Identity
	for end := time.Now().Add(10 * time.Second); id.PID == 0 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
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
	// Exited but not yet reaped: a zombie is gone.
	lv, err := artisan.Probe(id)
	for end := time.Now().Add(10 * time.Second); lv == artisan.Alive && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
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
