package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// showGrill is task show's grill for app/<id>.
func showGrill(t *testing.T, path, id string) grillView {
	t.Helper()
	code, out, stderr := runTask(t, "show", "--store", path, "--repo", "app", "--json", id)
	if code != 0 {
		t.Fatalf("task show %s: exit %d, stderr %s", id, code, stderr)
	}
	var d struct {
		Grill grillView `json:"grill"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatal(err)
	}
	return d.Grill
}

// grillRecords are app/<id>'s grill-mode records as task timeline prints them.
func grillRecords(t *testing.T, path, id string) []store.GrillMode {
	t.Helper()
	code, out, stderr := runTask(t, "timeline", "--store", path, "--repo", "app", "--json", id)
	if code != 0 {
		t.Fatalf("task timeline %s: exit %d, stderr %s", id, code, stderr)
	}
	var tl struct {
		GrillModes []store.GrillMode `json:"grill_modes"`
	}
	if err := json.Unmarshal([]byte(out), &tl); err != nil {
		t.Fatal(err)
	}
	return tl.GrillModes
}

// TestTaskNewGrill: task new --grill records the task's grill mode with its reason, set by the Director, which task
// show prints as one line and task timeline lists; a rerun replays it, a task without --grill is live with no record,
// and a bug, whose workflow has no grill, is refused, creating nothing.
func TestTaskNewGrill(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	approveRepo(t, path, cfg, "app")
	args := []string{"new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Follow up", "--grill", "unattended", "--grill-reason", "A hallmark follow-up with its contracts written.", "t-1"}
	for range 2 {
		if code, _, stderr := runTask(t, args...); code != 0 {
			t.Fatalf("task new --grill: exit %d, stderr %s", code, stderr)
		}
	}
	g := showGrill(t, path, "t-1")
	if g.Line != "grill: unattended (director): A hallmark follow-up with its contracts written." || g.Mode != workflow.GrillUnattended || g.Number != 1 || g.SetAt == nil {
		t.Errorf("t-1 grill = %+v", g)
	}
	if got := grillRecords(t, path, "t-1"); len(got) != 1 || got[0].Reason != "A hallmark follow-up with its contracts written." || got[0].By != store.GrillModeByDirector {
		t.Errorf("t-1 timeline grill modes = %+v, want the one record", got)
	}

	newTask(t, path, cfg, "app", "Ask me", "t-2")
	if g := showGrill(t, path, "t-2"); g.Line != "grill: live" || g.Mode != workflow.GrillLive || g.Number != 0 || g.SetAt != nil {
		t.Errorf("t-2 grill = %+v, want live with no record", g)
	}
	if got := grillRecords(t, path, "t-2"); got == nil || len(got) != 0 {
		t.Errorf("t-2 timeline grill modes = %+v, want an empty list", got)
	}

	code, _, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Fix it", "--kind", "bug", "--grill", "live", "t-3")
	if code != 1 || !strings.Contains(stderr, "no grill run by the Director") {
		t.Errorf("task new --kind bug --grill: exit %d, stderr %s; want 1 naming the missing grill", code, stderr)
	}
	if code, _, _ := runTask(t, "show", "--store", path, "--repo", "app", "--json", "t-3"); code != 1 {
		t.Errorf("the refused bug was created")
	}
}

// TestTaskNewGrillUsage: a missing reason for unattended, a reason without --grill, an unknown mode and a two-line
// reason are usage errors.
func TestTaskNewGrillUsage(t *testing.T) {
	base := []string{"new", "--store", "s", "--config", "c", "--repo", "app", "--title", "T"}
	for name, extra := range map[string][]string{
		"unattended without a reason": {"--grill", "unattended"},
		"a reason without --grill":    {"--grill-reason", "why"},
		"an unknown mode":             {"--grill", "skipped", "--grill-reason", "why"},
		"a two-line reason":           {"--grill", "live", "--grill-reason", "one\ntwo"},
	} {
		if code, out, _ := runTask(t, append(append(append([]string{}, base...), extra...), "t-1")...); code != 2 || out != "" {
			t.Errorf("%s: exit %d, %q; want 2", name, code, out)
		}
	}
}

// TestTaskNewGrillNeedsAnArtisanAdapter: admission judges the definition with the mode applied, so an unattended grill
// is refused at creation when the grill's researcher profile cannot run it, naming --grill live, and the same task
// is created live.
func TestTaskNewGrillNeedsAnArtisanAdapter(t *testing.T) {
	cfg := writeDirector(t, strings.Replace(directorTOML, "[profile.demo]", "[profile.researcher]\nadapter = \"codex\"\n\n[profile.demo]", 1))
	path := emptyStore(t)
	approveRepo(t, path, cfg, "app")
	code, _, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "T", "--grill", "unattended", "--grill-reason", "Delegated.", "t-1")
	if code != 1 || !strings.Contains(stderr, `step "grill"`) || !strings.Contains(stderr, "--grill live") {
		t.Errorf("task new --grill unattended: exit %d, stderr %s; want 1 naming the grill and --grill live", code, stderr)
	}
	if code, _, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "T", "--grill", "live", "t-1"); code != 0 {
		t.Errorf("task new --grill live: exit %d, stderr %s", code, stderr)
	}
}

// TestTaskGrill: task grill switches an open task's grill mode until research starts, replays a rerun, records who
// set it, and refuses, writing nothing, the mode in effect, a running grill attempt, a started research and a bug.
func TestTaskGrill(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	for _, id := range []string{"t-1", "t-2", "t-3"} {
		newTask(t, path, cfg, "app", "Sort", id)
	}
	if code, _, stderr := runTask(t, "new", "--store", path, "--config", cfg, "--repo", "app", "--title", "Fix", "--kind", "bug", "t-4"); code != 0 {
		t.Fatalf("task new bug: exit %d, stderr %s", code, stderr)
	}
	grill := func(args ...string) (int, string, string) {
		return runTask(t, append([]string{"grill", "--store", path, "--config", cfg}, args...)...)
	}
	for range 2 {
		if code, out, stderr := grill("app/t-1", "unattended", "--reason", "Delegated: you decide."); code != 0 || !strings.Contains(out, `"record":"grill-mode/1"`) {
			t.Fatalf("task grill unattended: exit %d, %s, stderr %s", code, out, stderr)
		}
	}
	if code, _, stderr := grill("app/t-1", "unattended", "--reason", "Again."); code != 1 || !strings.Contains(stderr, "already unattended") {
		t.Errorf("the mode in effect: exit %d, stderr %s", code, stderr)
	}
	if code, _, stderr := grill("--by", "guildmaster", "app/t-1", "live", "--reason", "The Guildmaster: grill me on this."); code != 0 {
		t.Fatalf("task grill live: exit %d, stderr %s", code, stderr)
	}
	if g := showGrill(t, path, "t-1"); g.Line != "grill: live (guildmaster): The Guildmaster: grill me on this." || g.Number != 2 {
		t.Errorf("t-1 grill = %+v", g)
	}
	if code, _, stderr := grill("app/t-2", "live", "--reason", "Keep it live."); code != 1 || !strings.Contains(stderr, "already live") {
		t.Errorf("live on a task with no record: exit %d, stderr %s", code, stderr)
	}

	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range [][2]string{{"t-2", "grill"}, {"t-3", "grill"}, {"t-3", "research"}} {
		if _, err := s.StartAttempt("attempt:"+a[0]+":"+a[1], "app", a[0], a[1]); err != nil {
			t.Fatal(err)
		}
		if a[0] == "t-3" && a[1] == "grill" {
			if _, err := s.FinishAttempt("finish:t-3:grill", "app", "t-3", "grill", 1, store.AttemptSucceeded); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		task, want string
	}{
		"a running grill":    {"app/t-2", "grill attempt 1 of task app/t-2 is running"},
		"a started research": {"app/t-3", "research has started"},
		"a bug":              {"app/t-4", "no grill run by the Director"},
		"an unknown task":    {"app/t-9", "not found"},
	} {
		if code, _, stderr := grill(c.task, "unattended", "--reason", "Delegated."); code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, stderr %s; want 1 naming %q", name, code, stderr, c.want)
		}
	}
	for _, id := range []string{"t-2", "t-3", "t-4"} {
		if got := grillRecords(t, path, id); len(got) != 0 {
			t.Errorf("%s: a refusal recorded %+v", id, got)
		}
	}
}

// TestTaskGrillUsage: a missing reason or argument, an unknown mode or setter and a malformed task are usage errors.
func TestTaskGrillUsage(t *testing.T) {
	base := []string{"grill", "--store", "s", "--config", "c"}
	for name, args := range map[string][]string{
		"no reason":         {"app/t-1", "live"},
		"no mode":           {"app/t-1", "--reason", "r"},
		"an unknown mode":   {"app/t-1", "skipped", "--reason", "r"},
		"an unknown setter": {"app/t-1", "live", "--reason", "r", "--by", "artisan"},
		"no repository":     {"t-1", "live", "--reason", "r"},
		"a two-line reason": {"app/t-1", "live", "--reason", "a\nb"},
		"an unknown flag":   {"app/t-1", "live", "--reason", "r", "--force"},
	} {
		if code, out, _ := runTask(t, append(append([]string{}, base...), args...)...); code != 2 || out != "" {
			t.Errorf("%s: exit %d, %q; want 2", name, code, out)
		}
	}
	if code, out, _ := runTask(t, "grill", "--help"); code != 0 || !strings.Contains(out, "task grill") {
		t.Errorf("--help: exit %d, %q", code, out)
	}
}
