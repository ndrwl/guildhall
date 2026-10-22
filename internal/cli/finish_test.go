package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
)

// finish runs step finish for attempt n of step of task t-1 with extra flags.
func (d runDirector) finish(t *testing.T, step, n string, extra ...string) (int, string, string) {
	t.Helper()
	return d.run(t, append([]string{"step", "finish"}, d.flags(append([]string{"--task", "t-1", "--step", step, "--attempt", n}, extra...)...)...)...)
}

// grillNotesFile writes the Director's grill notes to a file outside the copy and returns it and its content.
func grillNotesFile(t *testing.T) (string, []byte) {
	t.Helper()
	notes := []byte("# Grill notes\n\n1. Sort by id? Yes.\n")
	file := filepath.Join(t.TempDir(), "agreed.md")
	if err := os.WriteFile(file, notes, 0o600); err != nil {
		t.Fatal(err)
	}
	return file, notes
}

func (d runDirector) documents(t *testing.T) []store.DocumentRef {
	t.Helper()
	s, err := store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	docs, err := s.Documents("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// TestStepFinishHandsTheGrillOn: task start hands the grill to the Director, rendered with the Director's role, and
// wakes it once however often the advance is repeated; step finish copies the notes into the task's working
// directory as notes.md, writes the attempt's result and advances to the plan, which is given the notes as context.
// A second finish, and a finish of the plan's Artisan attempt, are refused.
func TestStepFinishHandsTheGrillOn(t *testing.T) {
	d := newRunDirectorWith(t, "grill.toml")
	d.panes = &typedPanes{}
	startSession(t, d.store, store.DirectorPane{Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p2"})
	start := append([]string{"task", "start"}, d.flags("t-1")...)
	for range 2 {
		if code, out, stderr := d.run(t, start...); code != 0 || out != `{"wait":"director-stage","step":"grill","attempt":1}`+"\n" {
			t.Fatalf("task start: exit %d, %q, stderr %s", code, out, stderr)
		}
	}
	if len(d.panes.lines) != 1 {
		t.Errorf("typed %q, want one wake", d.panes.lines)
	}
	if ws := readWakes(t, d.store); len(ws) != 1 || ws[0].MessageID != "app.t-1.stage.grill-1" || ws[0].Decision != store.WakeWoke {
		t.Errorf("wakes = %+v, want one woke for the stage message", ws)
	}
	dir := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "grill", Attempt: 1}.Dir(d.state)
	data, err := os.ReadFile(filepath.Join(dir, "prompt", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m render.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Role.Path != "roles/director.md" || m.Role.Root != "release" || m.Guide.Path != "stages/grill.md" {
		t.Errorf("grill prompt role %+v, guide %+v; want the release Director role and grill guide", m.Role, m.Guide)
	}

	file, notes := grillNotesFile(t)
	code, out, stderr := d.finish(t, "grill", "1", "--summary", "Agreed with the Guildmaster.", "--output", "notes="+file)
	if code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
		t.Fatalf("step finish: exit %d, %q, stderr %s", code, out, stderr)
	}
	if got, err := os.ReadFile(filepath.Join(d.state, "app", "t-1", "work", "notes.md")); err != nil || string(got) != string(notes) {
		t.Errorf("work/notes.md = %q, %v; want the notes", got, err)
	}
	r, err := adapter.ReadResult(d.result("grill", 1), adapter.Identity{TaskID: "t-1", AttemptID: "grill-1"})
	if err != nil || r.Status != adapter.StatusSucceeded || r.Summary != "Agreed with the Guildmaster." || !slices.Equal(r.Outputs, []adapter.Output{{Path: "notes.md"}}) {
		t.Errorf("grill 1 result = %+v, %v", r, err)
	}
	var recorded bool
	for _, doc := range d.documents(t) {
		recorded = recorded || doc.Name == "grill-1-notes" && doc.SHA256 == digest.Of(notes).Hex()
	}
	if got := d.attempts(t); !recorded || len(got) != 2 || got[0].Outcome != store.AttemptSucceeded {
		t.Errorf("after step finish: notes recorded %v, attempts %+v", recorded, got)
	}

	if code, out, stderr := d.finish(t, "grill", "1", "--summary", "again", "--output", "notes="+file); code != 1 || out != "" || !strings.Contains(stderr, "finished") {
		t.Errorf("second finish: exit %d, %q, stderr %s; want 1 naming the finished attempt", code, out, stderr)
	}
	if code, out, stderr := d.finish(t, "plan", "1", "--summary", "mine", "--output", "spec="+file); code != 1 || out != "" || !strings.Contains(stderr, "not a Director stage") {
		t.Errorf("finish of the plan: exit %d, %q, stderr %s; want 1, not a Director stage", code, out, stderr)
	}
	d.waitResult(t, "plan", 1)
	data, err = os.ReadFile(filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}.Dir(d.state), "prompt", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Inputs) != 1 || m.Inputs[0].Port != "context" || m.Inputs[0].Digest != string(digest.Of(notes)) {
		t.Errorf("plan inputs = %+v, want the grill notes as context", m.Inputs)
	}
}

// TestStepFinishFailedThenRetry: --failed leaves the task waiting for the Director, step retry hands attempt 2 to
// it, and attempt 1, no longer current, is refused; each refusal records and writes nothing.
func TestStepFinishFailedThenRetry(t *testing.T) {
	d := newRunDirectorWith(t, "grill.toml")
	if code, _, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 {
		t.Fatalf("task start: exit %d, stderr %s", code, stderr)
	}
	file, _ := grillNotesFile(t)
	for name, args := range map[string][]string{
		"a later attempt":     {"--attempt", "2", "--summary", "s", "--output", "notes=" + file},
		"another output port": {"--attempt", "1", "--summary", "s", "--output", "spec=" + file},
		"a missing file":      {"--attempt", "1", "--summary", "s", "--output", "notes=" + filepath.Join(t.TempDir(), "none.md")},
		"a step not run":      {"--attempt", "1", "--summary", "s", "--output", "notes=" + file, "--step", "nope"},
		"an unknown task":     {"--attempt", "1", "--summary", "s", "--output", "notes=" + file, "--task", "t-2"},
	} {
		args := append([]string{"step", "finish"}, d.flags(append([]string{"--task", "t-1", "--step", "grill"}, args...)...)...)
		if code, out, stderr := d.run(t, args...); code != 1 || out != "" {
			t.Errorf("%s: exit %d, %q, stderr %s; want 1", name, code, out, stderr)
		}
	}
	if _, err := os.Stat(d.result("grill", 1)); !os.IsNotExist(err) {
		t.Fatalf("a refused finish wrote the result: %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.state, "app", "t-1", "work", "notes.md")); !os.IsNotExist(err) {
		t.Errorf("a refused finish copied an output: %v", err)
	}

	if code, out, stderr := d.finish(t, "grill", "1", "--summary", "The Guildmaster had to leave.", "--failed"); code != 0 || out != `{"wait":"director","step":"grill","attempt":1}`+"\n" {
		t.Fatalf("step finish --failed: exit %d, %q, stderr %s", code, out, stderr)
	}
	r, err := adapter.ReadResult(d.result("grill", 1), adapter.Identity{TaskID: "t-1", AttemptID: "grill-1"})
	if err != nil || r.Status != adapter.StatusFailed || len(r.Outputs) != 0 {
		t.Errorf("failed result = %+v, %v", r, err)
	}
	retry := append([]string{"step", "retry"}, d.flags("--task", "t-1", "--step", "grill", "--attempt", "1", "--reason", "back")...)
	if code, out, stderr := d.run(t, retry...); code != 0 || out != `{"wait":"director-stage","step":"grill","attempt":2}`+"\n" {
		t.Fatalf("step retry: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, out, stderr := d.finish(t, "grill", "1", "--summary", "late", "--failed"); code != 1 || out != "" {
		t.Errorf("finish of attempt 1 after the retry: exit %d, %q, stderr %s; want 1", code, out, stderr)
	}
	if got := d.attempts(t); len(got) != 2 || got[1].Outcome != "" {
		t.Errorf("attempts = %+v", got)
	}
}

func TestStepFinishUsage(t *testing.T) {
	d := runDirector{store: "s", config: "c", state: "st"}
	finish := func(extra ...string) []string {
		return append([]string{"step", "finish"}, d.flags(append([]string{"--task", "t-1", "--step", "grill", "--attempt", "1"}, extra...)...)...)
	}
	for name, args := range map[string][]string{
		"without store":          {"step", "finish", "--config", "c", "--state", "st", "--repo", "app", "--task", "t-1", "--step", "grill", "--attempt", "1", "--summary", "s", "--failed"},
		"without task":           append([]string{"step", "finish"}, d.flags("--step", "grill", "--attempt", "1", "--summary", "s", "--failed")...),
		"without summary":        finish("--output", "notes=n.md"),
		"with an empty summary":  finish("--summary", " ", "--output", "notes=n.md"),
		"without output":         finish("--summary", "s"),
		"failed with an output":  finish("--summary", "s", "--failed", "--output", "notes=n.md"),
		"output without a port":  finish("--summary", "s", "--output", "n.md"),
		"output without a file":  finish("--summary", "s", "--output", "notes="),
		"output with a bad port": finish("--summary", "s", "--output", "Bad Port=n.md"),
		"one port twice":         finish("--summary", "s", "--output", "notes=n.md", "--output", "notes=m.md"),
		"a bad number":           append([]string{"step", "finish"}, d.flags("--task", "t-1", "--step", "grill", "--attempt", "0", "--summary", "s", "--failed")...),
		"a bad id":               append([]string{"step", "finish"}, d.flags("--task", "T 1", "--step", "grill", "--attempt", "1", "--summary", "s", "--failed")...),
		"an argument":            finish("--summary", "s", "--failed", "extra"),
		"an unknown flag":        finish("--summary", "s", "--failed", "--force"),
	} {
		if code, out, _ := d.run(t, args...); code != 2 || out != "" {
			t.Errorf("%s: exit %d, %q; want 2", name, code, out)
		}
	}
	if _, out, _ := d.run(t, "help"); !strings.Contains(out, "step finish") {
		t.Errorf("help does not list step finish:\n%s", out)
	}
}

// TestPublishedStageResultRecovers: a step finish that stopped after publishing the grill's result, before its
// advance, leaves a result no launcher announces. Whether the stage message is still unread or was acknowledged,
// director status lists the exact step done that takes it, and that or task start moves the task on to the plan.
func TestPublishedStageResultRecovers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ack     bool
		recover func(d runDirector, flags, result string) []string
	}{
		{"unread, step done", false, func(d runDirector, flags, result string) []string {
			return append(append([]string{"step", "done"}, d.flags(strings.Fields(flags)...)...), "--result", result)
		}},
		{"acknowledged, task start", true, func(d runDirector, _, _ string) []string {
			return append([]string{"task", "start"}, d.flags("t-1")...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newRunDirectorWith(t, "grill.toml")
			if code, _, stderr := d.run(t, append([]string{"task", "start"}, d.flags("t-1")...)...); code != 0 {
				t.Fatalf("task start: exit %d, stderr %s", code, stderr)
			}
			file, _ := grillNotesFile(t)
			f := advanceFlags{store: d.store, config: d.config, state: d.state, repo: "app"}
			k := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "grill", Attempt: 1}
			if err := finishStage(f, k, "Agreed.", stageOutputs{{"notes", file}}, false); err != nil {
				t.Fatal(err)
			}
			if tc.ack {
				if code, _, stderr := d.run(t, "messages", "ack", "--store", d.store, "director", "app.t-1.stage.grill-1"); code != 0 {
					t.Fatalf("messages ack: exit %d, stderr %s", code, stderr)
				}
			}
			st, err := readDirectorStatus(context.Background(), d.store, d.state, nil)
			if err != nil {
				t.Fatal(err)
			}
			result := d.result("grill", 1)
			flags := "--repo app --task t-1 --step grill --attempt 1"
			i := slices.IndexFunc(st.NeedsMe, func(it needsMeItem) bool { return it.Kind == "advance" && it.Step == "grill" })
			if i < 0 || st.NeedsMe[i].Command != "guildhall step done "+flags+" --result "+result {
				t.Fatalf("needs_me = %+v, want an advance taking grill 1's result with step done", st.NeedsMe)
			}
			if code, out, stderr := d.run(t, tc.recover(d, flags, result)...); code != 0 || out != `{"wait":"attempt","step":"plan","attempt":1}`+"\n" {
				t.Fatalf("recovery: exit %d, %q, stderr %s; want the plan's attempt", code, out, stderr)
			}
			if got := d.attempts(t); len(got) != 2 || got[0].Outcome != store.AttemptSucceeded {
				t.Errorf("attempts = %+v, want grill 1 succeeded and the plan's attempt", got)
			}
			d.waitResult(t, "plan", 1)
		})
	}
}
