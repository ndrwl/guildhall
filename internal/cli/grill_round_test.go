package cli

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

const testRound = `**Q1. Which format?**
CSV, or CSV and JSON.
Recommended: CSV only.

**Q2. Behind a flag?**
Recommended: yes.

**Q3. Import too?**
Recommended: no, export only.`

func runGrill(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(append([]string{"grill"}, args...))
	return code, stdout.String(), stderr.String()
}

func pageDrafts(t *testing.T, path, task string) map[int]store.Draft {
	t.Helper()
	got, err := withStore(path, true, func(s *store.Store) ([]store.DraftEntry, error) { return s.Drafts("app", task) })
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]store.Draft{}
	for _, e := range got {
		out[e.Question] = e.Draft
	}
	return out
}

// startGrill records a running grill attempt of a-grill, as task start does when it hands the Director its stage.
func startGrill(t *testing.T, path string) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.StartDirectorAttempt("test-grill-1", "app", "a-grill", "grill", false)
	}); err != nil {
		t.Fatal(err)
	}
}

// TestGrillRoundOpen: a round goes under the running Director stage attempt in the grill format; a rerun replays; a
// second round waits until the first is sent; and an attempt that is not a running Director stage, a round that does
// not read and a usage error are refused with nothing written.
func TestGrillRoundOpen(t *testing.T) {
	_, path, _ := pageFixture(t)
	startGrill(t, path)
	args := []string{"round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--questions-file", "-"}
	code, out, errOut := runGrill(t, testRound, args...)
	if code != 0 || !strings.Contains(out, `"record":"conversation/1"`) {
		t.Fatalf("grill round open = %d %s %s", code, out, errOut)
	}
	if again, rerun, _ := runGrill(t, testRound, args...); again != 0 || rerun != out {
		t.Fatalf("rerun = %d %s, want %s", again, rerun, out)
	}
	got := conversation(t, path, "app", "a-grill")
	if len(got) != 1 || got[0].Saying != (store.Saying{Author: store.ConversationDirector, Tag: "grill-1", Kind: store.ConversationRound, Text: testRound}) {
		t.Fatalf("conversation = %+v", got)
	}
	for name, c := range map[string]struct {
		stdin string
		args  []string
		code  int
		says  string
	}{
		"second round":     {"**Q1. More?**\nRecommended: no.", args, 1, "round 1 of grill-1 is not sent yet"},
		"not a round":      {"Round 2, three questions.", args, 1, "the questions are not a round"},
		"artisan attempt":  {testRound, []string{"round", "open", "--store", path, "--repo", "app", "--task", "a-approve", "--attempt", "plan-1", "--questions-file", "-"}, 1, "is an Artisan's"},
		"no such attempt":  {testRound, []string{"round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-2", "--questions-file", "-"}, 1, `has no attempt "grill-2"`},
		"closed task":      {testRound, []string{"round", "open", "--store", path, "--repo", "app", "--task", "a-merged2", "--attempt", "grill-1", "--questions-file", "-"}, 1, "is done"},
		"no questions":     {"", []string{"round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1"}, 2, "requires --questions-file"},
		"round with open":  {testRound, append(slices.Clone(args), "--round", "1"), 2, "--round goes with round show"},
		"not a subcommand": {"", []string{"round", "close"}, 2, "grill takes round open or round show"},
		"bad attempt":      {testRound, []string{"round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "Grill 1", "--questions-file", "-"}, 2, "must be ids"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, errOut := runGrill(t, c.stdin, c.args...); code != c.code || !strings.Contains(errOut, c.says) {
				t.Errorf("= %d %s; want %d saying %q", code, errOut, c.code, c.says)
			}
		})
	}
	if n := len(conversation(t, path, "app", "a-grill")); n != 1 {
		t.Errorf("refusals wrote: %d entries", n)
	}
	if code, _, errOut := runGrill(t, "", "round", "show", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1"); code != 1 || !strings.Contains(errOut, "no round of grill-1") {
		t.Errorf("show with nothing sent = %d %s", code, errOut)
	}
}

