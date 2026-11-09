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
		"not a subcommand": {"", []string{"round", "close"}, 2, "grill takes round open, round show, round answer, summary open or summary show"},
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

// acceptAllRound is a round of five questions, for Accept all recommendations.
const acceptAllRound = `**Q1. Which format?**
Recommended: CSV only.

**Q2. Behind a flag?**
Recommended: yes.

**Q3. Import too?**
Recommended: no, export only.

**Q4. Who can export?**
Recommended: every member.

**Q5. Where does the button sit?**
Recommended: in the toolbar.`

// TestGrillRoundAcceptAll: Accept all recommendations saves every box, then accepts only the unmarked questions whose
// box is empty: a question answered with words keeps them, an accepted one stays accepted, and an unmarked question
// with words, saved before or typed with the press, stays open with them. It never sends; Send round is then refused
// while those stay open and, once they are answered, sends Accepted for every accepted question.
func TestGrillRoundAcceptAll(t *testing.T) {
	h, path, _ := pageFixture(t)
	startGrill(t, path)
	if code, _, errOut := runGrill(t, acceptAllRound, "round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--questions-file", "-"); code != 0 {
		t.Fatalf("grill round open = %d %s", code, errOut)
	}
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
	if !strings.Contains(gmPage(h, "a-grill"), `value="accept-all"`) {
		t.Fatal("an open round with empty questions does not offer Accept all recommendations")
	}
	// Q1 empty; Q2 answered with words; Q3 accepted; Q4 unmarked with saved words; Q5 typed with the press.
	press("words-2", map[int]string{1: "", 2: "Yes, off by default.", 3: "", 4: "Admins only?", 5: ""})
	press("accept-3", map[int]string{1: "", 3: "", 4: "Admins only?", 5: ""})
	if code, loc, body := press("accept-all", map[int]string{1: "  ", 4: "Admins only?", 5: "A menu, not the toolbar."}); code != http.StatusSeeOther || loc != "/app/a-grill#grill-1" {
		t.Fatalf("accept-all: %d %q\n%s", code, loc, body)
	}
	d := pageDrafts(t, path, "a-grill")
	if d[1].Mark != store.DraftAccept || d[1].Text != "" {
		t.Errorf("Q1, empty, = %+v; want accepted", d[1])
	}
	if d[2].Mark != store.DraftWords || d[2].Text != "Yes, off by default." {
		t.Errorf("Q2, answered with words, = %+v", d[2])
	}
	if d[3].Mark != store.DraftAccept {
		t.Errorf("Q3, accepted, = %+v", d[3])
	}
	if d[4].Mark != store.DraftUnmarked || d[4].Text != "Admins only?" {
		t.Errorf("Q4, saved words, = %+v; want open with its words", d[4])
	}
	if d[5].Mark != store.DraftUnmarked || d[5].Text != "A menu, not the toolbar." {
		t.Errorf("Q5, typed words, = %+v; want open with its words, saved", d[5])
	}
	if msgs := pageMessages(t, path); len(msgs) != 0 {
		t.Fatalf("accept-all sent %+v", msgs)
	}
	// With every open question holding words, the button is no longer offered.
	if body := gmPage(h, "a-grill"); strings.Contains(body, `value="accept-all"`) || !strings.Contains(body, `<b>Grill round 1</b> · 3 of 5`) {
		t.Errorf("after accept-all the page offers it again or miscounts:\n%s", body)
	}
	// Pressed again with nothing to accept, it is a plain save.
	if code, _, body := press("accept-all", map[int]string{4: "Admins only?", 5: "A menu, not the toolbar."}); code != http.StatusSeeOther {
		t.Fatalf("accept-all with nothing to accept: %d\n%s", code, body)
	}

	if code, _, refused := press("send", map[int]string{4: "Admins only?", 5: "A menu, not the toolbar."}); code != http.StatusConflict ||
		!strings.Contains(refused, "Q4 and Q5 have no answer yet") {
		t.Fatalf("send with Q4 and Q5 open: %d\n%s", code, refused)
	}
	press("words-4", map[int]string{4: "Admins only.", 5: "A menu, not the toolbar."})
	press("words-5", map[int]string{5: "A menu, not the toolbar."})
	if code, _, body := press("send", nil); code != http.StatusSeeOther {
		t.Fatalf("send: %d\n%s", code, body)
	}
	got := conversation(t, path, "app", "a-grill")
	want := "**Q1. Which format?**\nAccepted: CSV only.\n\n**Q2. Behind a flag?**\nYour words:\n\nYes, off by default.\n\n**Q3. Import too?**\nAccepted: no, export only." +
		"\n\n**Q4. Who can export?**\nYour words:\n\nAdmins only.\n\n**Q5. Where does the button sit?**\nYour words:\n\nA menu, not the toolbar."
	if len(got) != 2 || got[1].Text != want {
		t.Fatalf("conversation = %+v", got)
	}
}

