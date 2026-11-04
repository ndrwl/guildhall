package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// briefFile writes text to a brief file outside the copy and returns its path.
func briefFile(t *testing.T, text string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestTaskDesignHelpAndValidation: help needs no store, and bad flags, a missing or unknown surface, or a missing or
// empty brief exit 2 before the store is opened.
func TestTaskDesignHelpAndValidation(t *testing.T) {
	d := newRunDirector(t)
	code, out, stderr := d.run(t, "task", "design", "--help")
	if code != 0 || !strings.Contains(out, "--brief") || !strings.Contains(out, "--surface web-page|cli|chat|none") || stderr != "" {
		t.Fatalf("help = %d %q %q", code, out, stderr)
	}
	for _, args := range [][]string{
		{"--unknown", "x"},
		{"--task", "t1"},
		{"--brief", briefFile(t, "Show the options."), "--surface", "none"},
		{"--task", "t1", "--brief", briefFile(t, "Show the options.")},
		{"--task", "t1", "--surface", "poster", "--brief", briefFile(t, "Show the options.")},
		{"--task", "t1", "--surface", "Web-Page", "--brief", briefFile(t, "Show the options.")},
		{"--task", "t1", "--surface", "none", "--brief", filepath.Join(t.TempDir(), "missing.md")},
		{"--task", "t1", "--surface", "none", "--brief", briefFile(t, " \n")},
		{"--task", "t1", "--surface", "none", "--brief", briefFile(t, "Show the options."), "extra"},
	} {
		if code, _, _ := d.run(t, append([]string{"task", "design"}, d.flags(args...)...)...); code != 2 {
			t.Errorf("%v exit %d, want 2", args, code)
		}
	}
	if code, _, stderr := d.run(t, append([]string{"task", "design"}, d.flags("--task", "t1", "--surface", "poster", "--brief", briefFile(t, "x"))...)...); code != 2 ||
		!strings.Contains(stderr, `unknown surface "poster": want one of web-page, cli, chat, none`) {
		t.Errorf("an unknown surface = %d, %s", code, stderr)
	}
	if code, _, _ := d.run(t, "task", "design", "--task", "t1", "--surface", "none", "--brief", briefFile(t, "Show the options."), "--store", filepath.Join(t.TempDir(), "none.store"), "--repo", "app"); code != 2 {
		t.Errorf("missing --config and --state exit %d, want 2", code)
	}
	if _, out, _ := d.run(t, "help"); !strings.Contains(out, "task design") {
		t.Errorf("help omits task design: %s", out)
	}
}

// TestTaskDesignRefusesABugTask: a task whose workflow has no Director grill has no design round; the refusal exits 1
// and records nothing.
func TestTaskDesignRefusesABugTask(t *testing.T) {
	d := newRunDirector(t)
	code, _, stderr := d.run(t, append([]string{"task", "design"}, d.flags("--task", "t1", "--surface", "none", "--brief", briefFile(t, "Show the options."))...)...)
	if code != 1 || !strings.Contains(stderr, "bugs have no design round") {
		t.Fatalf("task design = %d, %s; want the bug refusal", code, stderr)
	}
	if docs := d.documents(t); len(docs) != 0 {
		t.Errorf("a refused request recorded %+v", docs)
	}
}

// TestTaskDesignRunsARound: on the grill workflow, a request while the grill runs is refused; once plan has run, a
// request launches the designer with the composed brief, its Surface: line first, an identical one replays, recording
// no brief, its result opens design-1, a second request is refused while it is open, and task show and task timeline
// count one round used.
func TestTaskDesignRunsARound(t *testing.T) {
	d := newRunDirectorWith(t, "design.toml")
	if code, out, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t1")...)...); code != 0 || out != `{"wait":"director-stage","step":"grill","attempt":1}`+"\n" {
		t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
	}
	design := append([]string{"task", "design"}, d.flags("--task", "t1", "--surface", "none", "--brief", briefFile(t, "Show the sort options as a mock-up."))...)
	if code, _, stderr := d.run(t, design...); code != 1 || !strings.Contains(stderr, "grill attempt 1 is still running") {
		t.Fatalf("task design during the grill = %d, %s", code, stderr)
	}
	notes, _ := grillNotesFile(t)
	if code, out, stderr := d.finish(t, "grill", "1", "--summary", "Agreed.", "--output", "notes="+notes); code != 0 || !strings.Contains(out, `"step":"plan"`) {
		t.Fatalf("step finish: exit %d, %q, stderr %s", code, out, stderr)
	}
	d.waitResult(t, "plan", 1)
	if code, out, stderr := d.run(t, append([]string{"step", "done"}, d.flags("--task", "t1", "--step", "plan", "--attempt", "1", "--result", d.result("plan", 1))...)...); code != 0 || !strings.Contains(out, `"decision":"accept-1"`) {
		t.Fatalf("plan done: exit %d, %q, stderr %s", code, out, stderr)
	}

	// The workspaces double makes no git repository, so the task's worktree is made one with a commit: the designer's
	// launch records its HEAD and intake checks the project is unchanged (docs/interpreter.md#design-rounds).
	work := filepath.Join(d.state, "app", "t1", "work")
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("task worktree: %v", err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "base"}} {
		cmd := exec.Command("git", append([]string{"-C", work, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if code, out, stderr := d.run(t, design...); code != 0 || out != `{"wait":"attempt","step":"designer","attempt":1}`+"\n" {
		t.Fatalf("task design: exit %d, %q, stderr %s", code, out, stderr)
	}
	sum := sha256.Sum256([]byte("Surface: none\n\nShow the sort options as a mock-up."))
	briefs := 0
	for _, doc := range d.documents(t) {
		if strings.HasSuffix(doc.Name, "-brief") {
			briefs++
			if doc.Name != "design-1-brief" || doc.SHA256 != hex.EncodeToString(sum[:]) {
				t.Errorf("brief %+v, want design-1-brief holding the composed brief", doc)
			}
		}
	}
	d.waitResult(t, "designer", 1)
	// An identical request replays: the fake designer may already have finished, so it may take the result.
	if code, out, stderr := d.run(t, design...); code != 0 || !strings.Contains(out, `"step":"design`) {
		t.Fatalf("replayed task design: exit %d, %q, stderr %s", code, out, stderr)
	}
	if got := d.attempts(t); len(got) != 3 {
		t.Errorf("attempts = %+v, want grill, plan and one designer", got)
	}
	if code, out, stderr := d.run(t, append([]string{"step", "done"}, d.flags("--task", "t1", "--step", "designer", "--attempt", "1", "--result", d.result("designer", 1))...)...); code != 0 || out != `{"wait":"decision","step":"design","decision":"design-1"}`+"\n" {
		t.Fatalf("designer done: exit %d, %q, stderr %s", code, out, stderr)
	}
	for _, doc := range d.documents(t) {
		if strings.HasSuffix(doc.Name, "-brief") {
			briefs--
		}
	}
	if briefs != 0 {
		t.Errorf("the replay changed the brief count by %d, want no brief recorded", -briefs)
	}
	// The same brief under another surface is another request, refused while the round is open.
	other := append([]string{"task", "design"}, d.flags("--task", "t1", "--surface", "cli", "--brief", briefFile(t, "Show the sort options as a mock-up."))...)
	if code, _, stderr := d.run(t, other...); code != 1 || !strings.Contains(stderr, "design round 1 is already open") {
		t.Fatalf("a second request = %d, %s", code, stderr)
	}
	for _, sub := range []string{"show", "timeline"} {
		code, out, stderr := d.run(t, "task", sub, "--store", d.store, "--repo", "app", "t1", "--json")
		if code != 0 || !strings.Contains(out, `"design_rounds":1`) {
			t.Errorf("task %s: exit %d, stderr %s; want design_rounds 1 in %s", sub, code, stderr, out)
		}
	}
}
