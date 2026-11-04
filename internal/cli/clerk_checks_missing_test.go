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

// TestClerkNoCIRun: a task waiting on checks whose newest checks message is checks-missing shows "no CI run for
// <head12>" instead of CI pending, its pull request's checks read "no CI run", and a merge-queue front in that state
// waits on "no CI run"; a later checks-pending message, as a new head's read sends, shows CI pending again.
func TestClerkNoCIRun(t *testing.T) {
	dir := clerkFixture(t)
	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	send := func(id, repo, task, typ string) {
		t.Helper()
		if _, err := s.SendMessage(store.Message{ID: id, Recipient: store.RecipientDirector, Repository: repo, TaskID: task, Type: typ, Text: "text"}); err != nil {
			t.Fatal(err)
		}
	}
	// l-front gets a pull request, as a rebased front has, so its checks messages are read.
	data, _ := json.Marshal(map[string]any{"schema": "guildhall.pull-request/1", "remote": "origin", "number": 41,
		"url": "https://forge.invalid/lib/pull/41", "branch": "guildhall/l-front", "head": strings.Repeat("2", 40), "state": "open"})
	rel := "tasks/lib/l-front/publish-1/pull-request.json"
	path := filepath.Join(dir, "state", "documents", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
	if _, err := s.PutDocument("m-lfront-pr", store.DocumentRef{Repository: "lib", TaskID: "l-front", Name: "publish-1-pr", Path: rel, SHA256: digest.Of(data).Hex()}); err != nil {
		t.Fatal(err)
	}
	send("app.a-checks.checks-pending.validate.111111111111", "app", "a-checks", store.MessageChecksPending)
	send("app.a-checks.checks-missing.validate.111111111111", "app", "a-checks", store.MessageChecksMissing)
	send("lib.l-front.checks-pending.deliver.222222222222", "lib", "l-front", store.MessageChecksPending)
	send("lib.l-front.checks-missing.deliver.222222222222", "lib", "l-front", store.MessageChecksMissing)
	send("lib.l-front.checks-missing.deliver.222222222222.after-start", "lib", "l-front", store.MessageChecksMissing)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := runClerk(t, dir, "guild", "--all")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	tasks, _, _ := strings.Cut(out, "\nNeeds you\n")
	if line := clerkLine(tasks, "app/a-checks"); !strings.HasSuffix(line, "  no CI run for 111111111111, PR #212") {
		t.Errorf("a-checks: line %q, want no CI run for its head", line)
	}
	if line := clerkLine(tasks, "lib/l-front"); !strings.HasSuffix(line, "  in merge queue 1/1, no CI run") {
		t.Errorf("l-front: line %q, want the front waiting on no CI run", line)
	}
	if prs := clerkSection(out, "Pull requests"); !strings.Contains(prs, "#212") || !strings.Contains(prs, "no CI run") {
		t.Errorf("pull requests:\n%s\nwant #212 with no CI run", prs)
	}
	if q := clerkSection(out, "Merge queue"); !strings.Contains(q, "front l-front no CI run") {
		t.Errorf("merge queue:\n%s\nwant the front waiting on no CI run", q)
	}
	code, out, _ = runClerk(t, dir, "queue")
	if code != 0 || !strings.Contains(out, "no CI run") {
		t.Errorf("clerk queue: exit %d, want the front waiting on no CI run:\n%s", code, out)
	}

	// A new head's read sends its checks-pending first: the task is CI pending again.
	s, err = store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	send("app.a-checks.checks-pending.validate.333333333333", "app", "a-checks", store.MessageChecksPending)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, out, _ = runClerk(t, dir, "guild", "--all")
	tasks, _, _ = strings.Cut(out, "\nNeeds you\n")
	if line := clerkLine(tasks, "app/a-checks"); !strings.HasSuffix(line, "  CI pending, PR #212") {
		t.Errorf("a-checks after a new head: line %q, want CI pending", line)
	}
}

func TestMissingHead(t *testing.T) {
	for id, want := range map[string]string{
		"app.t7.checks-missing.validate.0123456789ab":            "0123456789ab",
		"app.t7.checks-missing.deliver.0123456789ab.after-start": "0123456789ab",
		"app.t7.checks-missing.validate":                         "",
		"app.t7.checks-missing.validate.0123":                    "",
		"advance.0123456789abcdef":                               "",
		"app.t7.checks-pending.validate.0123456789ab":            "",
	} {
		if got := missingHead(id); got != want {
			t.Errorf("missingHead(%q) = %q, want %q", id, got, want)
		}
	}
}
