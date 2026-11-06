package web

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
)

// This file holds the grill summary on the page (docs/web.md#grill-summary): the Director's closing summary of a
// grill, asked as one card with Confirm and Request changes, its route and its row once answered.

// The buttons of the grill summary's card, as its form posts them in act.
const (
	SummaryConfirm = "confirm"
	SummaryChanges = "changes"
)

// summaryName is how the card, the rail and the folded row name a grill summary.
const summaryName = "Grill summary"

// SummaryID is the message id of the grill-answered message for one grill summary, keyed on its conversation entry
// number, sent once since a summary is answered once.
func SummaryID(repo, task string, summary int) string {
	return "page-summary." + AnsweredID(repo, task, "summary:"+strconv.Itoa(summary))[len("page-answered."):]
}

// summaryView is a grill summary as the page shows it: the open summary as one card with Confirm and Request
// changes, or folded into one Director row, answered or not answered.
type summaryView struct {
	// ID is its anchor: the attempt's tag for the open summary, so a link to the attempt lands on it, else
	// <tag>-summary-<k>.
	ID, Tag, Chip string
	// Number is the summary's conversation entry, which the form posts.
	Number int
	// Body is the summary in full, rendered by goldmark.
	Body template.HTML
	// Since is when the open summary was put up.
	Since string
	// Form says the request is from the Guildmaster's login, which answers as Login.
	Form          bool
	Login         string
	Action, Token string
	// Refused is why the last press of one of its buttons was refused, and Words the words its box holds.
	Refused, Words string
	// Line is a folded summary's one line, such as "Grill summary · confirmed 21:06"; Void marks one never
	// answered; Outcome is the answer's outcome, such as "Confirmed", and Answer the Guildmaster's words with it.
	Line    string
	Void    bool
	Outcome string
	Answer  template.HTML
}

// Name is the summary's name, "Grill summary".
func (summaryView) Name() string { return summaryName }

// openSummary is the open grill summary of the current attempt as one card, with the words a refused press kept;
// nil when none is open.
func (s *Server) openSummary(t Task, p *talkPlan, base string, st pageState) *summaryView {
	m := p.summary
	if m == nil {
		return nil
	}
	v := &summaryView{ID: m.Tag, Tag: m.Tag, Chip: chipLabel(m.Stage, m.Tag), Number: m.Number, Body: RenderMarkdown([]byte(m.Text)),
		Form: st.Answer && s.answering(), Login: st.Login, Refused: st.SummaryRefused, Words: st.SummaryWords}
	if !m.At.IsZero() {
		v.Since = "since " + s.at(t, m.At)
	}
	if v.Form {
		v.Action, v.Token = base+"/summary", st.Token
	}
	return v
}

// summaryRow is a grill summary folded into one Director row: answered, with its outcome and the Guildmaster's
// words, or not answered.
func (s *Server) summaryRow(t Task, p *talkPlan, m Message) *summaryView {
	v := &summaryView{ID: m.Tag + "-summary-" + strconv.Itoa(p.summaryOrdinal[m.Number]), Tag: m.Tag, Chip: chipLabel(m.Stage, m.Tag), Number: m.Number,
		Body: RenderMarkdown([]byte(m.Text))}
	a, answered := p.summaryAnswers[m.Number]
	if !answered {
		v.Void, v.Line = true, "⊘ "+summaryName+" · not answered"
		return v
	}
	v.Outcome = SummaryOutcome(a.Option)
	v.Line = summaryName + " · " + lowerFirst(v.Outcome)
	if !a.At.IsZero() {
		v.Line += " " + s.at(t, a.At)
	}
	if w := SummaryAnswerWords(a.Option, a.Text); w != "" {
		v.Answer = RenderMarkdown([]byte(w))
	}
	return v
}

// summaryOutcome reads a summary button's value: Confirm or Request changes, as the outcome it answers with.
func summaryOutcome(act string) (string, bool) {
	switch act {
	case SummaryConfirm:
		return SummaryConfirmed, true
	case SummaryChanges:
		return SummaryChangesRequested, true
	}
	return "", false
}

// summary handles POST /<repo>/<task>/summary, Confirm or Request changes on the open grill summary: it passes the
// gate, then gives the Answerer the summary, the outcome and the words, and redirects to the attempt's talk, where
// the answered summary folds; a refusal shows the page again with its reason on the card and the words kept.
func (s *Server) summary(w http.ResponseWriter, r *http.Request, repo, id string) {
	if !s.readForm(w, r) {
		return
	}
	p := SummaryPost{Repository: repo, TaskID: id, Tag: r.PostForm.Get("tag"), Words: r.PostForm.Get("text")}
	who, status, reason := s.gate(r)
	st := pageState{identity: who, Token: s.token, SummaryWords: p.Words}
	again := func(status int, reason string) {
		st.SummaryRefused = reason
		t, ok := s.read(w, repo, id)
		if !ok {
			return
		}
		s.render(w, status, "task", s.taskView(t, st))
	}
	if reason != "" {
		again(status, reason)
		return
	}
	outcome, ok := summaryOutcome(r.PostForm.Get("act"))
	n, err := strconv.Atoi(r.PostForm.Get("summary"))
	switch {
	case p.Tag == "":
		again(http.StatusBadRequest, "the summary names no attempt")
		return
	case err != nil || n < 1:
		again(http.StatusBadRequest, "the form names no summary")
		return
	case !ok:
		again(http.StatusBadRequest, "the form names no button of the summary")
		return
	}
	p.Summary, p.Outcome, p.Login = n, outcome, who.Login
	if s.failed(s.o.Answerer.Summary(p), "summary "+repo+"/"+id+" "+p.Tag, again) {
		return
	}
	http.Redirect(w, r, taskHref(repo, id)+"#"+url.PathEscape(talkID(p.Tag)), http.StatusSeeOther)
}
