package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

// A launcher released before starting a worker has no containment on process-group
// platforms. Its persisted outcome or cancel marker must not hide that cleanup evidence.
func TestCleanupReleasedBeforeWorker(t *testing.T) {
	for _, state := range []string{"absent", "marker", "cancelled", "failed", "finished"} {
		t.Run(state, func(t *testing.T) {
			d := newRunDirector(t)
			launcher := liveLauncher(t)
			if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
				if _, err := s.StartAttempt("start", "app", "t-1", "plan"); err != nil {
					return store.Receipt{}, err
				}
				return s.TakeOwnership("own", store.Ownership{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1, LauncherIdentity: store.ProcessIdentity(launcher)}, launch.ProbeOwner)
			}); err != nil {
				t.Fatal(err)
			}
			a := launch.Attempt{Key: artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}, Store: d.store, StateDir: d.state}
			if err := os.MkdirAll(a.Dir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if state == "marker" {
				if err := a.Files().Mark(); err != nil {
					t.Fatal(err)
				}
			} else if state != "absent" {
				b, err := json.Marshal(artisan.Outcome{Schema: artisan.OutcomeSchema, State: artisan.OutcomeState(state), Detail: "released before worker startup"})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(a.Dir(), artisan.OutcomeFile), b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
				return s.FinishAttempt("finish", "app", "t-1", "plan", 1, store.AttemptStopped)
			}); err != nil {
				t.Fatal(err)
			}
			if err := artisantest.KillVerified(launcher); err != nil {
				t.Fatal(err)
			}
			waitGone(t, "released launcher", launcher)
			if leftovers := d.leftovers(t, ""); len(leftovers) != 0 {
				t.Errorf("cleanup of released attempt: %v", leftovers)
			}
		})
	}
}
