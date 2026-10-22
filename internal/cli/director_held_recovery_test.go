package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

func TestHeldRecoveryWritesWait(t *testing.T) {
	for _, director := range []bool{false, true} {
		for _, retry := range []bool{false, true} {
			name, cfg, step := "artisan", "config.toml", "plan"
			if director {
				name, cfg, step = "director", "grill.toml", "grill"
			}
			if retry {
				name += "/retry"
			} else {
				name += "/start"
			}
			t.Run(name, func(t *testing.T) {
				d := newRunDirectorWith(t, cfg)
				if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
					rc, err := s.StartAttempt("recorded-before-pause", "app", "t-1", step)
					if err != nil || !retry {
						return rc, err
					}
					if _, err := s.StopUnownedAttempt("stopped-before-retry", "app", "t-1", step, 1); err != nil {
						return store.Receipt{}, err
					}
					return s.StartAttempt("retry-recorded-before-pause", "app", "t-1", step)
				}); err != nil {
					t.Fatal(err)
				}
				recordSession(t, d.store, false, store.DirectorRunning, store.DirectorPaused)
				args := append([]string{"task", "start"}, d.flags("t-1")...)
				if retry {
					args = append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", step, "--attempt", "1")...)
				}
				want := `{"wait":"paused","step":"` + step + `"}` + "\n"
				if code, out, stderr := d.run(t, args...); code != 0 || out != want {
					t.Fatalf("held recovery: %d, %q, %s", code, out, stderr)
				}
				data, err := os.ReadFile(filepath.Join(d.state, "app", "t-1", "next.json"))
				if err != nil || string(data) != want {
					t.Fatalf("persisted recovery wait = %q, %v", data, err)
				}
				n := 1
				if retry {
					n = 2
				}
				if got := d.attempts(t); len(got) != n {
					t.Fatalf("recovery recorded extra attempts: %+v", got)
				}
				code, out, stderr := d.run(t, "director", "resume", "--store", d.store, "--state", d.state)
				if code != 0 {
					t.Fatalf("resume: %d, %s, %s", code, out, stderr)
				}
			})
		}
	}
}
