package web

import (
	"html/template"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/spec"
)

// This file is the one layout of an ask's questions and answers: the question cards (questionsView, drawn by the
// "questions" template) and the answer row (answerTiles, drawn by the "answers" template). Every ask that shows
// questions and answers lays them out through it, so a change here changes them all; the chat text comes from the
// same parse (spec.Question.Markdown), so chat and page read the same.

// questionsView is an ask's open questions as the page shows them: the line saying there are none, or a card for
// each question, in order, up to the ones spec.Questions.Render keeps within spec.QuestionsMax.
type questionsView struct {
	None  bool
	Count int
	Cards []cardView
	// Cut says questions were left out to keep within spec.QuestionsMax, as the question text leaves them out.
	Cut bool
	// Lettered says at least one card has lettered options with a recommended one, so the ask can offer "take
	// every recommendation" as its recommended answer.
	Lettered bool
	// Ask is the id of the ask the cards belong to, which a pick's radio ids carry so they are unique on a page of
	// several asks.
	Ask string
}

// cardView is one question's card: its number and title, then its body and its option rows when it read cleanly
// (spec.Question.Shaped), or else its lines word for word, and its other lines.
type cardView struct {
	N        int
	Title    string
	Body     template.HTML
	Rows     []rowView
	Verbatim template.HTML
	More     template.HTML
	// Pick says the card's lettered rows are a radio group, pick-<N>, on the Guildmaster's form for an ask that
	// offers taking every recommendation (withPicks).
	Pick bool
}

// rowView is one option row: its mark, such as "(b)" or "Alternative", its text and, on the recommended row, the
// reason the document gives for it.
type rowView struct {
	Mark string
	Text template.HTML
	Why  template.HTML
	Rec  bool
	// Letter is a lettered row's bare letter, such as "b"; "" on the rows of a question that has no letters.
	// Checked says its radio is the card's pick.
	Letter  string
	Checked bool
}

// newQuestionsView lays out q, read by spec, as cards: the recommended option first and marked, letters as written.
// Option text, bodies and reasons are Markdown rendered by goldmark at its safe defaults; a question that did not
// read cleanly shows its lines word for word, as written, with no marker. ask is the id of the ask the cards belong
// to.
func newQuestionsView(q spec.Questions, ask string) *questionsView {
	if q.None {
		return &questionsView{None: true, Ask: ask}
	}
	kept := q.Fit(spec.QuestionsMax)
	v := &questionsView{Count: len(q.Items), Cut: kept < len(q.Items), Ask: ask}
	for _, it := range q.Items[:kept] {
		c := cardView{N: it.N, Title: it.Title}
		switch {
		case it.Lettered():
			v.Lettered = true
			for i, o := range it.Pick() {
				r := rowView{Mark: "(" + o.Letter + ")", Text: inlineMarkdown(o.Text), Letter: o.Letter}
				if i == 0 {
					r.Rec, r.Why = true, inlineMarkdown(it.Why)
				}
				c.Rows = append(c.Rows, r)
			}
		case it.Shaped():
			c.Rows = []rowView{{Text: inlineMarkdown(it.Why), Rec: true}, {Mark: "Alternative", Text: inlineMarkdown(it.Alternative)}}
		default:
			if _, lines, _ := strings.Cut(it.Text, "\n"); strings.TrimSpace(lines) != "" {
				c.Verbatim = RenderMarkdown([]byte(lines))
			}
		}
		if it.Shaped() {
			if it.Body != "" {
				c.Body = RenderMarkdown([]byte(it.Body))
			}
			if it.More != "" {
				c.More = RenderMarkdown([]byte(it.More))
			}
		}
		v.Cards = append(v.Cards, c)
	}
	return v
}

// withPicks makes each lettered card a radio group, pick-<N>, its recommended row checked, as the Guildmaster's form
// shows them on an ask that offers taking every recommendation; picks, the changed picks a refused answer sent, by
// question number, check their rows instead. A pick naming no row of its card leaves the recommendation checked.
func (v *questionsView) withPicks(picks map[int]string) {
	for i := range v.Cards {
		c := &v.Cards[i]
		if len(c.Rows) == 0 || c.Rows[0].Letter == "" {
			continue
		}
		c.Pick = true
		want := picks[c.N]
		if !slices.ContainsFunc(c.Rows, func(r rowView) bool { return r.Letter == want }) {
			want = ""
		}
		for j := range c.Rows {
			r := &c.Rows[j]
			r.Checked = (want == "" && r.Rec) || (want != "" && r.Letter == want)
		}
	}
}

// inlineMarkdown renders a line of Markdown with goldmark at its safe defaults for use inside a row: the paragraph's
// contents when it renders as one paragraph, or else the text escaped.
func inlineMarkdown(s string) template.HTML {
	if s == "" {
		return ""
	}
	h := strings.TrimSuffix(string(RenderMarkdown([]byte(s))), "\n")
	if inner, ok := strings.CutPrefix(h, "<p>"); ok && strings.HasSuffix(inner, "</p>") && !strings.Contains(inner, "<p>") {
		return template.HTML(strings.TrimSuffix(inner, "</p>"))
	}
	return template.HTML(template.HTMLEscapeString(s))
}

// The words a recommended answer row shows: Approved takes every recommendation, and Changes requested asks for the
// answers to change in words, which the Director maps by letter.
const (
	recommendedText = "take every recommendation above; the task goes on"
	changesText     = "send it back with your words, such as “Q2 (a)”"
	wordsHint       = "optional with an answer"
	recommendedHint = "optional with an answer; to change an answer, name it, such as “Q2 (a)”"
)

// answerTiles are an ask's answer tiles, one per option, with the words and tone of the option table (OptionMeaning).
// With recommend, as on an approval whose questions carry recommendations, approved comes first as the recommended
// answer, "take every recommendation", and changes_requested asks for the answers to change; the options' values do
// not change.
func answerTiles(kind string, options []string, recommend bool) []tileView {
	var out []tileView
	for _, o := range options {
		m, _ := OptionMeaning(kind, o)
		t := tileView{Option: o, Word: OptionWord(o), Text: m.Text, Tone: m.Tone}
		if recommend {
			switch o {
			case "approved":
				t.Rec, t.Text = true, recommendedText
			case "changes_requested":
				t.Text = changesText
			}
		}
		out = append(out, t)
	}
	sortTiles(out)
	return out
}

// markRecommended marks the tile of option, the answer the Director recommends on a question it opened, and puts it
// first, as an approval's recommended answer is.
func markRecommended(tiles []tileView, option string) {
	for i := range tiles {
		if tiles[i].Option == option {
			tiles[i].Rec = true
		}
	}
	sortTiles(tiles)
}

// sortTiles puts the recommended tile first, the rest in their order.
func sortTiles(out []tileView) {
	slices.SortStableFunc(out, func(a, b tileView) int {
		switch {
		case a.Rec == b.Rec:
			return 0
		case a.Rec:
			return -1
		}
		return 1
	})
}