// TestGrillRoundAcceptAllAndSend: Accept all and send saves every box, accepts every unmarked question, keeping the
// words in its box, leaves a question answered with words or accepted as it is, and sends the round once with a
// grill-answered message that wakes the Director; pressed again it replays, and any other button is then refused as
// already sent.
func TestGrillRoundAcceptAllAndSend(t *testing.T) {
	h, path, log := pageFixture(t)
	startGrill(t, path)
	if code, _, errOut := runGrill(t, acceptAllRound, "round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--questions-file", "-"); code != 0 {
		t.Fatalf("grill round open = %d %s", code, errOut)
	}
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
	if !strings.Contains(gmPage(h, "a-grill"), `value="accept-send"`) {
		t.Fatal("an open round with questions to answer does not offer Accept all and send")
	}
	// Q1 empty; Q2 answered with words; Q3 accepted; Q4 unmarked with saved words; Q5 empty.
	press("words-2", map[int]string{1: "", 2: "Yes, off by default.", 3: "", 4: "Admins only?", 5: ""})
	press("accept-3", map[int]string{1: "", 3: "", 4: "Admins only?", 5: ""})
	if code, loc, body := press("accept-send", map[int]string{1: "", 4: "Admins only?", 5: ""}); code != http.StatusSeeOther || loc != "/app/a-grill#grill-1-talk" {
		t.Fatalf("accept-send: %d %q\n%s", code, loc, body)
	}
	d := pageDrafts(t, path, "a-grill")
	for _, n := range []int{1, 3, 4, 5} {
		if d[n].Mark != store.DraftAccept {
			t.Errorf("Q%d = %+v; want accepted", n, d[n])
		}
	}
	if d[2].Mark != store.DraftWords || d[2].Text != "Yes, off by default." || d[4].Text != "Admins only?" {
		t.Errorf("Q2 = %+v, Q4 = %+v; want their words kept", d[2], d[4])
	}
	got := conversation(t, path, "app", "a-grill")
	if len(got) != 2 || got[1].Kind != store.ConversationRoundAnswers || got[1].Author != store.ConversationGuildmasterPage ||
		!strings.Contains(got[1].Text, "**Q1. Which format?**\nAccepted: CSV only.") ||
		!strings.Contains(got[1].Text, "**Q2. Behind a flag?**\nYour words:\n\nYes, off by default.") ||
		!strings.Contains(got[1].Text, "**Q4. Who can export?**\nAccepted: every member.") ||
		!strings.Contains(got[1].Text, "Admins only?") ||
		!strings.Contains(got[1].Text, "**Q5. Where does the button sit?**\nAccepted: in the toolbar.") {
		t.Fatalf("conversation = %+v", got)
	}
	msgs := pageMessages(t, path)
	if len(msgs) != 1 || msgs[0].ID != web.RoundID("app", "a-grill", 1) || msgs[0].Type != store.MessageGrillAnswered {
		t.Fatalf("messages = %+v", msgs)
	}
	if !slices.ContainsFunc(log.events, func(e string) bool { return strings.Contains(e, "grill-answered 1") }) {
		t.Errorf("events %q: Accept all and send does not wake the Director", log.events)
	}
	if code, _, body := press("accept-send", nil); code != http.StatusSeeOther {
		t.Fatalf("a second accept-send: %d\n%s", code, body)
	}
	if again := conversation(t, path, "app", "a-grill"); len(again) != 2 || len(pageMessages(t, path)) != 1 {
		t.Fatalf("a second accept-send sent again: %+v", again)
	}
	if code, _, body := press("accept-all", nil); code != http.StatusConflict || !strings.Contains(body, "already sent") {
		t.Fatalf("accept-all on a sent round: %d\n%s", code, body)
	}
}

