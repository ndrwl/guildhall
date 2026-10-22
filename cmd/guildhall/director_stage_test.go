package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestDirectorGrillRunsEndToEnd: the built binary runs task app/t-1 on the fake-grill workflow, whose grill is a
// Director stage, with this test playing the Director: task start hands it the grill in a director-stage message,
// the test reads the prompt that message names, writes its notes and runs step finish, and the task goes on through
// the fake Artisans' plan and build and the Guildmaster's answers to the merge decision. The plan is given the
// notes the Director agreed as context, and the timeline records the grill as an attempt like any other.
func TestDirectorGrillRunsEndToEnd(t *testing.T) {
	d := newBinDirector(t, filepath.Join(runCopy(t), "grill.toml"))
	d.newTask()
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"director-stage","step":"grill","attempt":1}`)

	s, err := store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	m, err := (&messaging.StoreMailbox{Store: s}).Read("app.t-1.stage.grill-1")
	_ = s.Close()
	if err != nil || m.Type != "director-stage" {
		t.Fatalf("stage message %+v, %v", m, err)
	}
	prompt := regexp.MustCompile(`(?m)^Prompt: (.+)$`).FindStringSubmatch(m.Text)
	if prompt == nil {
		t.Fatalf("stage message names no prompt:\n%s", m.Text)
	}
	text, err := os.ReadFile(prompt[1])
	if err != nil || !strings.Contains(string(text), "Step: grill (grill version 1)") {
		t.Fatalf("prompt %s: %v\n%s", prompt[1], err, text)
	}

	notes := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(notes, []byte("# Grill notes\n\n1. Sort by id? Yes.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.want(d.advance([]string{"step", "finish"}, "--task", "t-1", "--step", "grill", "--attempt", "1", "--summary", "Agreed: sort by id.", "--output", "notes="+notes),
		`{"wait":"attempt","step":"plan","attempt":1}`)
	d.want(d.done("plan", 1), `{"wait":"decision","step":"accept","decision":"accept-1"}`)
	d.want(d.answer("accept-1"), `{"wait":"attempt","step":"build","attempt":1}`)
	d.want(d.done("build", 1), `{"wait":"decision","step":"sign-off","decision":"sign-off-1"}`)
	d.want(d.answer("sign-off-1"), `{"wait":"decision","step":"deliver","decision":"deliver-1"}`)

	manifest, err := os.ReadFile(filepath.Join(d.state, "app", "t-1", "plan-1", "prompt", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `"port":"context","path":"tasks/app/t-1/grill-1/notes.md"`) {
		t.Errorf("plan manifest does not carry the grill notes as context:\n%s", manifest)
	}
	timeline := d.run("", "task", "timeline", "--store", d.store, "--repo", "app", "t-1", "--json")
	if !strings.Contains(timeline, `"step":"grill","number":1,"outcome":"succeeded"`) {
		t.Errorf("timeline does not record the grill attempt:\n%s", timeline)
	}
}
