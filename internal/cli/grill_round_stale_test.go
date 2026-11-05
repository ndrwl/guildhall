package cli

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// openGrillRound opens testRound on a-grill's running grill-1 and returns the page's handler and store path.
func openGrillRound(t *testing.T) (http.Handler, string) {
	t.Helper()
	h, path, _ := pageFixture(t)
	startGrill(t, path)
	if code, _, errOut := runGrill(t, testRound, "round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--questions-file", "-"); code != 0 {
		t.Fatalf("grill round open = %d %s", code, errOut)
	}
	return h, path
}

// pageRoundDrafts is the drafts number the open round's form on a-grill's page carries, or "" with no form.
func pageRoundDrafts(h http.Handler) string {
	_, rest, ok := strings.Cut(gmPage(h, "a-grill"), `<input type="hidden" name="drafts" value="`)
	if !ok {
		return ""
	}
	return rest[:strings.Index(rest, `"`)]
}

// pressRound posts one button of a-grill's round 1 from a tab that loaded drafts, or with no drafts field when
// drafts is "".
func pressRound(t *testing.T, h http.Handler, drafts, act string, boxes map[int]string) (int, string) {
	t.Helper()
	form := url.Values{"tag": {"grill-1"}, "round": {"1"}, "act": {act}}
	if drafts != "" {
		form.Set("drafts", drafts)
	}
	for n, w := range boxes {
		form.Set("text-"+strconv.Itoa(n), w)
	}
	w := postRoute(t, h, "a-grill", "round", form)
	return w.Code, w.Body.String()
}

const staleReason = "changed since you loaded the page: its answers were saved from another tab or window; reload, your words there are kept"

// TestGrillRoundStaleSaveRefused: tab B, loaded before tab A answered Q1, presses Save with empty boxes; it is refused
// with 409 and Q1 and Q2 keep tab A's words (H81 of d209).
func TestGrillRoundStaleSaveRefused(t *testing.T) {
	h, path := openGrillRound(t)
	b := "0"
	if got := pageRoundDrafts(h); got != b {
		t.Errorf("a round with no drafts carries drafts %q, want 0", got)
	}
	if code, body := pressRound(t, h, pageRoundDrafts(h), "words-1", map[int]string{1: "CSV only.", 2: "flag it", 3: ""}); code != http.StatusSeeOther {
		t.Fatalf("tab A words-1: %d\n%s", code, body)
	}
	code, body := pressRound(t, h, b, "save", map[int]string{1: "", 2: "", 3: ""})
	if code != http.StatusConflict || !strings.Contains(body, staleReason) {
		t.Fatalf("stale save: %d\n%s", code, body)
	}
	d := pageDrafts(t, path, "a-grill")
	if d[1].Mark != store.DraftWords || d[1].Text != "CSV only." || d[2].Text != "flag it" {
		t.Fatalf("drafts after a stale save = %+v", d)
	}
}

// TestGrillRoundStaleQuestionRefused: a stale tab's question button is refused too and accepts nothing.
func TestGrillRoundStaleQuestionRefused(t *testing.T) {
	h, path := openGrillRound(t)
	b := pageRoundDrafts(h)
	pressRound(t, h, pageRoundDrafts(h), "words-1", map[int]string{1: "CSV only.", 2: "flag it", 3: ""})
	if code, body := pressRound(t, h, b, "accept-3", map[int]string{1: "", 2: "", 3: ""}); code != http.StatusConflict || !strings.Contains(body, staleReason) {
		t.Fatalf("stale accept-3: %d\n%s", code, body)
	}
	d := pageDrafts(t, path, "a-grill")
	if d[3].Mark != "" || d[1].Mark != store.DraftWords || d[1].Text != "CSV only." {
		t.Fatalf("drafts after a stale accept = %+v", d)
	}
}

// TestGrillRoundStaleSendRefused: with every question answered on both tabs, tab A changes Q1's words; tab B's Send
// round is refused and sends nothing.
func TestGrillRoundStaleSendRefused(t *testing.T) {
	h, path := openGrillRound(t)
	pressRound(t, h, pageRoundDrafts(h), "words-1", map[int]string{1: "CSV only.", 2: "", 3: ""})
	pressRound(t, h, pageRoundDrafts(h), "accept-2", map[int]string{2: "", 3: ""})
	pressRound(t, h, pageRoundDrafts(h), "accept-3", map[int]string{3: ""})
	b := pageRoundDrafts(h)
	if code, body := pressRound(t, h, b, "change-1", nil); code != http.StatusSeeOther {
		t.Fatalf("tab A change-1: %d\n%s", code, body)
	}
	if code, body := pressRound(t, h, pageRoundDrafts(h), "words-1", map[int]string{1: "CSV and JSON."}); code != http.StatusSeeOther {
		t.Fatalf("tab A words-1: %d\n%s", code, body)
	}
	if code, body := pressRound(t, h, b, "send", nil); code != http.StatusConflict || !strings.Contains(body, staleReason) {
		t.Fatalf("stale send: %d\n%s", code, body)
	}
	if msgs := pageMessages(t, path); len(msgs) != 0 {
		t.Fatalf("a stale send sent %+v", msgs)
	}
	for _, e := range conversation(t, path, "app", "a-grill") {
		if e.Kind == store.ConversationRoundAnswers {
			t.Fatalf("a stale send recorded answers: %+v", e)
		}
	}
	// The current tab sends.
	if code, body := pressRound(t, h, pageRoundDrafts(h), "send", nil); code != http.StatusSeeOther {
		t.Fatalf("current send: %d\n%s", code, body)
	}
	if msgs := pageMessages(t, path); len(msgs) != 1 {
		t.Fatalf("the current send sent %+v", msgs)
	}
}

