package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestTaskContextAddAndList: add records an entry from --text or --file and prints its receipt, a rerun prints the
// same receipt and writes nothing, and list, show and timeline print the entries.
func TestTaskContextAddAndList(t *testing.T) {
	path := goldenStore(t)
	first := "Guildmaster: plain `guildhall director start` must record the pane."
	code, out, stderr := runTask(t, "context", "add", "--store", path, "--repo", "app", "--text", first, "t-2")
	if code != 0 || stderr != "" {
		t.Fatalf("add --text: exit %d, stderr %s", code, stderr)
	}
	var rc store.Receipt
	if err := json.Unmarshal([]byte(out), &rc); err != nil || rc.Record != "task-context/1" || rc.Operation != "add-task-context" ||
		rc.MutationID != mutationID("task-context", "app", "t-2", digest.Of([]byte(first)).Hex()) {
		t.Fatalf("add receipt = %+v, %v", rc, err)
	}
	if code, again, _ := runTask(t, "context", "add", "--repo", "app", "t-2", "--text", first, "--store", path); code != 0 || again != out {
		t.Fatalf("rerun: exit %d, %s, want %s", code, again, out)
	}
	file := filepath.Join(t.TempDir(), "context.md")
	second := "Supersedes 1:\n```sh\nguildhall director start --copy ./\n```\n"
	writeFile(t, file, second)
	if code, out, stderr := runTask(t, "context", "add", "--store", path, "--repo", "app", "--file", file, "t-2"); code != 0 || !strings.Contains(out, `"record":"task-context/2"`) {
		t.Fatalf("add --file: exit %d, %s, stderr %s", code, out, stderr)
	}

	code, out, stderr = runTask(t, "context", "list", "--store", path, "--repo", "app", "--json", "t-2")
	var entries []store.TaskContext
	if code != 0 || json.Unmarshal([]byte(out), &entries) != nil || len(entries) != 2 || entries[0].Text != first || entries[1].Text != second ||
		entries[1].Number != 2 || entries[1].Author != "director" || entries[1].AddedAt.IsZero() {
		t.Fatalf("list: exit %d, %s, stderr %s", code, out, stderr)
	}
	if code, out, _ := runTask(t, "context", "list", "--store", path, "--repo", "app", "--json", "t-1"); code != 0 || out != "[]\n" {
		t.Errorf("list of a task without context: exit %d, %q", code, out)
	}
	if code, _, stderr := runTask(t, "context", "list", "--store", path, "--repo", "app", "--json", "t-9"); code != 1 || !strings.Contains(stderr, store.ErrNotFound.Error()) {
		t.Errorf("list of an unknown task: exit %d, stderr %q", code, stderr)
	}

	code, out, _ = runTask(t, "show", "--store", path, "--repo", "app", "--json", "t-2")
	var detail struct {
		Context []store.TaskContext `json:"context"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &detail) != nil || len(detail.Context) != 2 || detail.Context[0] != entries[0] {
		t.Errorf("show: exit %d, %s", code, out)
	}
	code, out, _ = runTask(t, "timeline", "--store", path, "--repo", "app", "--json", "t-2")
	var tl struct {
		Context []store.TaskContext `json:"context"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &tl) != nil || len(tl.Context) != 2 || tl.Context[0] != entries[0] || tl.Context[1] != entries[1] {
		t.Errorf("timeline: exit %d, %s", code, out)
	}
}

// TestTaskContextReadsReadOnly: list, show and timeline open the store read-only, so they run beside another reader.
func TestTaskContextReadsReadOnly(t *testing.T) {
	path := goldenStore(t)
	if code, _, stderr := runTask(t, "context", "add", "--store", path, "--repo", "app", "--text", "Keep it quiet.", "t-2"); code != 0 {
		t.Fatalf("add: %s", stderr)
	}
	s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, sub := range [][]string{{"context", "list"}, {"show"}, {"timeline"}} {
		if code, _, stderr := runTask(t, append(sub, "--store", path, "--repo", "app", "--json", "t-2")...); code != 0 {
			t.Errorf("%v beside a reader: exit %d, stderr %s", sub, code, stderr)
		}
	}
}

// TestTaskContextRefusals: usage errors exit 2 with the usage, and the store's refusals exit 1.
func TestTaskContextRefusals(t *testing.T) {
	path := goldenStore(t)
	file := filepath.Join(t.TempDir(), "context.md")
	writeFile(t, file, "Words.")
	for name, args := range map[string][]string{
		"no subcommand":          {},
		"unknown subcommand":     {"remove", "--store", path, "--repo", "app", "t-2"},
		"text and file":          {"add", "--store", path, "--repo", "app", "--text", "Words.", "--file", file, "t-2"},
		"neither text nor file":  {"add", "--store", path, "--repo", "app", "t-2"},
		"file from stdin":        {"add", "--store", path, "--repo", "app", "--file", "-", "t-2"},
		"no repo":                {"add", "--store", path, "--text", "Words.", "t-2"},
		"no task":                {"add", "--store", path, "--repo", "app", "--text", "Words."},
		"two tasks":              {"add", "--store", path, "--repo", "app", "--text", "Words.", "t-1", "t-2"},
		"json on add":            {"add", "--store", path, "--repo", "app", "--text", "Words.", "--json", "t-2"},
		"text on list":           {"list", "--store", path, "--repo", "app", "--text", "Words.", "--json", "t-2"},
		"list without json":      {"list", "--store", path, "--repo", "app", "t-2"},
		"flags after terminator": {"list", "--store", path, "--repo", "app", "--", "t-2", "--json"},
		"no store":               {"add", "--repo", "app", "--text", "Words.", "t-2"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, stderr := runTask(t, append([]string{"context"}, args...)...)
			if code != 2 || out != "" || !strings.Contains(stderr, "guildhall task context add") {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
			}
		})
	}
	for name, args := range map[string][]string{
		"blank text":   {"--text", " ", "t-2"},
		"unknown task": {"--text", "Words.", "t-9"},
		"missing file": {"--file", filepath.Join(t.TempDir(), "absent.md"), "t-2"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, stderr := runTask(t, append([]string{"context", "add", "--store", path, "--repo", "app"}, args...)...)
			if code != 1 || out != "" || !strings.Contains(stderr, "guildhall: task context add:") {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
			}
		})
	}
	if code, out, stderr := runTask(t, "context", "list", "--store", path, "--repo", "app", "--json", "t-2"); code != 0 || out != "[]\n" {
		t.Errorf("after refusals: exit %d, %q, stderr %s", code, out, stderr)
	}
}
