package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/store"
)

// Real CLI/store recovery verifies next.json, the status feed and retry, without
// starting an external worker. The interpreter's D3 regression uses a review.
func TestCLIRejectedIntakeRecovery(t *testing.T) {
	for _, command := range []string{"task start", "step done"} {
		for _, bad := range []string{"missing output", "malformed result"} {
			t.Run(command+"/"+bad, func(t *testing.T) {
				d := newRunDirectorWith(t, "grill.toml")
				if code, _, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 {
					t.Fatal(stderr)
				}
				result := d.result("grill", 1)
				if bad == "missing output" {
					if err := adapter.WriteResult(result, adapter.Result{Identity: adapter.Identity{TaskID: "t-1", AttemptID: "grill-1"}, Status: adapter.StatusSucceeded, Summary: "done"}); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(result, []byte(`{}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
				s, err := store.Open(d.store, store.Options{})
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.FinishAttempt("launcher-success", "app", "t-1", "grill", 1, store.AttemptSucceeded)
				_ = s.Close()
				if err != nil {
					t.Fatal(err)
				}
				args := append([]string{"task", "start"}, d.flags("t-1")...)
				if command == "step done" {
					args = append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", "grill", "--attempt", "1", "--result", result)...)
				}
				for range 2 {
					code, out, stderr := d.run(t, args...)
					if code != 0 || out != "{\"wait\":\"director\",\"step\":\"grill\",\"attempt\":1}\n" {
						t.Fatalf("intake: exit %d %q %s", code, out, stderr)
					}
				}
				data, err := os.ReadFile(filepath.Join(d.state, "app", "t-1", "next.json"))
				if err != nil || string(data) != "{\"wait\":\"director\",\"step\":\"grill\",\"attempt\":1}\n" {
					t.Fatalf("next.json=%q, %v", data, err)
				}
				st, err := readDirectorStatus(context.Background(), d.store, d.state, nil)
				if err != nil {
					t.Fatal(err)
				}
				i := slices.IndexFunc(st.NeedsMe, func(it needsMeItem) bool { return it.Kind == "retry" && it.Step == "grill" })
				if i < 0 || !st.NeedsMe[i].Retryable || !strings.Contains(st.NeedsMe[i].Reason, "result rejected:") {
					t.Fatalf("status=%+v", st.NeedsMe)
				}
				code, out, stderr := d.run(t, append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", "grill", "--attempt", "1")...)...)
				if code != 0 || out != "{\"wait\":\"director-stage\",\"step\":\"grill\",\"attempt\":2}\n" {
					t.Fatalf("retry: exit %d %q %s", code, out, stderr)
				}
			})
		}
	}
}