// TestGrillRoundOnThePage: every button of the round's form saves every box first; Accept recommendation and Answer
// with my words mark their question and Change opens it again; Send round with a question unanswered saves and then
// refuses, naming them; once all are answered it records the answers and a grill-answered message in one store
// open, wakes the Director, and a double Send round sends once. grill round show prints the answers, the page folds
// the sent round, and the next round can open.
func TestGrillRoundOnThePage(t *testing.T) {
	h, path, log := pageFixture(t)
	startGrill(t, path)
	if code, _, errOut := runGrill(t, testRound, "round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--questions-file", "-"); code != 0 {
		t.Fatalf("grill round open = %d %s", code, errOut)
	}
	body := gmPage(h, "a-grill")
	for _, want := range []string{`<div class="ask round" id="grill-1">`, `action="/app/a-grill/round"`, `<input type="hidden" name="round" value="1">`,
		`name="text-3"`, `<b>Grill round 1</b> · 0 of 3`, `<span class="kind">Grill round 1</span>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("the page lacks %s:\n%s", want, body)
		}
	}
	// press presses as a current tab, posting the drafts number the page shows now.
	press := func(act string, boxes map[int]string) (int, string, string) {
		t.Helper()
		form := url.Values{"tag": {"grill-1"}, "round": {"1"}, "act": {act}}
		if drafts := pageRoundDrafts(h); drafts != "" {
			form.Set("drafts", drafts)
		}
		for n, w := range boxes {
			form.Set("text-"+string(rune('0'+n)), w)
		}
		w := postRoute(t, h, "a-grill", "round", form)
		return w.Code, w.Header().Get("Location"), w.Body.String()
	}

	if code, loc, body := press("save", map[int]string{1: "CSV only, please.\r\n", 2: "", 3: ""}); code != http.StatusSeeOther || loc != "/app/a-grill#grill-1" {
		t.Fatalf("save: %d %q\n%s", code, loc, body)
	}
	d := pageDrafts(t, path, "a-grill")
	if len(d) != 1 || d[1] != (store.Draft{Tag: "grill-1", Round: 1, Question: 1, Text: "CSV only, please.\n"}) {
		t.Fatalf("drafts after save = %+v", d)
	}
	if body := gmPage(h, "a-grill"); !strings.Contains(body, "CSV only, please.\n</textarea><p class=\"saved\">saved ") || !strings.Contains(body, " · not an answer yet</p>") {
		t.Errorf("the saved words do not show as not an answer yet")
	}

	// Accept on Q2 saves Q1's and Q3's boxes too.
	if code, loc, _ := press("accept-2", map[int]string{1: "CSV only.", 2: "", 3: "maybe"}); code != http.StatusSeeOther || loc != "/app/a-grill#grill-1-q2" {
		t.Fatalf("accept: %d %q", code, loc)
	}
	d = pageDrafts(t, path, "a-grill")
	if d[1].Text != "CSV only." || d[1].Mark != "" || d[2].Mark != store.DraftAccept || d[3].Text != "maybe" {
		t.Fatalf("drafts after accept = %+v", d)
	}

	// Send round with Q1 and Q3 unanswered saves the boxes and refuses.
	code, _, refused := press("send", map[int]string{1: "CSV only.", 3: "maybe not"})
	if code != http.StatusConflict || !strings.Contains(refused, "<b>Refused · nothing sent:</b> Q1 and Q3 have no answer yet; your words are saved.") ||
		!strings.Contains(refused, `name="text-3" rows="2">maybe not</textarea>`) || !strings.Contains(refused, "<span>2 still to answer</span>") {
		t.Fatalf("send with two unanswered: %d\n%s", code, refused)
	}
	if d = pageDrafts(t, path, "a-grill"); d[3].Text != "maybe not" {
		t.Errorf("the refused send did not save Q3's box: %+v", d[3])
	}
	if msgs := pageMessages(t, path); len(msgs) != 0 {
		t.Fatalf("a refused send sent %+v", msgs)
	}

	// Answer with my words needs words; the other boxes are still saved.
	if code, _, body := press("words-1", map[int]string{1: " ", 3: "No import."}); code != http.StatusBadRequest || !strings.Contains(body, "Q1 has no words to answer with") {
		t.Fatalf("words with an empty box: %d\n%s", code, body)
	}
	if d = pageDrafts(t, path, "a-grill"); d[3].Text != "No import." || d[1].Text != "" {
		t.Errorf("drafts after words with none = %+v", d)
	}
	press("words-1", map[int]string{1: "CSV only.", 3: "No import."})
	press("words-3", map[int]string{3: "No import."})
	press("change-3", nil)
	if d = pageDrafts(t, path, "a-grill"); d[3].Mark != "" || d[3].Text != "No import." || d[1].Mark != store.DraftWords {
		t.Fatalf("drafts after change = %+v", d)
	}
	body = gmPage(h, "a-grill")
	if !strings.Contains(body, `<b>✓ Your words, saved</b> <span class="gist">CSV only.</span>`) || !strings.Contains(body, `<b>✓ Accepted</b> <span class="gist">yes.</span>`) ||
		strings.Contains(body, `name="text-1"`) || !strings.Contains(body, `<b>Grill round 1</b> · 2 of 3`) {
		t.Errorf("the answered questions do not fold:\n%s", body)
	}
	press("accept-3", map[int]string{3: "No import."})

	for i := range 2 {
		if code, loc, body := press("send", nil); code != http.StatusSeeOther || loc != "/app/a-grill#grill-1-talk" {
			t.Fatalf("send %d: %d %q\n%s", i+1, code, loc, body)
		}
	}
	msgs := pageMessages(t, path)
	if len(msgs) != 1 || msgs[0].ID != web.RoundID("app", "a-grill", 1) || msgs[0].Type != store.MessageGrillAnswered ||
		!strings.Contains(msgs[0].Text, "Round 1 of grill-1 of app/a-grill was sent on the task page: 3 questions, 2 accepted, 1 your words") ||
		!strings.Contains(msgs[0].Text, "guildhall grill round show --repo app --task a-grill --attempt grill-1") || strings.Contains(msgs[0].Text, "CSV only.") {
		t.Fatalf("messages = %+v", msgs)
	}
	if !slices.ContainsFunc(log.events, func(e string) bool { return strings.Contains(e, "grill-answered 1") }) {
		t.Errorf("events %q: grill-answered does not wake the Director", log.events)
	}
	got := conversation(t, path, "app", "a-grill")
	want := "**Q1. Which format?**\nYour words:\n\nCSV only.\n\n**Q2. Behind a flag?**\nAccepted: yes.\n\n**Q3. Import too?**\nAccepted: no, export only.\n\nNo import."
	if len(got) != 2 || got[1].Saying != (store.Saying{Author: store.ConversationGuildmasterPage, Tag: "grill-1", Kind: store.ConversationRoundAnswers, Text: want, Round: 1}) {
		t.Fatalf("conversation = %+v", got)
	}

	code, out, errOut := runGrill(t, "", "round", "show", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1")
	if code != 0 || !strings.HasPrefix(out, "# Round 1 of grill-1 · 3 questions · sent ") || !strings.Contains(out, "2 accepted, 1 your words\n\n"+want) {
		t.Fatalf("grill round show = %d %q %s", code, out, errOut)
	}

	body = gmPage(h, "a-grill")
	if strings.Contains(body, `action="/app/a-grill/round"`) || !strings.Contains(body, `<summary>Round 1 · 3 questions · sent `) || !strings.Contains(body, " · 2 accepted, 1 your words</summary>") {
		t.Errorf("the sent round does not fold:\n%s", body)
	}
	// A press on the sent round saves nothing; the next round opens.
	if code, _, _ := press("save", map[int]string{1: "late"}); code != http.StatusConflict {
		t.Errorf("save on a sent round: %d", code)
	}
	// The next round may ask the sent round's questions again; a rerun of it replays.
	for i := range 2 {
		if code, _, errOut := runGrill(t, testRound, "round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--questions-file", "-"); code != 0 {
			t.Fatalf("second round, run %d = %d %s", i+1, code, errOut)
		}
	}
	if got := conversation(t, path, "app", "a-grill"); len(got) != 3 {
		t.Fatalf("conversation after the second round = %+v", got)
	}
	if body := gmPage(h, "a-grill"); !strings.Contains(body, `<span class="kind">Grill round 2</span>`) || !strings.Contains(body, `<input type="hidden" name="round" value="3">`) {
		t.Errorf("the second round is not the form")
	}
	if code, _, _ := press("save", map[int]string{1: "late"}); code != http.StatusConflict {
		t.Errorf("save on an older round: %d", code)
	}
}
