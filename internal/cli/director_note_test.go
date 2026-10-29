package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
)

// TestDirectorNoteCommands: add, edit and remove each print a one-line receipt, and list prints the live notes
// grouped by kind, flagging a note whose task is done, or "No notes." when there are none.
func TestDirectorNoteCommands(t *testing.T) {
	d := newRunDirector(t)
	newTask(t, d.store, d.config, "app", "Another", "t-2")
	flags := []string{"--store", d.store}
	run := func(args ...string) (int, string, string) {
		return runCLI(t, append(append([]string{"director", "note"}, args...), flags...)...)
	}
	if code, _, stderr := run("add", "--kind", "plan", "x"); code != 1 || !strings.Contains(stderr, "no Director session") {
		t.Fatalf("add with no session: exit %d, stderr %s", code, stderr)
	}
	if code, out, stderr := run("list"); code != 0 || out != "No notes.\n" {
		t.Fatalf("list of none: exit %d, %q, stderr %s", code, out, stderr)
	}
	recordSession(t, d.store, false, store.DirectorRunning)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"add", "--kind", "plan", "rebase d20 after d18 merges"}, "n1 added\n"},
		{[]string{"add", "--kind", "promised", "--task", "app/t-1", "report when t-1 merges"}, "n2 added\n"},
		{[]string{"add", "--kind", "guildmaster", "prefers short replies"}, "n3 added\n"},
		{[]string{"add", "--task", "app/t-2", "--kind", "plan", "drop t-2 if t-1 covers it"}, "n4 added\n"},
		{[]string{"edit", "n1", "rebase d20 and d22 after d18 merges"}, "n1 edited\n"},
		{[]string{"add", "--kind", "plan", "gone soon"}, "n5 added\n"},
		{[]string{"remove", "n5", "--reason", "done"}, "n5 removed\n"},
	} {
		if code, out, stderr := run(c.args...); code != 0 || out != c.want {
			t.Fatalf("%q: exit %d, %q, stderr %s; want %q", c.args, code, out, stderr, c.want)
		}
	}
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.SetTaskState("finish-t-1", "app", "t-1", store.TaskDone)
	}); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := run("list")
	want := "### promised\n- n2 app/t-1 (s1, 0m): report when t-1 merges [task done, check it]\n" +
		"### guildmaster\n- n3 (s1, 0m): prefers short replies\n" +
		"### plan\n- n1 (s1, 0m): rebase d20 and d22 after d18 merges\n- n4 app/t-2 (s1, 0m): drop t-2 if t-1 covers it\n"
	if code != 0 || out != want {
		t.Fatalf("list: exit %d, stderr %s\n%s\nwant\n%s", code, stderr, out, want)
	}
	for name, args := range map[string][]string{
		"bad kind":         {"add", "--kind", "doing", "x"},
		"multi-line":       {"add", "--kind", "plan", "a\nb"},
		"unknown task":     {"add", "--kind", "plan", "--task", "app/t-9", "x"},
		"unknown note":     {"edit", "n9", "x"},
		"removed note":     {"edit", "n5", "x"},
		"removed again":    {"remove", "n5", "--reason", "other"},
		"over-long reason": {"remove", "n1", "--reason", strings.Repeat("r", store.MaxNoteReasonBytes+1)},
	} {
		if code, out, _ := run(args...); code != 1 || out != "" {
			t.Errorf("%s: exit %d, %q; want 1 and no output", name, code, out)
		}
	}
	for name, args := range map[string][]string{
		"no subcommand": nil,
		"unknown":       {"show"},
		"no kind":       {"add", "x"},
		"no text":       {"add", "--kind", "plan"},
		"two texts":     {"add", "--kind", "plan", "a", "b"},
		"edit no text":  {"edit", "n1"},
		"remove no id":  {"remove"},
		"list argument": {"list", "extra"},
	} {
		if code, _, stderr := run(args...); code != 2 || !strings.Contains(stderr, "director note") {
			t.Errorf("%s: exit %d, stderr %s; want a usage error", name, code, stderr)
		}
	}
	if code, _, stderr := runCLI(t, "director", "note", "list"); code != 2 || !strings.Contains(stderr, "director note list: no Guildhall copy found") {
		t.Errorf("list without a store: exit %d, stderr %s", code, stderr)
	}
}

func TestNoteAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second: "0m", 59 * time.Minute: "59m", time.Hour: "1h", 23*time.Hour + 59*time.Minute: "23h",
		24 * time.Hour: "1d", 50 * time.Hour: "2d",
	} {
		if got := noteAge(d); got != want {
			t.Errorf("noteAge(%v) = %q, want %q", d, got, want)
		}
	}
}
