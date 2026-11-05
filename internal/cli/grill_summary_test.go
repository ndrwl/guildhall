package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

const testSummary = "- Export CSV only.\n- Behind a flag.\n- No import."

// answerSummary records the Guildmaster's answer to summary entry n of grill-1 of a-grill, as the page's buttons do.
func answerSummary(t *testing.T, path, mutation string, n int, outcome, words string) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.AddConversation(mutation, "app", "a-grill", store.Saying{Author: store.ConversationGuildmasterPage, Tag: "grill-1",
			Kind: store.ConversationSummaryAnswer, Text: web.SummaryAnswerText(outcome, words), Round: n, Option: outcome})
	}); err != nil {
		t.Fatal(err)
	}
}

// TestGrillSummaryOpen: a summary goes under the running Director stage attempt; a rerun replays; the attempt has
// one open ask at a time, so neither a second summary nor a round opens while a summary is unanswered, and a summary
// does not open while a round is unsent; a revised summary after Request changes is a new entry even when it reads
// the same; and an attempt that is not a running Director stage and a usage error are refused with nothing written.
func TestGrillSummaryOpen(t *testing.T) {
	_, path, _ := pageFixture(t)
	startGrill(t, path)
	open := []string{"summary", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--summary-file", "-"}
	show := []string{"summary", "show", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1"}
	round := []string{"round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--questions-file", "-"}
	code, out, errOut := runGrill(t, testSummary, open...)
	if code != 0 || !strings.Contains(out, `"record":"conversation/1"`) {
		t.Fatalf("grill summary open = %d %s %s", code, out, errOut)
	}
	if again, rerun, _ := runGrill(t, testSummary, open...); again != 0 || rerun != out {
		t.Fatalf("rerun = %d %s, want %s", again, rerun, out)
	}
	got := conversation(t, path, "app", "a-grill")
	if len(got) != 1 || got[0].Saying != (store.Saying{Author: store.ConversationDirector, Tag: "grill-1", Kind: store.ConversationSummary, Text: testSummary}) {
		t.Fatalf("conversation = %+v", got)
	}
	for name, c := range map[string]struct {
		stdin string
		args  []string
		code  int
		says  string
	}{
		"second summary":    {"- Export JSON too.", open, 1, "grill summary 1 of grill-1 is not answered yet; the summary opens once"},
		"round":             {testRound, round, 1, "grill summary 1 of grill-1 is not answered yet; a new round opens once"},
		"blank summary":     {"  \n", open, 1, ""},
		"artisan attempt":   {testSummary, []string{"summary", "open", "--store", path, "--repo", "app", "--task", "a-approve", "--attempt", "plan-1", "--summary-file", "-"}, 1, "is an Artisan's"},
		"no such attempt":   {testSummary, []string{"summary", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-2", "--summary-file", "-"}, 1, `has no attempt "grill-2"`},
		"closed task":       {testSummary, []string{"summary", "open", "--store", path, "--repo", "app", "--task", "a-merged2", "--attempt", "grill-1", "--summary-file", "-"}, 1, "is done"},
		"no summary file":   {"", open[:len(open)-2], 2, "requires --summary-file"},
		"summary with open": {testSummary, append(slices.Clone(open), "--summary", "1"), 2, "--summary goes with summary show"},
		"file with show":    {"", append(slices.Clone(show), "--summary-file", "-"), 2, "--summary-file goes with summary open"},
		"questions file":    {"", append(slices.Clone(open), "--questions-file", "-"), 2, "flag provided but not defined"},
		"bad attempt":       {testSummary, []string{"summary", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "Grill 1", "--summary-file", "-"}, 2, "must be ids"},
		"not a subcommand":  {"", []string{"summary", "close"}, 2, "grill takes round open, round show, summary open or summary show"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, errOut := runGrill(t, c.stdin, c.args...); code != c.code || !strings.Contains(errOut, c.says) {
				t.Errorf("= %d %s; want %d saying %q", code, errOut, c.code, c.says)
			}
		})
	}
	if n := len(conversation(t, path, "app", "a-grill")); n != 1 {
		t.Fatalf("refusals wrote: %d entries", n)
	}
	if code, _, errOut := runGrill(t, "", show...); code != 1 || !strings.Contains(errOut, "no grill summary of grill-1 of app/a-grill is answered yet") {
		t.Errorf("show with nothing answered = %d %s", code, errOut)
	}
	if code, out, errOut := runGrill(t, "", append(slices.Clone(show), "--summary", "1")...); code != 0 || out != "# Grill summary 1 of grill-1 · not answered yet\n\n"+testSummary+"\n" {
		t.Errorf("show --summary 1 unanswered = %d %q %s", code, out, errOut)
	}

	// Request changes: the Director may then open a revised summary, here reading the same, which is a new entry.
	answerSummary(t, path, "answer-1", 1, store.SummaryChangesRequested, "Export JSON too.")
	if code, out, errOut := runGrill(t, "", show...); code != 0 || !strings.HasPrefix(out, "# Grill summary 1 of grill-1 · Changes requested · ") ||
		!strings.HasSuffix(out, "\n\n"+testSummary+"\n\n## The Guildmaster's words\n\nExport JSON too.\n") {
		t.Errorf("show after Request changes = %d %q %s", code, out, errOut)
	}
	if code, out, errOut := runGrill(t, testSummary, open...); code != 0 || !strings.Contains(out, `"record":"conversation/3"`) {
		t.Fatalf("revised summary = %d %s %s", code, out, errOut)
	}
	if code, _, errOut := runGrill(t, testRound, round...); code != 1 || !strings.Contains(errOut, "grill summary 2 of grill-1 is not answered yet") {
		t.Errorf("round while summary 2 is open = %d %s", code, errOut)
	}

	// Confirm without words: a round then opens, and while it is unsent no summary does.
	answerSummary(t, path, "answer-2", 3, store.SummaryConfirmed, "")
	if code, out, errOut := runGrill(t, testRound, round...); code != 0 || !strings.Contains(out, `"record":"conversation/5"`) {
		t.Fatalf("round after Confirm = %d %s %s", code, out, errOut)
	}
	if code, _, errOut := runGrill(t, "- Something else.", open...); code != 1 || !strings.Contains(errOut, "round 1 of grill-1 is not sent yet; the summary opens once") {
		t.Errorf("summary while a round is unsent = %d %s", code, errOut)
	}
	if code, out, errOut := runGrill(t, "", show...); code != 0 || !strings.HasPrefix(out, "# Grill summary 2 of grill-1 · Confirmed · ") ||
		!strings.HasSuffix(out, "## The Guildmaster's words\n\nNone.\n") {
		t.Errorf("show after Confirm = %d %q %s", code, out, errOut)
	}
	if code, out, _ := runGrill(t, "", append(slices.Clone(show), "--summary", "1")...); code != 0 || !strings.Contains(out, "· Changes requested ·") {
		t.Errorf("show --summary 1 = %d %q", code, out)
	}
	if code, _, errOut := runGrill(t, "", append(slices.Clone(show), "--summary", "3")...); code != 1 || !strings.Contains(errOut, "has 2 summaries, not a summary 3") {
		t.Errorf("show --summary 3 = %d %s", code, errOut)
	}
}

// TestSummaryAnswerText: the answer's text leads with its outcome and keeps the words after a blank line, and the
// words read back.
func TestSummaryAnswerText(t *testing.T) {
	for _, c := range []struct{ outcome, words, text string }{
		{store.SummaryConfirmed, "", "Confirmed"},
		{store.SummaryConfirmed, "  also note the flag name  ", "Confirmed\n\nalso note the flag name"},
		{store.SummaryChangesRequested, "JSON too.\n\nAnd import.", "Changes requested\n\nJSON too.\n\nAnd import."},
	} {
		if got := web.SummaryAnswerText(c.outcome, c.words); got != c.text {
			t.Errorf("SummaryAnswerText(%q, %q) = %q, want %q", c.outcome, c.words, got, c.text)
		}
		if got := web.SummaryAnswerWords(c.outcome, c.text); got != strings.TrimSpace(c.words) {
			t.Errorf("SummaryAnswerWords(%q, %q) = %q", c.outcome, c.text, got)
		}
	}
	if got := web.SummaryAnswerWords(store.SummaryConfirmed, "ok, go"); got != "ok, go" {
		t.Errorf("words without an outcome line = %q", got)
	}
}
