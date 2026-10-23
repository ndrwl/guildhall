package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
)

func TestAdvancePreflightLockWait(t *testing.T) {
	for _, command := range [][]string{
		{"task", "start", "t-1"},
		{"task", "start", "t-1", "--decision", "d-1"},
		{"step", "retry", "--task", "t-1", "--step", "build", "--attempt", "1"},
	} {
		t.Run(strings.Join(command, " "), func(t *testing.T) {
			d := newRunDirector(t)
			b, err := os.ReadFile(d.config)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS(filepath.Dir(d.config))); err != nil {
				t.Fatal(err)
			}
			d.config = filepath.Join(dir, "config.toml")
			b = []byte(strings.Replace(string(b), "[local]", "[local]\nlock_wait = \"1s\"", 1))
			if err := os.WriteFile(d.config, b, 0o600); err != nil {
				t.Fatal(err)
			}
			holder, err := store.Open(d.store, store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = holder.Close() }() // Cleanup only; lock-release errors are not under test.
			start := time.Now()
			code, _, stderr := d.run(t, append(command, d.flags()...)...)
			elapsed := time.Since(start)
			if code == 0 || !strings.Contains(stderr, store.ErrLocked.Error()) {
				t.Fatalf("code %d, stderr %s; want store lock refusal", code, stderr)
			}
			if elapsed > 4*time.Second {
				t.Fatalf("lock wait %v; want refusal within 4s", elapsed)
			}
		})
	}
}
