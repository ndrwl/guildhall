package cli

import (
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

// openGrillSummary opens testSummary on a-grill's running grill-1 and returns the page's handler, store path and
// surface log.
func openGrillSummary(t *testing.T) (http.Handler, string, *surfaceLog) {
	t.Helper()
	h, path, log := pageFixture(t)
	startGrill(t, path)
	if code, _, errOut := runGrill(t, testSummary, "summary", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--summary-file", "-"); code != 0 {
		t.Fatalf("grill summary open = %d %s", code, errOut)
	}
	return h, path, log
}

// pressSummary presses act, confirm or changes, on a-grill's summary entry n with words.
func pressSummary(t *testing.T, h http.Handler, n int, act, words string) pressResult {
	t.Helper()
	w := postRoute(t, h, "a-grill", "summary", url.Values{"tag": {"grill-1"}, "summary": {strconv.Itoa(n)}, "act": {act}, "text": {words}})
	return pressResult{Code: w.Code, Location: w.Header().Get("Location"), Body: w.Body.String()}
}

// pressResult is what a press answered.
type pressResult struct {
	Code           int
	Location, Body string
}

// TestGrillSummaryConfirmOnThePage: the open summary is a card on Now under the attempt, counted in Needs you and
// named by the band; Confirm with words records the answer and a grill-answered message in one store open, naming
// grill summary show and never the words, wakes the Director, and a second Confirm sends once; the page then folds
// the summary to one row, grill summary show prints it, and the other button is refused.
func TestGrillSummaryConfirmOnThePage(t *testing.T) {
	h, path, log := openGrillSummary(t)
	body := gmPage(h, "a-grill")
	for _, want := range []string{`<div class="ask summary" id="grill-1">`, `action="/app/a-grill/summary"`, `<input type="hidden" name="summary" value="1">`,
		`<span class="kind">Grill summary</span>`, `<li>Export CSV only.</li>`, `<li><a href="#grill-1"><b>Grill summary</b><span>#grill-1</span></a></li>`,
		`value="confirm"><b>Confirm</b>`, `value="changes"><b>Request changes</b>`, "Waiting for you on the grill summary:", "its grill summary is below, to confirm here."} {
		if !strings.Contains(body, want) {
			t.Fatalf("the page lacks %s:\n%s", want, body)
		}
	}
	for i := range 2 {
		if r := pressSummary(t, h, 1, "confirm", "Name the flag export-csv."); r.Code != http.StatusSeeOther || r.Location != "/app/a-grill#grill-1-talk" {
			t.Fatalf("confirm %d: %d %q\n%s", i+1, r.Code, r.Location, r.Body)
		}
	}
	msgs := pageMessages(t, path)
	if len(msgs) != 1 || msgs[0].ID != web.SummaryID("app", "a-grill", 1) || msgs[0].Type != store.MessageGrillAnswered ||
		!strings.Contains(msgs[0].Text, "Grill summary 1 of grill-1 of app/a-grill was confirmed on the task page") ||
		!strings.Contains(msgs[0].Text, "guildhall grill summary show --repo app --task a-grill --attempt grill-1 --summary 1") ||
		!strings.Contains(msgs[0].Text, "finish the stage") || strings.Contains(msgs[0].Text, "export-csv") {
		t.Fatalf("messages = %+v", msgs)
	}
	if !slices.ContainsFunc(log.events, func(e string) bool { return strings.Contains(e, "grill-answered 1") }) {
		t.Errorf("events %q: grill-answered does not wake the Director", log.events)
	}
	got := conversation(t, path, "app", "a-grill")
	if len(got) != 2 || got[1].Saying != (store.Saying{Author: store.ConversationGuildmasterPage, Tag: "grill-1", Kind: store.ConversationSummaryAnswer,
		Text: "Confirmed\n\nName the flag export-csv.", Round: 1, Option: store.SummaryConfirmed}) {
		t.Fatalf("conversation = %+v", got)
	}
	if code, out, errOut := runGrill(t, "", "summary", "show", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1"); code != 0 ||
		!strings.Contains(out, "· Confirmed ·") || !strings.HasSuffix(out, "## The Guildmaster's words\n\nName the flag export-csv.\n") {
		t.Errorf("grill summary show = %d %q %s", code, out, errOut)
	}
	body = gmPage(h, "a-grill")
	if strings.Contains(body, `action="/app/a-grill/summary"`) || !strings.Contains(body, `<summary>Grill summary · confirmed `) ||
		!strings.Contains(body, `<p class="ra">✓ Confirmed</p><div class="md"><p>Name the flag export-csv.</p>`) || strings.Contains(body, "Waiting for you on the grill summary") {
		t.Errorf("the answered summary does not fold:\n%s", body)
	}
	if r := pressSummary(t, h, 1, "changes", "JSON too."); r.Code != http.StatusConflict || !strings.Contains(r.Body, "already answered: you chose Confirmed on this summary") ||
		!strings.Contains(r.Body, "JSON too.</textarea>") {
		t.Errorf("the other button after Confirm: %d\n%s", r.Code, r.Body)
	}
	if n := len(pageMessages(t, path)); n != 1 {
		t.Errorf("the refused press sent: %d messages", n)
	}
}

// TestGrillSummaryTaskList: on the task list an open summary waits for the Guildmaster, named by its sentence, since
// it was put up; once answered it no longer does.
func TestGrillSummaryTaskList(t *testing.T) {
	h, path, _ := openGrillSummary(t)
	src := webSourceFor(t, filepath.Dir(path))
	row := func() web.Listed {
		t.Helper()
		l, err := src.Tasks()
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range l.Tasks {
			if x.ID == "a-grill" {
				return x
			}
		}
		t.Fatal("a-grill is not listed")
		return web.Listed{}
	}
	put := conversation(t, path, "app", "a-grill")[0].At
	if x := row(); x.Group != web.GroupYou || x.Where.Lead != "Waiting for you on the grill summary:" || x.Since == nil || !x.Since.Equal(put) {
		t.Errorf("the open summary's row = %+v, want since %v", x, put)
	}
	if r := pressSummary(t, h, 1, "confirm", ""); r.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d", r.Code)
	}
	if x := row(); x.Where.Lead == "Waiting for you on the grill summary:" {
		t.Errorf("the answered summary still waits: %+v", x.Where)
	}
}

// TestGrillSummaryChangesOnThePage: Request changes needs words; with them it records the answer and a
// grill-answered message telling the Director to carry the interview on, and the next round can open.
func TestGrillSummaryChangesOnThePage(t *testing.T) {
	h, path, _ := openGrillSummary(t)
	if r := pressSummary(t, h, 1, "changes", " \r\n "); r.Code != http.StatusBadRequest || !strings.Contains(r.Body, "Request changes takes your words") {
		t.Fatalf("changes with no words: %d\n%s", r.Code, r.Body)
	}
	if n := len(conversation(t, path, "app", "a-grill")); n != 1 {
		t.Fatalf("the refused press wrote: %d entries", n)
	}
	if r := pressSummary(t, h, 1, "changes", "Export JSON too."); r.Code != http.StatusSeeOther {
		t.Fatalf("changes: %d\n%s", r.Code, r.Body)
	}
	msgs := pageMessages(t, path)
	if len(msgs) != 1 || msgs[0].ID != web.SummaryID("app", "a-grill", 1) || msgs[0].Type != store.MessageGrillAnswered ||
		!strings.Contains(msgs[0].Text, "Changes were requested to grill summary 1 of grill-1 of app/a-grill on the task page") ||
		!strings.Contains(msgs[0].Text, "carry the interview on") || strings.Contains(msgs[0].Text, "JSON") {
		t.Fatalf("messages = %+v", msgs)
	}
	if got := conversation(t, path, "app", "a-grill"); len(got) != 2 || got[1].Option != store.SummaryChangesRequested || got[1].Text != "Changes requested\n\nExport JSON too." {
		t.Fatalf("conversation = %+v", got)
	}
	if body := gmPage(h, "a-grill"); !strings.Contains(body, `<summary>Grill summary · changes requested `) {
		t.Errorf("the answered summary does not fold as changes requested")
	}
	if r := pressSummary(t, h, 1, "changes", "Export JSON too."); r.Code != http.StatusSeeOther || len(pageMessages(t, path)) != 1 {
		t.Errorf("a second Request changes: %d", r.Code)
	}
	if code, _, errOut := runGrill(t, testRound, "round", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--questions-file", "-"); code != 0 {
		t.Fatalf("round after Request changes = %d %s", code, errOut)
	}
}

// TestGrillSummaryStalePage: a press from a page loaded before the Director opened a newer summary, after its
// attempt finished, or after its task closed, is refused with the words kept and nothing written.
func TestGrillSummaryStalePage(t *testing.T) {
	t.Run("newer summary", func(t *testing.T) {
		h, path, _ := openGrillSummary(t)
		answerSummary(t, path, "answer-1", 1, store.SummaryChangesRequested, "JSON too.")
		if code, _, errOut := runGrill(t, "- CSV and JSON.", "summary", "open", "--store", path, "--repo", "app", "--task", "a-grill", "--attempt", "grill-1", "--summary-file", "-"); code != 0 {
			t.Fatalf("revised summary = %d %s", code, errOut)
		}
		// The first summary is answered, so a Confirm on it is the other button.
		if r := pressSummary(t, h, 1, "confirm", "kept words"); r.Code != http.StatusConflict || !strings.Contains(r.Body, "already answered") {
			t.Errorf("confirm on the answered summary: %d\n%s", r.Code, r.Body)
		}
		if body := gmPage(h, "a-grill"); !strings.Contains(body, `<input type="hidden" name="summary" value="3">`) {
			t.Errorf("the revised summary is not the card")
		}
	})
	t.Run("newer round", func(t *testing.T) {
		h, path, _ := openGrillSummary(t)
		// A round opened after an unanswered summary is not possible through the commands; the store takes it.
		if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
			return s.AddConversation("round-after", "app", "a-grill", store.Saying{Author: store.ConversationDirector, Tag: "grill-1", Kind: store.ConversationRound, Text: testRound})
		}); err != nil {
			t.Fatal(err)
		}
		if r := pressSummary(t, h, 1, "confirm", "kept words"); r.Code != http.StatusConflict || !strings.Contains(r.Body, "the Director opened a newer round or summary") ||
			!strings.Contains(r.Body, "kept words</textarea>") {
			t.Errorf("confirm after a newer round: %d\n%s", r.Code, r.Body)
		}
		if n := len(pageMessages(t, path)); n != 0 {
			t.Errorf("the refused press sent %d messages", n)
		}
	})
	t.Run("attempt finished", func(t *testing.T) {
		h, path, _ := openGrillSummary(t)
		if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
			return s.FinishAttempt("finish-grill", "app", "a-grill", "grill", 1, store.AttemptSucceeded)
		}); err != nil {
			t.Fatal(err)
		}
		r := pressSummary(t, h, 1, "confirm", "kept words")
		if r.Code != http.StatusConflict || !strings.Contains(r.Body, "grill-1 is no longer running") || !strings.Contains(r.Body, "kept words</textarea>") {
			t.Errorf("confirm after the attempt finished: %d\n%s", r.Code, r.Body)
		}
		if !strings.Contains(r.Body, "⊘ Grill summary · not answered") || strings.Contains(r.Body, `action="/app/a-grill/summary"`) {
			t.Errorf("the summary of a finished attempt does not fold as not answered:\n%s", r.Body)
		}
		if n := len(pageMessages(t, path)); n != 0 {
			t.Errorf("the refused press sent %d messages", n)
		}
	})
	t.Run("task closed", func(t *testing.T) {
		h, path, _ := openGrillSummary(t)
		if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
			return s.CancelTask("cancel", "app", "a-grill", "not needed")
		}); err != nil {
			t.Fatal(err)
		}
		if r := pressSummary(t, h, 1, "changes", "kept words"); r.Code != http.StatusConflict || !strings.Contains(r.Body, "this task is closed") ||
			!strings.Contains(r.Body, "kept words</textarea>") {
			t.Errorf("changes on a closed task: %d\n%s", r.Code, r.Body)
		}
		if n := len(pageMessages(t, path)); n != 0 {
			t.Errorf("the refused press sent %d messages", n)
		}
	})
	t.Run("no such summary", func(t *testing.T) {
		h, _, _ := openGrillSummary(t)
		if r := pressSummary(t, h, 7, "confirm", ""); r.Code != http.StatusNotFound {
			t.Errorf("confirm on no summary: %d", r.Code)
		}
	})
}