// chatAnswers are the Guildmaster's answers to testRound given in chat, as the Director writes them.
const chatAnswers = `**Q1. Which format?**
Accepted

**Q2. Behind a flag?**
Accepted: yes.
Off by default.

**Q3. Import too?**
Export only, and say so in the docs.`

// openTestRound opens testRound under a-grill's attempt.
func openTestRound(t *testing.T, path, attempt string) {
	t.Helper()
	if code, _, errOut := runGrill(t, testRound, "round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", attempt, "--questions-file", "-"); code != 0 {
		t.Fatalf("grill round open = %d %s", code, errOut)
	}
}

// TestGrillRoundAnswer: round answer records the Guildmaster's chat answers as the round's answers, authored
// guildmaster-chat in the page's text, and sends no message; round show prints them, the page folds the round as
// sent and refuses a Send round or save on it, and the next round opens. A rerun replays; a different answer, a
// wrong count and a round already sent on the page are refused.
func TestGrillRoundAnswer(t *testing.T) {
	h, path, log := pageFixture(t)
	startGrill(t, path)
	openTestRound(t, path, "grill-1")
	args := []string{"round", "answer", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--answers-file", "-"}
	if code, _, errOut := runGrill(t, "**Q1. Which format?**\nAccepted", args...); code != 1 || !strings.Contains(errOut, "answer 1 question; the round asks 3") {
		t.Errorf("a wrong count = %d %s", code, errOut)
	}
	code, out, errOut := runGrill(t, chatAnswers, args...)
	if code != 0 || !strings.Contains(out, `"record":"conversation/2"`) {
		t.Fatalf("grill round answer = %d %s %s", code, out, errOut)
	}
	if again, rerun, _ := runGrill(t, chatAnswers, append(slices.Clone(args), "--round", "1")...); again != 0 || rerun != out {
		t.Errorf("rerun = %d %s, want %s", again, rerun, out)
	}
	if code, _, errOut := runGrill(t, strings.Replace(chatAnswers, "Export only", "Import too", 1), args...); code != 1 || !strings.Contains(errOut, "round 1 of grill-1 is already sent from chat") {
		t.Errorf("a different second answer = %d %s", code, errOut)
	}
	want := "**Q1. Which format?**\nAccepted: CSV only.\n\n**Q2. Behind a flag?**\nAccepted: yes.\n\nOff by default.\n\n**Q3. Import too?**\nYour words:\n\nExport only, and say so in the docs."
	got := conversation(t, path, "app", "a-grill")
	if len(got) != 2 || got[1].Saying != (store.Saying{Author: store.ConversationGuildmasterChat, Tag: "grill-1", Kind: store.ConversationRoundAnswers, Text: want, Round: 1}) {
		t.Fatalf("conversation = %+v", got)
	}
	if msgs := pageMessages(t, path); len(msgs) != 0 {
		t.Errorf("round answer sent %+v", msgs)
	}
	if slices.ContainsFunc(log.events, func(e string) bool { return strings.Contains(e, "grill-answered") }) {
		t.Errorf("events %q: round answer woke the Director", log.events)
	}
	code, out, errOut = runGrill(t, "", "round", "show", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1")
	if code != 0 || !strings.Contains(out, "· 2 accepted, 1 your words\n\n"+want) {
		t.Fatalf("grill round show = %d %q %s", code, out, errOut)
	}
	body := gmPage(h, "a-grill")
	if strings.Contains(body, `action="/app/a-grill/round"`) || !strings.Contains(body, `<summary>Round 1 · 3 questions · sent `) {
		t.Errorf("the round answered in chat does not fold as sent:\n%s", body)
	}
	for _, act := range []string{"send", "save"} {
		w := postRoute(t, h, "a-grill", "round", url.Values{"tag": {"grill-1"}, "round": {"1"}, "act": {act}})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "you answered this round in chat") {
			t.Errorf("%s on the page after round answer: %d\n%s", act, w.Code, w.Body)
		}
	}
	if msgs := pageMessages(t, path); len(msgs) != 0 {
		t.Errorf("the page sent %+v", msgs)
	}
	openTestRound(t, path, "grill-1")

	// A round sent on the page takes no chat answers.
	if code, _, errOut := runGrill(t, chatAnswers, append(slices.Clone(args), "--round", "3")...); code != 1 || !strings.Contains(errOut, "has 2 rounds, not a round 3") {
		t.Errorf("no such round = %d %s", code, errOut)
	}
	for _, act := range []string{"accept-1", "accept-2", "accept-3", "send"} {
		form := url.Values{"tag": {"grill-1"}, "round": {"3"}, "act": {act}}
		if drafts := pageRoundDrafts(h); drafts != "" {
			form.Set("drafts", drafts)
		}
		if w := postRoute(t, h, "a-grill", "round", form); w.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d\n%s", act, w.Code, w.Body)
		}
	}
	if code, _, errOut := runGrill(t, chatAnswers, args...); code != 1 || !strings.Contains(errOut, "round 2 of grill-1 is already sent on the task page") {
		t.Errorf("answer after the page sent it = %d %s", code, errOut)
	}
	if n := len(conversation(t, path, "app", "a-grill")); n != 4 {
		t.Errorf("conversation has %d entries, want 4", n)
	}
}

