package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
)

// linkedText is lib/t-1's document plan-1-spec, which these tests link to app/t-2.
const linkedText = "# Bump the parser\n\nKeep the old grammar behind a flag.\n"

// linkStore is goldenStore with lib/t-1's plan-1-spec recorded and its file written under the returned state
// directory's documents.
func linkStore(t *testing.T) (path, state string) {
	t.Helper()
	path, state = goldenStore(t), t.TempDir()
	d := store.DocumentRef{Repository: "lib", TaskID: "t-1", Name: "plan-1-spec", Path: "tasks/lib/t-1/plan-1/spec.md", SHA256: digest.Of([]byte(linkedText)).Hex()}
	file := filepath.Join(state, "documents", filepath.FromSlash(d.Path))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, linkedText)
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.PutDocument("doc-lib-t-1", d); err != nil {
		t.Fatal(err)
	}
	return path, state
}

// TestTaskLinkAddRemoveList: add links a document from --text or --file and prints its receipt, a rerun prints the
// same receipt and writes nothing, remove unlinks it, a second add links it again as a new record, and list, show
// and timeline print every record.
func TestTaskLinkAddRemoveList(t *testing.T) {
	path, state := linkStore(t)
	first := "Guildmaster: follow `lib/t-1`'s spec."
	add := []string{"link", "add", "--store", path, "--state", state, "--repo", "app", "--document", "lib/t-1/plan-1-spec", "--text", first, "t-2"}
	code, out, stderr := runTask(t, add...)
	if code != 0 || stderr != "" {
		t.Fatalf("add --text: exit %d, stderr %s", code, stderr)
	}
	var rc store.Receipt
	if err := json.Unmarshal([]byte(out), &rc); err != nil || rc.Record != "task-link/1" || rc.Operation != "add-task-link" ||
		rc.MutationID != "task-link:app:t-2:add:lib:t-1:plan-1-spec:0" {
		t.Fatalf("add receipt = %+v, %v", rc, err)
	}
	if code, again, _ := runTask(t, "link", "add", "--repo", "app", "t-2", "--text", first, "--document", "lib/t-1/plan-1-spec", "--state", state, "--store", path); code != 0 || again != out {
		t.Fatalf("rerun: exit %d, %s, want %s", code, again, out)
	}
	file := filepath.Join(t.TempDir(), "why.md")
	second := "Guildmaster: the spec is settled now;\n```text\nno need\n```\n"
	writeFile(t, file, second)
	if code, out, stderr := runTask(t, "link", "remove", "--store", path, "--repo", "app", "--document", "lib/t-1/plan-1-spec", "--file", file, "t-2"); code != 0 ||
		!strings.Contains(out, `"record":"task-link/2"`) || !strings.Contains(out, `"mutation_id":"task-link:app:t-2:remove:lib:t-1:plan-1-spec:1"`) {
		t.Fatalf("remove --file: exit %d, %s, stderr %s", code, out, stderr)
	}
	if code, out, stderr := runTask(t, add...); code != 0 || !strings.Contains(out, `"record":"task-link/3"`) {
		t.Fatalf("add again: exit %d, %s, stderr %s", code, out, stderr)
	}

	code, out, stderr = runTask(t, "link", "list", "--store", path, "--repo", "app", "--json", "t-2")
	var links []store.TaskLink
	if code != 0 || json.Unmarshal([]byte(out), &links) != nil || len(links) != 3 {
		t.Fatalf("list: exit %d, %s, stderr %s", code, out, stderr)
	}
	source := store.DocumentRef{Repository: "lib", TaskID: "t-1", Name: "plan-1-spec", Path: "tasks/lib/t-1/plan-1/spec.md", SHA256: digest.Of([]byte(linkedText)).Hex()}
	for i, l := range links {
		op, text := []store.TaskLinkOp{store.TaskLinkAdd, store.TaskLinkRemove, store.TaskLinkAdd}[i], []string{first, second, first}[i]
		if l.Number != i+1 || l.Op != op || l.Source != source || l.Text != text || l.Author != "director" || l.AddedAt.IsZero() {
			t.Errorf("record %d = %+v", i+1, l)
		}
	}
	if code, out, _ := runTask(t, "link", "list", "--store", path, "--repo", "app", "--json", "t-1"); code != 0 || out != "[]\n" {
		t.Errorf("list of a task without links: exit %d, %q", code, out)
	}
	if code, _, stderr := runTask(t, "link", "list", "--store", path, "--repo", "app", "--json", "t-9"); code != 1 || !strings.Contains(stderr, store.ErrNotFound.Error()) {
		t.Errorf("list of an unknown task: exit %d, stderr %q", code, stderr)
	}

	code, out, _ = runTask(t, "show", "--store", path, "--repo", "app", "--json", "t-2")
	var detail struct {
		Links []store.TaskLink `json:"links"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &detail) != nil || len(detail.Links) != 3 || detail.Links[1] != links[1] {
		t.Errorf("show: exit %d, %s", code, out)
	}
	code, out, _ = runTask(t, "timeline", "--store", path, "--repo", "app", "--json", "t-2")
	var tl struct {
		Links []store.TaskLink `json:"links"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &tl) != nil || len(tl.Links) != 3 || tl.Links[0] != links[0] || tl.Links[2] != links[2] {
		t.Errorf("timeline: exit %d, %s", code, out)
	}
	app, stdout, stderr2 := newTestApp(nil, nil)
	code, out = app.Run([]string{"store", "doctor", path}), stdout.String()
	if code != 0 || stderr2.String() != "" || !strings.Contains(out, "repository app: tasks 2,") || !strings.Contains(out, "task links 3,") {
		t.Errorf("store doctor: exit %d, %s", code, out)
	}
}

