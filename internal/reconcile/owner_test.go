package reconcile_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestProbeOwner pins the owner-gone rule directly, the one copy that
// Classify, launch.ProbeOwner and launch.Resume read: gone only when the
// recorded launcher is gone and its recorded unit is proven empty.
func TestProbeOwner(t *testing.T) {
	t.Run("a live launcher is alive and its unit is not checked", func(t *testing.T) {
		_, c, inv := started(t, artisantest.Worker{})
		o := owner(self(t))
		o.Containment = c
		w := reconcile.ProbeOwner(*o)
		if w.Launcher != artisan.Alive || w.Gone() || !w.Recorded || w.Survivors != nil || w.UnitErr != nil {
			t.Errorf("owner %+v", w)
		}
		releaseAll(t, inv)
	})
	t.Run("a gone launcher without recorded containment is not gone", func(t *testing.T) {
		w := reconcile.ProbeOwner(*owner(exitedIdentity(t)))
		if w.Launcher != artisan.Gone || w.ProbeErr != nil || w.Recorded || w.Gone() {
			t.Errorf("owner %+v", w)
		}
	})
	t.Run("a gone launcher with an emptied unit is gone", func(t *testing.T) {
		u, c, inv := started(t, artisantest.Worker{Grandchildren: 1})
		if err := u.Kill(); err != nil {
			t.Fatal(err)
		}
		assertGone(t, inv)
		o := owner(exitedIdentity(t))
		o.Containment = c
		for end := time.Now().Add(deadline); ; time.Sleep(beat) {
			w := reconcile.ProbeOwner(*o)
			if w.Gone() {
				if w.Unit.Kind != c.Kind || w.Unit.Name != c.Name || w.Worker != artisan.Identity(c.Worker) {
					t.Errorf("translated unit %+v worker %v, recorded %+v", w.Unit, w.Worker, c)
				}
				break
			}
			if time.Now().After(end) {
				t.Fatalf("owner never gone: %+v", w)
			}
		}
	})
	t.Run("a gone launcher with contained survivors is not gone and names them", func(t *testing.T) {
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
		w := reconcile.ProbeOwner(*o)
		if w.Gone() || w.UnitErr != nil || len(w.Survivors) != 2 {
			t.Errorf("owner %+v, want the worker and grandchild as survivors", w)
		}
		releaseAll(t, inv)
	})
	t.Run("a gone launcher whose unit cannot be checked is not gone", func(t *testing.T) {
		o := owner(exitedIdentity(t))
		o.Containment = store.Containment{Kind: "unrecognised", Name: "x"}
		w := reconcile.ProbeOwner(*o)
		if w.Gone() || !errors.Is(w.UnitErr, errors.ErrUnsupported) {
			t.Errorf("owner %+v, want an unsupported unit error", w)
		}
	})
}
