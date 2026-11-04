package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const barSpec = "# A spec\n\n## Summary\n\n- **Goal:** make the **thinking** the main thing on the `Workbench` pane.\n- **Size:** 2 pull requests.\n\n" +
	"## Slices\n\n1. **One**: the first. Small.\n2. **Two**: the second. Small.\n"

// barCopy is a copy whose config has a [web] table, and whose store holds app/t-1 with a title and detail; with
// accepted, its plan-1 spec is accepted by the answered approval accept-1 and recorded as accept-1-spec.
func barCopy(t *testing.T, accepted bool) (dir string, at launch.Attempt) {
	t.Helper()
	dir = filepath.Dir(writeDirector(t, directorTOML))
	cfg := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg, string(b)+"\n[web]\naddress = \""+testWebHost+"\"\n")
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	must := func(_ store.Receipt, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.CreateTask("m-1", store.Task{Repository: "app", ID: "t-1", Title: "Show the bar", Detail: "The Guildmaster asked for a bar. It names the task.",
		Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64)}))
	state := filepath.Join(dir, "state")
	if accepted {
		path := "tasks/app/t-1/plan-1/spec.md"
		file := filepath.Join(state, "documents", filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, file, barSpec)
		sum := digest.Of([]byte(barSpec))
		must(s.PutDocument("m-2", store.DocumentRef{Repository: "app", TaskID: "t-1", Name: "plan-1", Path: path, SHA256: sum.Hex()}))
		must(s.OpenDecision("m-3", store.Decision{Repository: "app", TaskID: "t-1", ID: "accept-1", Kind: store.DecisionApproval,
			Subject: &store.Subject{Kind: store.SubjectDocument, Digest: string(sum)}, Options: []string{"approved", "changes_requested"}, Question: "Accept?"}))
		must(s.AnswerDecision("m-4", "app", "t-1", "accept-1", "approved"))
		must(s.PutDocument("m-5", store.DocumentRef{Repository: "app", TaskID: "t-1", Name: "accept-1-spec", Path: path, SHA256: sum.Hex()}))
	}
	return dir, launch.Attempt{Store: filepath.Join(dir, "director.store"), StateDir: state,
		Key: artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build--s2", Attempt: 1}}
}

// TestWithBarFromTheAcceptedSpec: the bridge's bar fields come from the store, the accepted spec's goal and slice
// count, the copy's [web] address and the prompt file's time.
func TestWithBarFromTheAcceptedSpec(t *testing.T) {
	dir, at := barCopy(t, true)
	prompt := filepath.Join(t.TempDir(), "prompt.md")
	writeFile(t, prompt, "prompt")
	start := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	if err := os.Chtimes(prompt, start, start); err != nil {
		t.Fatal(err)
	}
	b := withBar(launch.Bridge{Attempt: at}, dir, prompt)
	if b.Title != "Show the bar" || b.Asked != "make the thinking the main thing on the Workbench pane." || b.Slices != 2 ||
		b.URL != "http://"+testWebHost+"/app/t-1" || !b.Start.Equal(start) {
		t.Errorf("bridge = title %q, asked %q, slices %d, url %q, start %v", b.Title, b.Asked, b.Slices, b.URL, b.Start)
	}
	// Without --copy the config beside the store is read.
	if b := withBar(launch.Bridge{Attempt: at}, "", prompt); b.URL != "http://"+testWebHost+"/app/t-1" {
		t.Errorf("url without --copy = %q", b.URL)
	}
}

// TestWithBarBeforeASpec: before a spec is accepted, Asked: is the detail's first sentence; with no [web] table
// there is no URL, and a store or prompt that does not read leaves those fields out without failing.
func TestWithBarBeforeASpec(t *testing.T) {
	dir, at := barCopy(t, false)
	b := withBar(launch.Bridge{Attempt: at}, dir, filepath.Join(dir, "missing-prompt.md"))
	if b.Asked != "The Guildmaster asked for a bar." || b.Slices != 0 || !b.Start.IsZero() {
		t.Errorf("bridge = asked %q, slices %d, start %v", b.Asked, b.Slices, b.Start)
	}
	writeFile(t, filepath.Join(dir, "config.toml"), "not toml [")
	at.Store = filepath.Join(dir, "no.store")
	if b := withBar(launch.Bridge{Attempt: at}, dir, ""); b.URL != "" || b.Title != "" || b.Asked != "" {
		t.Errorf("unreadable copy = %+v", b)
	}
}

func TestFirstSentence(t *testing.T) {
	for in, want := range map[string]string{
		"One. Two.":                 "One.",
		"Is it? Yes.":               "Is it?",
		"No stop at all":            "No stop at all",
		"Line one\nstill one. Next": "Line one still one.",
		"Para one\n\nPara two.":     "Para one",
		"v1.2 is out. Next":         "v1.2 is out.",
		"":                          "",
	} {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}
