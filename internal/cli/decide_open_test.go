package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// storeWrites is the store's receipt count and the decisions of repo/task, to show a refused or replayed open wrote nothing.
func storeWrites(t *testing.T, path, repo, task string) (int, []store.Decision) {
	t.Helper()
	s, err := store.Open(path, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	receipts, err := s.Receipts()
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := s.Decisions(repo, task)
	if err != nil {
		t.Fatal(err)
	}
	return len(receipts), decisions
}

func assertNoWrite(t *testing.T, path, repo, task string, receipts int, decisions []store.Decision) {
	t.Helper()
	n, after := storeWrites(t, path, repo, task)
	was, _ := json.Marshal(decisions)
	now, _ := json.Marshal(after)
	if n != receipts || string(now) != string(was) {
		t.Fatalf("store changed: %d receipts, decisions %+v; was %d, %+v", n, after, receipts, decisions)
	}
}

func TestDecideOpenNumbersQuestionsAndListsThem(t *testing.T) {
	path := decideStorePath(t)
	code, out, errOut := runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "t-1", "--question", "Which grill first?", "--option", "a", "--option", "b")
	q1 := decodeJSON[store.Decision](t, out)
	if code != 0 || q1.ID != "q1" || q1.Kind != store.DecisionQuestion || q1.Question != "Which grill first?" || !slices.Equal(q1.Options, []string{"a", "b"}) || q1.Subject != nil || !q1.Open() {
		t.Fatalf("open = %d %+v %s", code, q1, errOut)
	}
	s, err := store.Open(path, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Receipt("decision-open:app:t-1:q1")
	_ = s.Close()
	if err != nil {
		t.Fatalf("open's receipt: %v", err)
	}

	code, out, errOut = runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "t-1", "--question", "Ship on Friday?")
	if q2 := decodeJSON[store.Decision](t, out); code != 0 || q2.ID != "q2" || len(q2.Options) != 0 {
		t.Fatalf("second open = %d %+v %s", code, q2, errOut)
	}
	code, out, _ = runDecide(t, "", "list", "--store", path, "--repo", "app")
	var ids []string
	for _, d := range decodeJSON[[]store.Decision](t, out) {
		ids = append(ids, d.TaskID+"/"+d.ID)
	}
	if code != 0 || !slices.Contains(ids, "t-1/q1") || !slices.Contains(ids, "t-1/q2") {
		t.Fatalf("list = %d %v", code, ids)
	}
	code, out, _ = runDecide(t, "", "show", "--store", path, "--repo", "app", "--task", "t-1", "--decision", "q1")
	if got := decodeJSON[store.Decision](t, out); code != 0 || fmt.Sprint(got) != fmt.Sprint(q1) {
		t.Fatalf("show = %d %+v, want %+v", code, got, q1)
	}

	// An answered question no longer counts as a retry, and the numbering continues past it.
	if code, _, errOut := runDecide(t, "a\nthen b", "answer", "--store", path, "--repo", "app", "--task", "t-1", "--decision", "q1", "--answer-file", "-"); code != 0 {
		t.Fatalf("answer q1 = %d %s", code, errOut)
	}
	if d := decisionOf(t, path, "app", "t-1", "q1"); d.Choice != "a" || d.Note != "then b" || d.Answer != "a\nthen b" {
		t.Fatalf("q1 answered with an option = %+v", d)
	}
	code, out, errOut = runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "t-1", "--question", "Which grill first?", "--option", "a", "--option", "b")
	if q3 := decodeJSON[store.Decision](t, out); code != 0 || q3.ID != "q3" {
		t.Fatalf("reopen after answer = %d %+v %s", code, q3, errOut)
	}
}

func TestDecideOpenRetryWritesNothing(t *testing.T) {
	path := decideStorePath(t)
	args := []string{"open", "--store", path, "--repo", "app", "--task", "t-1", "--question", "Which grill first?", "--option", "a", "--option", "b"}
	code, first, errOut := runDecide(t, "", args...)
	if code != 0 {
		t.Fatalf("open = %d %s", code, errOut)
	}
	receipts, decisions := storeWrites(t, path, "app", "t-1")
	code, again, errOut := runDecide(t, "", args...)
	if code != 0 || again != first {
		t.Fatalf("retry = %d %q %s; want %q", code, again, errOut, first)
	}
	assertNoWrite(t, path, "app", "t-1", receipts, decisions)

	// Other options make another question.
	code, out, _ := runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "t-1", "--question", "Which grill first?", "--option", "b", "--option", "a")
	if d := decodeJSON[store.Decision](t, out); code != 0 || d.ID != "q2" {
		t.Fatalf("reordered options = %d %+v", code, d)
	}
}