// TestGrillRoundStaleNoChangeRefused: a stale press that would change nothing, and is no retry of a post that saved,
// is refused all the same.
func TestGrillRoundStaleNoChangeRefused(t *testing.T) {
	h, path := openGrillRound(t)
	b := pageRoundDrafts(h)
	pressRound(t, h, b, "save", map[int]string{1: "CSV only.", 2: "", 3: ""})
	pressRound(t, h, pageRoundDrafts(h), "save", map[int]string{1: "CSV only.", 2: "yes", 3: ""})
	if d := pageDrafts(t, path, "a-grill"); d[1].Text != "CSV only." || d[2].Text != "yes" {
		t.Fatalf("tab A's drafts = %+v", d)
	}
	if code, body := pressRound(t, h, b, "save", map[int]string{1: "CSV only.", 2: "yes", 3: ""}); code != http.StatusConflict || !strings.Contains(body, staleReason) {
		t.Fatalf("stale press changing nothing: %d\n%s", code, body)
	}
}

// TestGrillRoundCurrentPressSaves: a press carrying the number the page shows saves, and the refused page carries
// the fresh number, so a press from it saves.
func TestGrillRoundCurrentPressSaves(t *testing.T) {
	h, path := openGrillRound(t)
	b := pageRoundDrafts(h)
	pressRound(t, h, b, "save", map[int]string{1: "CSV only.", 2: "", 3: ""})
	_, refused := pressRound(t, h, b, "save", map[int]string{1: "CSV", 2: "yes", 3: ""})
	fresh := pageRoundDrafts(h)
	if fresh == b || !strings.Contains(refused, `<input type="hidden" name="drafts" value="`+fresh+`">`) {
		t.Fatalf("the refused page does not carry the fresh number %q:\n%s", fresh, refused)
	}
	if code, body := pressRound(t, h, fresh, "save", map[int]string{1: "CSV", 2: "yes", 3: ""}); code != http.StatusSeeOther {
		t.Fatalf("current save: %d\n%s", code, body)
	}
	if d := pageDrafts(t, path, "a-grill"); d[1].Text != "CSV" || d[2].Text != "yes" {
		t.Fatalf("drafts after a current save = %+v", d)
	}
}

// TestGrillRoundRetryReplays: the same post sent twice with the same drafts redirects both times and writes once.
func TestGrillRoundRetryReplays(t *testing.T) {
	h, _ := openGrillRound(t)
	b := pageRoundDrafts(h)
	for i := range 2 {
		if code, body := pressRound(t, h, b, "accept-2", map[int]string{1: "CSV only.", 2: "", 3: "maybe"}); code != http.StatusSeeOther {
			t.Fatalf("press %d: %d\n%s", i+1, code, body)
		}
	}
	once := pageRoundDrafts(h)
	if once == b {
		t.Fatalf("the press saved nothing")
	}
	pressRound(t, h, b, "accept-2", map[int]string{1: "CSV only.", 2: "", 3: "maybe"})
	if again := pageRoundDrafts(h); again != once {
		t.Fatalf("a retry wrote drafts: %s, then %s", once, again)
	}
}

// TestGrillRoundNoDraftsField: a form from an older build, with no drafts, saves on a round with no drafts and is
// refused once one exists.
func TestGrillRoundNoDraftsField(t *testing.T) {
	h, _ := openGrillRound(t)
	if code, body := pressRound(t, h, "", "save", map[int]string{1: "CSV only."}); code != http.StatusSeeOther {
		t.Fatalf("first save with no drafts field: %d\n%s", code, body)
	}
	if code, body := pressRound(t, h, "", "save", map[int]string{1: "CSV"}); code != http.StatusConflict || !strings.Contains(body, staleReason) {
		t.Fatalf("save with no drafts field over a draft: %d\n%s", code, body)
	}
}
