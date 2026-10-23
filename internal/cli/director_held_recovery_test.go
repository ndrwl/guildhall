package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
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
				// The engine persists inputs before recording an attempt. Recreate
				// that crash boundary, including the input binding needed by replay.
				count := 1
				if retry {
					count = 2
				}
				for n := 1; n <= count; n++ {
					data := []byte(`{"inputs":[]}`)
					name := fmt.Sprintf("%s-%d-inputs", step, n)
					path := fmt.Sprintf("tasks/app/t-1/%s-%d.inputs.json", step, n)
					file := filepath.Join(d.state, "documents", filepath.FromSlash(path))
					if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
						t.Fatal(err)
					}
					writeFile(t, file, string(data))
					if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
						return s.PutDocument(name, store.DocumentRef{Repository: "app", TaskID: "t-1", Name: name, Path: path, SHA256: digest.Of(data).Hex()})
					}); err != nil {
						t.Fatal(err)
					}
				}
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
				t.Logf("held %s: %s; persisted next.json: %s", name, want, data)
				n := 1
				if retry {
					n = 2
				}
				if got := d.attempts(t); len(got) != n {
					t.Fatalf("recovery recorded extra attempts: %+v", got)
				}
				attemptDir := filepath.Join(d.state, "app", "t-1", fmt.Sprintf("%s-%d", step, n))
				if _, err := os.Stat(attemptDir); !os.IsNotExist(err) {
					t.Fatalf("held recovery prepared an attempt directory: %v", err)
				}
				code, out, stderr := d.run(t, "director", "resume", "--store", d.store, "--state", d.state)
				if code != 0 {
					t.Fatalf("resume: %d, %s, %s", code, out, stderr)
				}
				code, out, stderr = d.run(t, args...)
				wait := "attempt"
				if director {
					wait = "director-stage"
				}
				want = fmt.Sprintf("{\"wait\":%q,\"step\":%q,\"attempt\":%d}\n", wait, step, n)
				if code != 0 || out != want {
					t.Fatalf("recovery after resume: %d, %q, %s; want %q", code, out, stderr, want)
				}
				t.Logf("recovery after resume: %s", out)
				if _, err := os.Stat(filepath.Join(attemptDir, "launched.json")); err != nil {
					t.Fatalf("resumed recovery did not publish its launch mark: %v", err)
				}
				if !director {
					d.waitResult(t, step, n)
				}
			})
		}
	}
}