func TestDecideOpenQuestionFileFromStdin(t *testing.T) {
	path := decideStorePath(t)
	code, out, errOut := runDecide(t, "\xef\xbb\xbfWhich grill first?\r\nThe ones on d105.\r\n", "open", "--store", path, "--repo", "lib", "--task", "t-1", "--question-file", "-")
	if d := decodeJSON[store.Decision](t, out); code != 0 || d.ID != "q1" || d.Question != "Which grill first?\nThe ones on d105.\n" {
		t.Fatalf("open from stdin = %d %+v %s", code, d, errOut)
	}
	file := writeAnswer(t, "From a file?")
	code, out, errOut = runDecide(t, "", "open", "--store", path, "--repo", "lib", "--task", "t-1", "--question-file", file)
	if d := decodeJSON[store.Decision](t, out); code != 0 || d.ID != "q2" || d.Question != "From a file?" {
		t.Fatalf("open from file = %d %+v %s", code, d, errOut)
	}
}

func TestDecideOpenTakesFreeTextAnswers(t *testing.T) {
	path := decideStorePath(t)
	if code, _, errOut := runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "t-1", "--question", "Which grill first?", "--option", "a", "--option", "b"); code != 0 {
		t.Fatalf("open = %d %s", code, errOut)
	}
	code, _, errOut := runDecide(t, "neither, do c", "answer", "--store", path, "--repo", "app", "--task", "t-1", "--decision", "q1", "--answer-file", "-")
	if code != 0 {
		t.Fatalf("free answer = %d %s", code, errOut)
	}
	if d := decisionOf(t, path, "app", "t-1", "q1"); !d.Answered || d.Answer != "neither, do c" || d.Choice != "" || d.Note != "" {
		t.Fatalf("q1 = %+v", d)
	}
	code, out, _ := runDecide(t, "", "list", "--store", path, "--repo", "app")
	for _, d := range decodeJSON[[]store.Decision](t, out) {
		if code != 0 || d.ID == "q1" {
			t.Fatalf("answered question still listed: %d %+v", code, d)
		}
	}
}

