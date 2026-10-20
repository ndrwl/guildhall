package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// decideStorePath returns a store with open decisions in two repositories and one answered decision.
func decideStorePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guildhall.store")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, ref := range [][2]string{{"lib", "t-1"}, {"app", "t-2"}, {"app", "t-1"}} {
		task := store.Task{Repository: ref[0], ID: ref[1], Title: "Task", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}
		if _, err := s.CreateTask("task:"+ref[0]+":"+ref[1], task); err != nil {
			t.Fatal(err)
		}
	}
	plan := &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("c", 64)}
	for _, d := range []store.Decision{
		{Repository: "lib", TaskID: "t-1", ID: "port", Kind: store.DecisionQuestion, Question: "Which port?"},
		{Repository: "app", TaskID: "t-2", ID: "merge", Kind: store.DecisionMerge, Subject: &store.Subject{Kind: store.SubjectHead, Digest: strings.Repeat("e", 40)}, Options: []string{"merge", "hold"}, Question: "Merge the branch?"},
		{Repository: "app", TaskID: "t-1", ID: "approve-plan", Kind: store.DecisionApproval, Subject: plan, Options: []string{"approve", "reject"}, Question: "Approve the plan?"},
		{Repository: "app", TaskID: "t-1", ID: "done-already", Kind: store.DecisionQuestion, Question: "Answered?"},
	} {
		if _, err := s.OpenDecision("open:"+d.Repository+":"+d.TaskID+":"+d.ID, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AnswerDecision("m-answered", "app", "t-1", "done-already", "Yes"); err != nil {
		t.Fatal(err)
	}
	return path
}

func runDecide(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(append([]string{"decide"}, args...))
	return code, stdout.String(), stderr.String()
}

// writeAnswer writes an answer file the way a Windows editor might: in a directory with a space, with a byte order mark and CRLF endings.
func writeAnswer(t *testing.T, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "answer files")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "answer.txt")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func decisionOf(t *testing.T, path, repo, task, id string) store.Decision {
	t.Helper()
	s, err := store.Open(path, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	all, err := s.Decisions(repo, task)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("no decision %s/%s/%s", repo, task, id)
	return store.Decision{}
}

func TestDecideListIsTheOpenInbox(t *testing.T) {
	path := decideStorePath(t)
	code, out, errOut := runDecide(t, "", "list", "--store", path)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "decide-list.json"))
	if err != nil {
		t.Fatal(err)
	}
	if out != strings.ReplaceAll(string(golden), "\r\n", "\n") {
		t.Fatalf("list output:\n%s\nwant golden:\n%s", out, golden)
	}

	code, out, errOut = runDecide(t, "", "list", "--store", path, "--repo", "lib")
	got := decodeJSON[[]store.Decision](t, out)
	if code != 0 || len(got) != 1 || got[0].ID != "port" {
		t.Fatalf("list --repo lib = %d %+v %s", code, got, errOut)
	}
	code, out, _ = runDecide(t, "", "list", "--store", path, "--repo", "none")
	if code != 0 || out != "[]\n" {
		t.Fatalf("list of a repository with no records = %d %q, want []", code, out)
	}
}

