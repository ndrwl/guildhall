package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const exampleWorkflow = "../../examples/workflows/human-review.json"

// approvalFiles returns the example workflow and approval words written the way a Windows editor
// might: in a directory with a space and with CRLF endings.
func approvalFiles(t *testing.T, words string) (workflowFile, wordsFile string) {
	t.Helper()
	data, err := os.ReadFile(exampleWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "workflow drafts")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	workflowFile = filepath.Join(dir, "human-review.json")
	wordsFile = filepath.Join(dir, "words.txt")
	crlf := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n", "\r\n")
	if err := os.WriteFile(workflowFile, []byte(crlf), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wordsFile, []byte(words), 0o600); err != nil {
		t.Fatal(err)
	}
	return workflowFile, wordsFile
}

func exampleDigest(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(exampleWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return def.Digest()
}

func runWorkflow(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(append([]string{"workflow"}, args...))
	return code, stdout.String(), stderr.String()
}

func storeApprovals(t *testing.T, path string) []store.WorkflowApproval {
	t.Helper()
	s, err := store.Open(path, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := s.WorkflowApprovals()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestWorkflowApproveRecordsDigestAndWords(t *testing.T) {
	path := writeStore(t)
	words := "Approved: human review replaces automated review on app.\r\n"
	file, wordsFile := approvalFiles(t, words)

	code, stdout, stderr := runWorkflow(t, "", "approve", "--store", path, "--file", file, "--words-file", wordsFile)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	want := exampleDigest(t)
	receipt := decodeJSON[store.Receipt](t, stdout)
	if receipt.MutationID != "workflow-approval:"+want || receipt.Operation != "approve-workflow" {
		t.Fatalf("receipt = %+v", receipt)
	}
	got := storeApprovals(t, path)
	if len(got) != 1 || got[0].Name != "human-review" || got[0].Digest != want || got[0].Words != words {
		t.Fatalf("approvals = %+v, want human-review %s with the exact words", got, want)
	}

	// A retry prints the original receipt; different words for the same digest are refused.
	if code, again, stderr := runWorkflow(t, "", "approve", "--store", path, "--file", file, "--words-file", wordsFile); code != 0 || again != stdout {
		t.Fatalf("replay: exit %d, stdout %q, stderr %q; want %q", code, again, stderr, stdout)
	}
	code, out, stderr := runWorkflow(t, "Approved for the pilot only.\n", "approve", "--store", path, "--file", file, "--words-file", "-")
	if code != 1 || out != "" || !strings.Contains(stderr, "mutation id was already used") {
		t.Fatalf("conflict: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if got := storeApprovals(t, path); len(got) != 1 || got[0].Words != words {
		t.Fatalf("approvals after conflict = %+v", got)
	}
}

func TestWorkflowApproveRefusesBeforeWriting(t *testing.T) {
	path := writeStore(t)
	file, wordsFile := approvalFiles(t, "Approved.\n")
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	invalid := write("invalid.json", `{"schema": "guildhall.workflow/1", "name": "broken", "steps": [{"id": "build", "type": "build", "version": 1}]}`)
	unknownField := write("unknown.json", `{"schema": "guildhall.workflow/1", "name": "x", "run": "rm -rf /", "steps": []}`)
	shipped := write("default.json", mustReadFile(t, "../workflow/default.json"))
	blank := write("blank.txt", " \r\n")

	cases := []struct {
		name, stdin string
		args        []string
		wantErr     string
	}{
		{"invalid workflow", "", []string{"--file", invalid, "--words-file", wordsFile}, `required input "spec" is not wired`},
		{"unknown field", "", []string{"--file", unknownField, "--words-file", wordsFile}, `unknown field "run"`},
		{"missing workflow file", "", []string{"--file", filepath.Join(dir, "missing.json"), "--words-file", wordsFile}, "workflow file"},
		{"shipped workflow", "", []string{"--file", shipped, "--words-file", wordsFile}, "approved by this release"},
		{"blank words", "", []string{"--file", file, "--words-file", blank}, "words must not be empty"},
		{"blank stdin words", "\n", []string{"--file", file, "--words-file", "-"}, "words must not be empty"},
		{"missing words file", "", []string{"--file", file, "--words-file", filepath.Join(dir, "missing.txt")}, "words file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"approve", "--store", path}, tc.args...)
			code, stdout, stderr := runWorkflow(t, tc.stdin, args...)
			if code != 1 || stdout != "" || !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit 1 mentioning %q", code, stdout, stderr, tc.wantErr)
			}
			if got := storeApprovals(t, path); len(got) != 0 {
				t.Fatalf("approvals = %+v, want none", got)
			}
		})
	}

	t.Run("missing store", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.store")
		code, _, stderr := runWorkflow(t, "", "approve", "--store", missing, "--file", file, "--words-file", wordsFile)
		if code != 1 || !strings.Contains(stderr, "no store at") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatalf("missing store was created: %v", err)
		}
	})
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWorkflowApprovalsList(t *testing.T) {
	path := writeStore(t)
	s, err := store.Open(path, store.Options{Now: func() time.Time { return time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveWorkflow(store.WorkflowApproval{Name: "human-review", Digest: exampleDigest(t), Words: "Approved: human review on app.\n"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	expand := func(golden string) string {
		return strings.NewReplacer("{{DEFAULT_DIGEST}}", def.Digest(), "{{EXAMPLE_DIGEST}}", exampleDigest(t)).Replace(golden)
	}

	code, stdout, stderr := runWorkflow(t, "", "approvals", "--store", path, "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if want := expand(mustReadFile(t, "testdata/workflow-approvals.json")); stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}

	code, stdout, stderr = runWorkflow(t, "", "approvals", "--store", path)
	if code != 0 {
		t.Fatalf("text: exit %d, stderr %q", code, stderr)
	}
	want := expand("release      default       {{DEFAULT_DIGEST}}\n" +
		"guildmaster  human-review  {{EXAMPLE_DIGEST}}  2026-10-20T09:30:00Z\n")
	if stdout != want {
		t.Errorf("text stdout =\n%s\nwant\n%s", stdout, want)
	}
}

func TestWorkflowUsageErrors(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"no subcommand", nil, "missing workflow subcommand"},
		{"validate is not here yet", []string{"validate", "x.json"}, `unknown workflow subcommand "validate"`},
		{"approve without store", []string{"approve", "--file", "w.json", "--words-file", "-"}, "--store is required"},
		{"approve without file", []string{"approve", "--store", "s", "--words-file", "-"}, "--file is required"},
		{"approve without words", []string{"approve", "--store", "s", "--file", "w.json"}, "--words-file is required"},
		{"approve argument", []string{"approve", "--store", "s", "--file", "w.json", "--words-file", "-", "extra"}, "takes no arguments"},
		{"approvals with file", []string{"approvals", "--store", "s", "--file", "w.json"}, "--file and --words-file apply only to workflow approve"},
		{"approve with json", []string{"approve", "--store", "s", "--file", "w.json", "--words-file", "-", "--json"}, "--json applies only to workflow approvals"},
		{"unknown flag", []string{"approvals", "--store", "s", "--yaml"}, "flag provided but not defined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runWorkflow(t, "", tc.args...)
			if code != 2 || stdout != "" || !strings.Contains(stderr, tc.wantErr) || !strings.Contains(stderr, "guildhall workflow approve") {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit 2 mentioning %q and usage", code, stdout, stderr, tc.wantErr)
			}
		})
	}
}