func TestDecideOpenRefusals(t *testing.T) {
	path := decideStorePath(t)
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTaskState("done", "app", "t-2", store.TaskDone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelTask("cancel", "lib", "t-1", "not needed"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	many := []string{}
	for i := range store.MaxDecisionOptions + 1 {
		many = append(many, "--option", fmt.Sprintf("o%d", i))
	}
	open := func(repo, task string, rest ...string) []string {
		return append([]string{"open", "--store", path, "--repo", repo, "--task", task}, rest...)
	}
	for name, c := range map[string]struct {
		repo, task string
		args       []string
		stdin      string
		code       int
		want       string
	}{
		"done task":         {"app", "t-2", open("app", "t-2", "--question", "Why?"), "", 1, "app/t-2 is done"},
		"cancelled task":    {"lib", "t-1", open("lib", "t-1", "--question", "Why?"), "", 1, "lib/t-1 is cancelled"},
		"unknown task":      {"app", "t-9", open("app", "t-9", "--question", "Why?"), "", 1, "not found"},
		"blank question":    {"app", "t-1", open("app", "t-1", "--question", "  "), "", 2, "usage:"},
		"blank file":        {"app", "t-1", open("app", "t-1", "--question-file", "-"), " \n", 1, "question must not be empty"},
		"too many options":  {"app", "t-1", open("app", "t-1", append([]string{"--question", "Which?"}, many...)...), "", 1, "options"},
		"duplicate options": {"app", "t-1", open("app", "t-1", "--question", "Which?", "--option", "a", "--option", "a"), "", 1, "a"},
		"multi-line option": {"app", "t-1", open("app", "t-1", "--question", "Which?", "--option", "a\nb"), "", 1, "option"},
		"no question":       {"app", "t-1", open("app", "t-1"), "", 2, "usage:"},
		"both questions":    {"app", "t-1", open("app", "t-1", "--question", "A?", "--question-file", "-"), "A?", 2, "usage:"},
		"no task":           {"app", "", []string{"open", "--store", path, "--repo", "app", "--question", "A?"}, "", 2, "usage:"},
		"bad task id":       {"app", "t-1", open("app", "T1", "--question", "A?"), "", 2, "usage:"},
		"decision flag":     {"app", "t-1", open("app", "t-1", "--question", "A?", "--decision", "q1"), "", 2, "usage:"},
		"positional":        {"app", "t-1", open("app", "t-1", "--question", "A?", "extra"), "", 2, "usage:"},
	} {
		t.Run(name, func(t *testing.T) {
			task := c.task
			if task == "" || task == "t-9" {
				task = "t-1"
			}
			receipts, decisions := storeWrites(t, path, c.repo, task)
			code, out, errOut := runDecide(t, c.stdin, c.args...)
			if code != c.code || out != "" || !strings.Contains(errOut, c.want) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want %d and %q", code, out, errOut, c.code, c.want)
			}
			assertNoWrite(t, path, c.repo, task, receipts, decisions)
		})
	}
}

func TestQuestionNumber(t *testing.T) {
	for id, want := range map[string]int{"q1": 1, "q12": 12, "q": 0, "q0": 0, "q01": 0, "q-1": 0, "qa": 0, "port": 0, "q1x": 0, "q1234567890": 0} {
		if n, ok := questionNumber(id); n != want || ok != (want > 0) {
			t.Errorf("questionNumber(%q) = %d %v, want %d", id, n, ok, want)
		}
	}
}

// TestQuestionSurfacesButHoldsNothing: an open question is a decision needs_me item in director status, with the
// question as its reason, and leaves the needs_me list once answered; a merge-queue front task whose only open
// decision is a question waits on CI, not on a decision.
func TestQuestionSurfacesButHoldsNothing(t *testing.T) {
	cfg := writeDirector(t, directorTOML)
	path := emptyStore(t)
	newTask(t, path, cfg, "app", "Queued", "t-1")
	if code, _, errOut := runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "t-1", "--question", "Which grill first?", "--option", "a", "--option", "b"); code != 0 {
		t.Fatalf("open = %d %s", code, errOut)
	}
	status := func() []needsMeItem {
		t.Helper()
		code, out, errOut := directorCmd(t, nil, "status", "--store", path, "--state", t.TempDir(), "--json")
		if code != 0 {
			t.Fatalf("status = %d %s", code, errOut)
		}
		var items []needsMeItem
		for _, n := range decodeJSON[struct {
			NeedsMe []needsMeItem `json:"needs_me"`
		}](t, out).NeedsMe {
			if n.Kind == decisionKind {
				items = append(items, n)
			}
		}
		return items
	}
	if got := status(); len(got) != 1 || got[0].TaskID != "t-1" || got[0].Decision != "q1" || got[0].Reason != "Which grill first?" {
		t.Fatalf("needs_me decisions = %+v", got)
	}

	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument("doc", store.DocumentRef{Repository: "app", TaskID: "t-1", Name: "deliver-1-authorized", Path: "tasks/app/t-1/deliver-1/authorized.json", SHA256: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JoinMergeQueue("join", "app", "t-1", "deliver-1", "document/deliver-1-authorized", "https://forge.invalid/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimMergeFront("front", "app", "t-1"); err != nil {
		t.Fatal(err)
	}
	wait, err := frontWait(s, "app", "t-1")
	if err == nil && wait == "ci" {
		_, err = s.OpenDecision("open-ask", store.Decision{Repository: "app", TaskID: "t-1", ID: "ask-1", Kind: store.DecisionApproval,
			Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("d", 64)}, Options: []string{"approve"}, Question: "Approve?"})
	}
	var other string
	if err == nil {
		other, err = frontWait(s, "app", "t-1")
	}
	_ = s.Close()
	if err != nil || wait != "ci" || other != "decision" {
		t.Fatalf("front waits on %q with only a question and %q with an approval too, %v; want ci then decision", wait, other, err)
	}

	if code, _, errOut := runDecide(t, "neither, do c", "answer", "--store", path, "--repo", "app", "--task", "t-1", "--decision", "q1", "--answer-file", "-"); code != 0 {
		t.Fatalf("answer = %d %s", code, errOut)
	}
	for _, n := range status() {
		if n.Decision == "q1" {
			t.Fatalf("answered question still needs me: %+v", n)
		}
	}
}