func TestDecideListPrefixOrder(t *testing.T) {
	path := decideStorePath(t)
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, id := range []string{"t-a", "t"} {
		task := store.Task{Repository: "app", ID: id, Title: "Task", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}
		if _, err := s.CreateTask("task:"+id, task); err != nil {
			t.Fatal(err)
		}
		for _, decision := range []string{"b", "a"} {
			d := store.Decision{Repository: "app", TaskID: id, ID: decision, Kind: store.DecisionQuestion, Question: "Proceed?"}
			if _, err := s.OpenDecision("open:"+id+":"+decision, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"", "app"} {
		args := []string{"list", "--store", path}
		want := "app/t/a,app/t/b,app/t-1/approve-plan,app/t-2/merge,app/t-a/a,app/t-a/b"
		if repo != "" {
			args = append(args, "--repo", repo)
		} else {
			want += ",lib/t-1/port"
		}
		code, out, errOut := runDecide(t, "", args...)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		var order []string
		for _, d := range decodeJSON[[]store.Decision](t, out) {
			order = append(order, d.Repository+"/"+d.TaskID+"/"+d.ID)
		}
		if strings.Join(order, ",") != want {
			t.Fatalf("repo %q: order = %v, want %s", repo, order, want)
		}
	}
}

func TestDecideShow(t *testing.T) {
	path := decideStorePath(t)
	code, out, errOut := runDecide(t, "", "show", "--store", path, "--repo", "app", "--task", "t-1", "--decision", "approve-plan")
	got := decodeJSON[store.Decision](t, out)
	if code != 0 || got.Kind != store.DecisionApproval || got.Subject == nil || len(got.Options) != 2 || got.Answered {
		t.Fatalf("show = %d %+v %s", code, got, errOut)
	}
	code, _, errOut = runDecide(t, "", "show", "--store", path, "--repo", "app", "--task", "t-1", "--decision", "missing")
	if code != 1 || !strings.Contains(errOut, "not found") {
		t.Fatalf("show missing = %d %q", code, errOut)
	}
}

func TestDecideAnswerRefusesNonOption(t *testing.T) {
	path := decideStorePath(t)
	before, _ := os.ReadFile(path)
	code, out, errOut := runDecide(t, "maybe later\n", "answer", "--store", path, "--repo", "app", "--task", "t-1", "--decision", "approve-plan", "--answer-file", "-")
	if code != 1 || out != "" || !strings.Contains(errOut, "approve") {
		t.Fatalf("non-option answer = %d %q %q", code, out, errOut)
	}
	if d := decisionOf(t, path, "app", "t-1", "approve-plan"); d.Answered {
		t.Fatalf("refused answer was recorded: %+v", d)
	}
	s, err := store.Open(path, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Receipt("decision-answer:app:t-1:approve-plan")
	_ = s.Close()
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused answer left a receipt: %v (store was %d bytes)", err, len(before))
	}
}

func TestDecideAnswerFromWindowsFile(t *testing.T) {
	path := decideStorePath(t)
	file := writeAnswer(t, "\ufeffapprove\r\n\r\nKeep the migration behind a flag.\r\n")
	args := []string{"answer", "--store", path, "--repo", "app", "--task", "t-1", "--decision", "approve-plan", "--answer-file", file}
	code, out, errOut := runDecide(t, "", args...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	first := decodeJSON[store.Receipt](t, out)
	if first.MutationID != "decision-answer:app:t-1:approve-plan" || first.Operation != "answer-decision" {
		t.Fatalf("receipt = %+v", first)
	}
	d := decisionOf(t, path, "app", "t-1", "approve-plan")
	want := "approve\n\nKeep the migration behind a flag.\n"
	if !d.Answered || d.Answer != want || d.Choice != "approve" || d.Note != "Keep the migration behind a flag." {
		t.Fatalf("decision = %+v; want answer %q", d, want)
	}

	// A retry of the same words prints the original receipt; different words are refused.
	code, out, errOut = runDecide(t, "", args...)
	if code != 0 || decodeJSON[store.Receipt](t, out) != first {
		t.Fatalf("replay = %d %q %s", code, out, errOut)
	}
	code, _, errOut = runDecide(t, "reject\n", "answer", "--store", path, "--repo", "app", "--task", "t-1", "--decision", "approve-plan", "--answer-file", "-")
	if code != 1 || !strings.Contains(errOut, "mutation id") {
		t.Fatalf("second answer = %d %q", code, errOut)
	}
}

func TestDecideAnswerFromStdin(t *testing.T) {
	path := decideStorePath(t)
	code, out, errOut := runDecide(t, "\ufeffmerge\r\nafter CI\r\n", "answer", "--store", path, "--repo", "app", "--task", "t-2", "--decision", "merge", "--answer-file", "-")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	_ = decodeJSON[store.Receipt](t, out)
	if d := decisionOf(t, path, "app", "t-2", "merge"); d.Answer != "merge\nafter CI\n" || d.Choice != "merge" || d.Note != "after CI" {
		t.Fatalf("decision = %+v", d)
	}
	// Free text answers a question without options.
	code, _, errOut = runDecide(t, "Use 8080.", "answer", "--store", path, "--repo", "lib", "--task", "t-1", "--decision", "port", "--answer-file", "-")
	if code != 0 || decisionOf(t, path, "lib", "t-1", "port").Answer != "Use 8080." {
		t.Fatalf("free answer = %d %s", code, errOut)
	}
}

func TestDecideAnswerRefusesUnreadableInput(t *testing.T) {
	path := decideStorePath(t)
	answer := func(stdin string) (int, string) {
		code, _, errOut := runDecide(t, stdin, "answer", "--store", path, "--repo", "lib", "--task", "t-1", "--decision", "port", "--answer-file", "-")
		return code, errOut
	}
	if code, errOut := answer("\xff\xfeu\x00s\x00e\x00"); code != 1 || !strings.Contains(errOut, "UTF-16") {
		t.Fatalf("UTF-16 answer = %d %q", code, errOut)
	}
	if code, errOut := answer(" \r\n"); code != 1 || !strings.Contains(errOut, "empty") {
		t.Fatalf("blank answer = %d %q", code, errOut)
	}
	if code, errOut := answer(strings.Repeat("x", store.MaxTextBytes+1)); code != 1 || !strings.Contains(errOut, "limit") {
		t.Fatalf("long answer = %d %q", code, errOut)
	}
	code, _, errOut := runDecide(t, "", "answer", "--store", path, "--repo", "lib", "--task", "t-1", "--decision", "port", "--answer-file", filepath.Join(t.TempDir(), "missing.txt"))
	if code != 1 || !strings.Contains(errOut, "answer") {
		t.Fatalf("missing answer file = %d %q", code, errOut)
	}
	if d := decisionOf(t, path, "lib", "t-1", "port"); d.Answered {
		t.Fatalf("refused input was recorded: %+v", d)
	}
}

func TestDecideLongIDsHashTheMutationID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guildhall.store")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	long := func(c string) string { return "a" + strings.Repeat(c, 62) }
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := s.CreateTask("m-1", store.Task{Repository: long("r"), ID: long("t"), Title: "T", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenDecision("m-2", store.Decision{Repository: long("r"), TaskID: long("t"), ID: long("d"), Kind: store.DecisionQuestion, Question: "Q?"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	code, out, errOut := runDecide(t, "yes", "answer", "--store", path, "--repo", long("r"), "--task", long("t"), "--decision", long("d"), "--answer-file", "-")
	if r := decodeJSON[store.Receipt](t, out); code != 0 || !strings.HasPrefix(r.MutationID, "decision-answer:sha256:") {
		t.Fatalf("long ids = %d %+v %s", code, r, errOut)
	}
}

func TestDecideUsageAndStoreErrors(t *testing.T) {
	path := decideStorePath(t)
	for name, args := range map[string][]string{
		"no subcommand":       nil,
		"unknown subcommand":  {"vote", "--store", path},
		"missing store":       {"list"},
		"positional argument": {"list", "--store", path, "extra"},
		"show without ids":    {"show", "--store", path, "--repo", "app"},
		"answer without file": {"answer", "--store", path, "--repo", "app", "--task", "t-1", "--decision", "approve-plan"},
		"file flag on list":   {"list", "--store", path, "--answer-file", "-"},
		"task flag on list":   {"list", "--store", path, "--task", "t-1"},
		"bad repository id":   {"list", "--store", path, "--repo", "App"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, errOut := runDecide(t, "", args...); code != 2 || !strings.Contains(errOut, "usage:") {
				t.Fatalf("exit %d, stderr %q; want usage error", code, errOut)
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "missing.store")
	if code, _, errOut := runDecide(t, "", "list", "--store", missing); code != 1 || !strings.Contains(errOut, "no store at") {
		t.Fatalf("missing store = %d %q", code, errOut)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("list created a store: %v", err)
	}
}