// TestTaskLinkReadsReadOnly: list, show and timeline open the store read-only, so they run beside another reader.
func TestTaskLinkReadsReadOnly(t *testing.T) {
	path, state := linkStore(t)
	if code, _, stderr := runTask(t, "link", "add", "--store", path, "--state", state, "--repo", "app", "--document", "lib/t-1/plan-1-spec", "--text", "Read it.", "t-2"); code != 0 {
		t.Fatalf("add: %s", stderr)
	}
	s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, sub := range [][]string{{"link", "list"}, {"show"}, {"timeline"}} {
		if code, _, stderr := runTask(t, append(sub, "--store", path, "--repo", "app", "--json", "t-2")...); code != 0 {
			t.Errorf("%v beside a reader: exit %d, stderr %s", sub, code, stderr)
		}
	}
}

// TestTaskLinkRefusals: usage errors exit 2 with the usage, and the store's refusals, and a source file missing or
// changed at add time, exit 1 and write nothing.
func TestTaskLinkRefusals(t *testing.T) {
	path, state := linkStore(t)
	file := filepath.Join(t.TempDir(), "why.md")
	writeFile(t, file, "Words.")
	doc := "lib/t-1/plan-1-spec"
	for name, args := range map[string][]string{
		"no subcommand":          {},
		"unknown subcommand":     {"move", "--store", path, "--repo", "app", "t-2"},
		"text and file":          {"add", "--store", path, "--state", state, "--repo", "app", "--document", doc, "--text", "Words.", "--file", file, "t-2"},
		"neither text nor file":  {"remove", "--store", path, "--repo", "app", "--document", doc, "t-2"},
		"file from stdin":        {"add", "--store", path, "--state", state, "--repo", "app", "--document", doc, "--file", "-", "t-2"},
		"no document":            {"add", "--store", path, "--state", state, "--repo", "app", "--text", "Words.", "t-2"},
		"two-part document":      {"add", "--store", path, "--state", state, "--repo", "app", "--document", "lib/t-1", "--text", "Words.", "t-2"},
		"spaced document":        {"add", "--store", path, "--state", state, "--repo", "app", "--document", "lib/t-1/plan-1 spec", "--text", "Words.", "t-2"},
		"no state on add":        {"add", "--store", path, "--repo", "app", "--document", doc, "--text", "Words.", "t-2"},
		"state on remove":        {"remove", "--store", path, "--state", state, "--repo", "app", "--document", doc, "--text", "Words.", "t-2"},
		"no repo":                {"add", "--store", path, "--state", state, "--document", doc, "--text", "Words.", "t-2"},
		"no task":                {"add", "--store", path, "--state", state, "--repo", "app", "--document", doc, "--text", "Words."},
		"two tasks":              {"add", "--store", path, "--state", state, "--repo", "app", "--document", doc, "--text", "Words.", "t-1", "t-2"},
		"json on add":            {"add", "--store", path, "--state", state, "--repo", "app", "--document", doc, "--text", "Words.", "--json", "t-2"},
		"document on list":       {"list", "--store", path, "--repo", "app", "--document", doc, "--json", "t-2"},
		"list without json":      {"list", "--store", path, "--repo", "app", "t-2"},
		"flags after terminator": {"list", "--store", path, "--repo", "app", "--", "t-2", "--json"},
		"no store":               {"remove", "--repo", "app", "--document", doc, "--text", "Words.", "t-2"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, stderr := runTask(t, append([]string{"link"}, args...)...)
			if code != 2 || out != "" || !strings.Contains(stderr, "guildhall task link add") {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
			}
		})
	}

	changed := t.TempDir()
	changedFile := filepath.Join(changed, "documents", "tasks", "lib", "t-1", "plan-1", "spec.md")
	if err := os.MkdirAll(filepath.Dir(changedFile), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, changedFile, "Other words.\n")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"blank text":         {[]string{"add", "--state", state, "--document", doc, "--text", " ", "t-2"}, "must not be empty"},
		"unknown task":       {[]string{"add", "--state", state, "--document", doc, "--text", "Words.", "t-9"}, store.ErrNotFound.Error()},
		"missing text file":  {[]string{"add", "--state", state, "--document", doc, "--file", filepath.Join(t.TempDir(), "absent.md"), "t-2"}, "absent.md"},
		"unknown document":   {[]string{"add", "--state", state, "--document", "lib/t-1/plan-9-spec", "--text", "Words.", "t-2"}, "lib/t-1/plan-9-spec"},
		"missing source":     {[]string{"add", "--state", t.TempDir(), "--document", doc, "--text", "Words.", "t-2"}, "lib/t-1/plan-1-spec: file tasks/lib/t-1/plan-1/spec.md is missing"},
		"changed source":     {[]string{"add", "--state", changed, "--document", doc, "--text", "Words.", "t-2"}, "lib/t-1/plan-1-spec: file tasks/lib/t-1/plan-1/spec.md holds sha256:" + digest.Of([]byte("Other words.\n")).Hex()},
		"self link":          {[]string{"add", "--state", state, "--document", "app/t-2/spec", "--text", "Words.", "t-2"}, "own document"},
		"remove when absent": {[]string{"remove", "--document", doc, "--text", "Words.", "t-2"}, "does not link lib/t-1/plan-1-spec"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, stderr := runTask(t, append(append([]string{"link"}, tc.args[0], "--store", path, "--repo", "app"), tc.args[1:]...)...)
			if code != 1 || out != "" || !strings.Contains(stderr, "guildhall: task link "+tc.args[0]+":") || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit %d, stdout %q, stderr %q, want %q", code, out, stderr, tc.want)
			}
		})
	}
	if code, out, stderr := runTask(t, "link", "list", "--store", path, "--repo", "app", "--json", "t-2"); code != 0 || out != "[]\n" {
		t.Errorf("after refusals: exit %d, %q, stderr %s", code, out, stderr)
	}
}
