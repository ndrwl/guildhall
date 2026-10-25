package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestRunCleanupReportsAnOrphanedWorker: the testdata/run cleanup reports a process of an attempt's worker
// tree that outlived its launcher, and reports nothing once artisan resume has swept the attempt's unit
// (docs/contracts/cli.md#tests). The worker itself dies with its launcher where the platform ties it to
// the launcher's life, so the orphan is the worker's grandchild, recorded by the fake's inventory directive.
func TestRunCleanupReportsAnOrphanedWorker(t *testing.T) {
	dir, inv := t.TempDir(), t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "run"))); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(dir, "baseline", "stages", "plan.md")
	guide, err := os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	directives := "guildhall-fake: result=never\nguildhall-fake: lifetime=1m\nguildhall-fake: grandchildren=1\nguildhall-fake: inventory=" + inv + "\n"
	if err := os.WriteFile(plan, append(guide, directives...), 0o600); err != nil {
		t.Fatal(err)
	}
	d := newRunDirectorAt(t, filepath.Join(dir, "config.toml"))
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	tree := inventoryOf(t, inv, 2)
	t.Cleanup(func() {
		for _, id := range tree {
			_ = artisan.Kill(id)
		}
	})
	s, err := store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.Ownership("app", "t-1", "plan", 1)
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := artisantest.KillVerified(artisan.Identity(o.LauncherIdentity)); err != nil {
		t.Fatal(err)
	}
	// Kill only sends the signal. Prove exit before stop can contact the endpoint.
	waitGone(t, "before checking the orphaned worker", artisan.Identity(o.LauncherIdentity))
	// A Windows Job Object that kills its processes when its last handle closes takes the grandchild
	// with the launcher; elsewhere the grandchild runs on until the unit is swept.
	if got := d.leftovers(t, inv); runtime.GOOS != "windows" && (len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), "grandchild")) {
		t.Errorf("leftovers with the grandchild orphaned = %q, want it reported", got)
	}
	d.sweep(t, "plan", 1)
	if got := d.leftovers(t, inv); len(got) != 0 {
		t.Errorf("leftovers after resume = %q, want none", got)
	}
}
