package release

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestPlanPrune: the newest Keep releases stay, an older held one stays with its holds, and an older one nothing
// holds is removed, with its damage in the reason when damaged.
func TestPlanPrune(t *testing.T) {
	rs := []Release{{Version: "v6"}, {Version: "v5"}, {Version: "v4"}, {Version: "v3"}, {Version: "v2", Damage: "bad"}, {Version: "v1"}}
	plan := PlanPrune(rs, map[string][]string{"v5": {"pinned by a"}, "v3": {"pinned by b", "rollback target of a"}})
	var removed []string
	for _, it := range plan {
		if it.Remove {
			removed = append(removed, it.Version)
		}
	}
	if !slices.Equal(removed, []string{"v2", "v1"}) {
		t.Fatalf("removed %q, want v2 and v1", removed)
	}
	if !slices.Equal(plan[1].Held, []string{}) || !slices.Equal(plan[3].Held, []string{"pinned by b", "rollback target of a"}) {
		t.Fatalf("holds %q, %q; want none within the newest three and v3's two", plan[1].Held, plan[3].Held)
	}
	if !strings.HasPrefix(plan[4].Reason, "damaged (bad), older than the newest 3") || !strings.HasPrefix(plan[5].Reason, "older than the newest 3") {
		t.Fatalf("reasons %q, %q", plan[4].Reason, plan[5].Reason)
	}

	// An entry whose name is not a release version, as List reports a stray one, is left and not counted: it
	// does not push a real release past the newest three, and is never removed.
	rs = []Release{{Version: "v3"}, {Version: "stray", Damage: "no manifest"}, {Version: "v2"}, {Version: "v1"}, {Version: "dev"}, {Version: "v0"}}
	plan = PlanPrune(rs, nil)
	removed = nil
	for _, it := range plan {
		if it.Remove {
			removed = append(removed, it.Version)
		}
	}
	if !slices.Equal(removed, []string{"v0"}) {
		t.Fatalf("with stray entries removed %q, want only v0", removed)
	}
	for _, i := range []int{1, 4} {
		if !plan[i].Left || plan[i].Remove || plan[i].Reason != NotARelease {
			t.Fatalf("stray entry %+v, want left with reason %q", plan[i], NotARelease)
		}
	}
	if plan[0].Left || plan[3].Left || plan[5].Left {
		t.Fatalf("a release was left: %+v", plan)
	}
}

// TestRemoveAndPruneIncoming: RemoveLocked takes a release out of releases/ leaving nothing behind, and
// PruneIncomingLocked removes stale and half-removed .incoming/ entries, keeping a fresh one.
func TestRemoveAndPruneIncoming(t *testing.T) {
	h := newHome(t)
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	installed(t, h, "v1.0.0", "one", at)
	installed(t, h, "v1.1.0", "two", at.Add(time.Minute))
	if err := h.RemoveLocked("v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Check("v1.0.0"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("v1.0.0 after remove: %v", err)
	}
	if all, err := h.List(); err != nil || len(all) != 1 || all[0].Version != "v1.1.0" {
		t.Fatalf("list %+v, %v", all, err)
	}
	if err := h.RemoveLocked("../x"); err == nil {
		t.Fatal("RemoveLocked of a path was not refused")
	}
	now := at.Add(48 * time.Hour)
	stale, fresh, half := filepath.Join(h.IncomingDir(), "v2"), filepath.Join(h.IncomingDir(), "v3"), filepath.Join(h.IncomingDir(), prunePrefix+"v1-1")
	for _, d := range []string{stale, fresh, half} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(d, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(stale, now.Add(-25*time.Hour), now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := h.PruneIncomingLocked(now)
	slices.Sort(got)
	if err != nil || !slices.Equal(got, []string{prunePrefix + "v1-1", "v2"}) {
		t.Fatalf("pruned %q, %v", got, err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("the fresh entry went: %v", err)
	}
}
