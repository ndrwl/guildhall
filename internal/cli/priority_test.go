package cli

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// TestTaskPriority sets a priority at task new and with task priority, by number, p-number and word; list, show
// and the timeline show it, a retry of the level in force prints the receipt that set it, and a bad level is a
// usage error.
func TestTaskPriority(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	newTask(t, path, cfg, "app", "Default", "t-1")
	approveRepo(t, path, cfg, "app")
	code, _, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Urgent", "--priority", "urgent", "t-2")
	if code != 0 {
		t.Fatalf("task new --priority: exit %d, stderr %s", code, stderr)
	}
	code, out, _ := runTask(t, "list", "--store", path, "--repo", "app", "--json")
	views := decodeJSON[[]struct {
		ID       string `json:"id"`
		Priority string `json:"priority"`
	}](t, out)
	if code != 0 || len(views) != 2 || views[0].Priority != "p3" || views[1].Priority != "p0" {
		t.Fatalf("list: exit %d, %s", code, out)
	}

	var first string
	for i, level := range []string{"p1", "1", "highest", "Highest"} {
		code, out, stderr := runTask(t, "priority", "--store", path, "--repo", "app", "t-1", level)
		rc := decodeJSON[store.Receipt](t, out)
		if code != 0 || rc.Record != store.TaskPriorityRecord || rc.MutationID != "priority:app:t-1:1" {
			t.Fatalf("priority %s: exit %d, %s, stderr %s", level, code, out, stderr)
		}
		if i > 0 && out != first {
			t.Fatalf("priority %s again = %s, want %s", level, out, first)
		}
		first = out
	}
	if code, out, _ := runTask(t, "priority", "--store", path, "--repo", "app", "t-1", "someday"); code != 0 || !strings.Contains(out, `"priority:app:t-1:2"`) {
		t.Fatalf("second change: exit %d, %s", code, out)
	}
	code, out, _ = runTask(t, "show", "--store", path, "--repo", "app", "--json", "t-1")
	if code != 0 || !strings.Contains(out, `"priority":"p5"`) {
		t.Fatalf("show: exit %d, %s", code, out)
	}
	code, out, _ = runTask(t, "timeline", "--store", path, "--repo", "app", "--json", "t-1")
	tl := decodeJSON[struct {
		Priority store.Priority `json:"priority"`
		Changes  []struct {
			Priority store.Priority `json:"priority"`
			SetAt    *string        `json:"set_at"`
		} `json:"priority_changes"`
	}](t, out)
	if code != 0 || tl.Priority != store.PrioritySomeday || len(tl.Changes) != 2 || tl.Changes[0].Priority != store.PriorityHighest || tl.Changes[1].SetAt == nil {
		t.Fatalf("timeline: exit %d, %s", code, out)
	}

	for _, args := range [][]string{
		{"priority", "--store", path, "--repo", "app", "t-1", "p6"},
		{"priority", "--store", path, "--repo", "app", "t-1", "top"},
		{"priority", "--store", path, "--repo", "app", "t-1"},
		{"priority", "--store", path, "t-1", "p1"},
		{"new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Bad", "--priority", "-1", "t-3"},
	} {
		if code, _, stderr := runTask(t, args...); code != 2 {
			t.Errorf("%v: exit %d, stderr %s; want usage error", args, code, stderr)
		}
	}
	if code, _, stderr := runTask(t, "priority", "--store", path, "--repo", "app", "t-9", "p1"); code != 1 || !strings.Contains(stderr, store.ErrNotFound.Error()) {
		t.Fatalf("unknown task: exit %d, stderr %s", code, stderr)
	}
}