// TestGrillRoundAnswerRefused: round answer refuses an attempt that has finished, a closed task, an attempt with no
// round of its own and usage errors, writing nothing.
func TestGrillRoundAnswerRefused(t *testing.T) {
	answer := func(t *testing.T, path, attempt string) (int, string) {
		t.Helper()
		code, _, errOut := runGrill(t, chatAnswers, "round", "answer", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", attempt, "--answers-file", "-")
		return code, errOut
	}
	// grilled is a page fixture whose a-grill has testRound open under grill-1, then change applied.
	grilled := func(t *testing.T, change func(s *store.Store) (store.Receipt, error)) string {
		t.Helper()
		_, path, _ := pageFixture(t)
		startGrill(t, path)
		openTestRound(t, path, "grill-1")
		if _, err := withStore(path, false, change); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Run("finished attempt", func(t *testing.T) {
		path := grilled(t, func(s *store.Store) (store.Receipt, error) {
			return s.FinishAttempt("finish-grill", "app", "a-grill", "grill", 1, store.AttemptSucceeded)
		})
		if code, errOut := answer(t, path, "grill-1"); code != 1 || !strings.Contains(errOut, "has finished (succeeded)") {
			t.Errorf("= %d %s", code, errOut)
		}
		if n := len(conversation(t, path, "app", "a-grill")); n != 1 {
			t.Errorf("the refusal wrote: %d entries", n)
		}
	})
	t.Run("closed task", func(t *testing.T) {
		path := grilled(t, func(s *store.Store) (store.Receipt, error) {
			return s.CancelTask("cancel", "app", "a-grill", "not needed")
		})
		if code, errOut := answer(t, path, "grill-1"); code != 1 || !strings.Contains(errOut, "is cancelled; its talk takes nothing new") {
			t.Errorf("= %d %s", code, errOut)
		}
	})
	t.Run("artisan attempt", func(t *testing.T) {
		path := grilled(t, func(s *store.Store) (store.Receipt, error) {
			if _, err := s.FinishAttempt("finish-grill", "app", "a-grill", "grill", 1, store.AttemptSucceeded); err != nil {
				return store.Receipt{}, err
			}
			return s.StartAttempt("artisan-grill", "app", "a-grill", "grill")
		})
		// grill-2 is an unattended grill: grill-1's round is not its, and it can have none of its own.
		if code, errOut := answer(t, path, "grill-2"); code != 1 || !strings.Contains(errOut, "grill-2 of app/a-grill has no round to answer") {
			t.Errorf("= %d %s", code, errOut)
		}
	})
	t.Run("usage", func(t *testing.T) {
		_, path, _ := pageFixture(t)
		for name, c := range map[string]struct {
			args []string
			says string
		}{
			"no answers file":     {[]string{"round", "answer", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1"}, "requires --answers-file"},
			"answers with show":   {[]string{"round", "show", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--answers-file", "-"}, "--answers-file goes with round answer"},
			"questions on answer": {[]string{"round", "answer", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--answers-file", "-", "--questions-file", "-"}, "--questions-file goes with round open"},
			"summary answer":      {[]string{"summary", "answer"}, "grill takes round open, round show, round answer"},
		} {
			if code, _, errOut := runGrill(t, "", c.args...); code != 2 || !strings.Contains(errOut, c.says) {
				t.Errorf("%s = %d %s", name, code, errOut)
			}
		}
	})
}

// TestGrillRoundReopened pins which grills take rounds: once the grill has finished, a design's changes_requested
// reopens it as a new Director attempt, grill-2, whose rounds open, are answered and are numbered from round 1 again;
// an unattended (Artisan) grill attempt takes no round.
func TestGrillRoundReopened(t *testing.T) {
	_, path, _ := pageFixture(t)
	startGrill(t, path)
	openTestRound(t, path, "grill-1")
	if code, _, errOut := runGrill(t, chatAnswers, "round", "answer", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--answers-file", "-"); code != 0 {
		t.Fatalf("answer grill-1's round = %d %s", code, errOut)
	}
	// The engine's reopening (interp's TestDesignChangesRequestedReopensTheGrill) leaves grill-1 succeeded and hands
	// grill attempt 2 to the Director.
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.FinishAttempt("finish-grill", "app", "a-grill", "grill", 1, store.AttemptSucceeded); err != nil {
			return store.Receipt{}, err
		}
		return s.StartDirectorAttempt("test-grill-2", "app", "a-grill", "grill", false)
	}); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runGrill(t, testRound, "round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--questions-file", "-"); code != 1 || !strings.Contains(errOut, "has finished") {
		t.Errorf("a round under the finished grill-1 = %d %s", code, errOut)
	}
	openTestRound(t, path, "grill-2")
	if code, _, errOut := runGrill(t, chatAnswers, "round", "answer", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-2", "--answers-file", "-"); code != 0 {
		t.Fatalf("answer grill-2's round = %d %s", code, errOut)
	}
	if code, out, errOut := runGrill(t, "", "round", "show", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-2", "--round", "1"); code != 0 || !strings.HasPrefix(out, "# Round 1 of grill-2 · 3 questions · sent ") {
		t.Errorf("grill-2's first round = %d %q %s", code, out, errOut)
	}

	// An unattended grill, an Artisan's attempt, takes no round.
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.FinishAttempt("finish-grill-2", "app", "a-grill", "grill", 2, store.AttemptSucceeded); err != nil {
			return store.Receipt{}, err
		}
		return s.StartAttempt("artisan-grill-3", "app", "a-grill", "grill")
	}); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runGrill(t, testRound, "round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-3", "--questions-file", "-"); code != 1 || !strings.Contains(errOut, "is an Artisan's") {
		t.Errorf("a round under the unattended grill-3 = %d %s", code, errOut)
	}
}
