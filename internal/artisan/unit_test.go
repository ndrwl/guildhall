package artisan_test

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

func TestMain(m *testing.M) {
	artisantest.MainIfWorker()
	mainIfEndpointHolder()
	os.Exit(m.Run())
}

// TestUnitContract runs the containment contract against this platform's
// backend; platformHarness sets what the backend's platform can and cannot do.
func TestUnitContract(t *testing.T) {
	artisantest.RunUnitContract(t, nativeHarness(t))
}

func nativeHarness(t *testing.T) artisantest.Harness {
	h := artisantest.Native(func(t *testing.T) artisan.Unit {
		u, err := artisan.NewUnit("contract-" + rand.Text()[:8])
		if err != nil {
			t.Fatal(err)
		}
		return u
	})
	platformHarness(t, &h)
	return h
}

// TestFakeUnitContract keeps the double the launcher's tests use honest.
func TestFakeUnitContract(t *testing.T) {
	artisantest.RunUnitContract(t, newFakeHarness())
}

func TestFakeUnitEscapeAfterWait(t *testing.T) {
	for _, action := range []string{"interrupt", "terminate", "kill", "close", "release"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newFakeHarness()
				u := h.New(t)
				t.Cleanup(func() { _ = u.Close() })
				dir := t.TempDir()
				if _, err := u.Start(artisantest.Command(dir, artisantest.Worker{Grandchildren: 1, SetsidGrandchild: true})); err != nil {
					t.Fatal(err)
				}
				entries, err := artisantest.Inventory(dir)
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				var stopErr error
				switch action {
				case "interrupt":
					stopErr = u.Interrupt()
				case "terminate":
					stopErr = u.Terminate()
				case "kill":
					stopErr = u.Kill()
				case "close":
					stopErr = u.Close()
				case "release":
					stopErr = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600)
				}
				if stopErr != nil {
					t.Fatal(stopErr)
				}
				// Let the waiter observe the release, not just the synchronous stop.
				time.Sleep(40 * time.Millisecond)
				synctest.Wait()
				for _, entry := range entries {
					want := artisan.Gone
					if entry.Role == "grandchild" && action != "release" {
						want = artisan.Alive
					}
					if got, err := h.Probe(entry.Identity); err != nil || got != want {
						t.Errorf("%s after %s: got %v (%v), want %v", entry.Role, action, got, err, want)
					}
					_ = h.Kill(entry.Identity)
				}
			})
		})
	}
}
