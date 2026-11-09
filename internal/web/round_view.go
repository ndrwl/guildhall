package web

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
)

// roundView is a grill round as the page shows it: as one form, the open round, or folded into one Director row,
// sent with its answers or not sent.
type roundView struct {
	// ID is its anchor: the attempt's tag for the open round, so a link to the attempt lands on it, else
	// <tag>-round-<k>.
	ID, Tag, Chip string
	// Stage is the tag's step, such as grill.
	Stage string
	// Number is the round's conversation entry, which the form posts; K is its place among its attempt's rounds.
	Number, K int
	// Drafts is the highest draft entry number among the open round's drafts the page renders, 0 with none, which
	// the form posts so a press from a page older than a saved draft is refused.
	Drafts    int
	Questions []roundQuestionView
	// Total is how many questions it asks and Left how many have no answer yet; Acceptable is how many of those
	// have an empty saved box, which Accept all recommendations accepts; Accept all and send shows while Left is not 0.
	Total, Left, Acceptable int
	// Form says the request is from the Guildmaster's login, which answers as Login; Answering says the page answers
	// at all.
	Form, Answering bool
	Login           string
	Action, Token   string
	// Refused is why the last press of one of its buttons was refused.
	Refused string
	// Line is a folded round's summary, such as "Round 1 · 4 questions · sent 21:06 · 3 accepted, 1 your words";
	// Void marks a round never sent.
	Line string
	Sent bool
	Void bool
	// Bad is a round whose text no longer reads, shown as written.
	Bad template.HTML
}

// roundQuestionView is one question of a round: its number, anchor, title, body and recommended answer; on the open
// round the words in its box, its mark and the line saying when unmarked words were saved, folded to one line with
// its gist once answered; on a sent round its answer.
type roundQuestionView struct {
	N         int
	ID, Title string
	Body      template.HTML
	Rec       string
	Words     string
	Mark      string
	Saved     string
	Folded    bool
	Done      string
	Gist      string
	Accepted  bool
	Answer    template.HTML
}

// roundQuestionID is the anchor of question n of the open round of tag.
func roundQuestionID(tag string, n int) string { return tag + "-q" + strconv.Itoa(n) }

// roundTitle names a round as the rail and its card do, "Grill round <k>", or "Round <k>" for a tag of no grill.
func roundTitle(stage string, k int) string {
	if stage == "grill" {
		return "Grill round " + strconv.Itoa(k)
	}
	return "Round " + strconv.Itoa(k)
}

// openRound is the open round of the current attempt as one form, with the Guildmaster's saved drafts, or the words
// a refused press kept; nil when no round is open.
func (s *Server) openRound(t Task, p *talkPlan, base string, st pageState) *roundView {
	m := p.open
	if m == nil {
		return nil
	}
	v := &roundView{ID: m.Tag, Tag: m.Tag, Chip: chipLabel(m.Stage, m.Tag), Stage: m.Stage, Number: m.Number, K: p.ordinal[m.Number],
		Answering: s.answering(), Form: st.Answer, Login: st.Login, Refused: st.RoundRefused}
	if v.Form {
		v.Action, v.Token = base+"/round", st.Token
	}
	qs, err := ParseRound(m.Text)
	if err != nil {
		v.Bad = RenderMarkdown([]byte(m.Text))
		return v
	}
	drafts := map[int]Draft{}
	for _, d := range t.Drafts {
		if d.Tag == m.Tag && d.Round == m.Number {
			drafts[d.Question] = d
			v.Drafts = max(v.Drafts, d.Number)
		}
	}
	v.Total = len(qs)
	for _, q := range qs {
		d := drafts[q.N]
		qv := roundQuestionView{N: q.N, ID: roundQuestionID(m.Tag, q.N), Title: q.Title, Rec: q.Recommended, Words: d.Text, Mark: d.Mark}
		if q.Body != "" {
			qv.Body = RenderMarkdown([]byte(q.Body))
		}
		if words, ok := st.RoundBoxes[q.N]; ok && st.RoundRefused != "" {
			qv.Words = words
		}
		switch d.Mark {
		case DraftAccept:
			qv.Folded, qv.Done, qv.Gist = true, "✓ Accepted", webGist(q.Recommended)
		case DraftWords:
			qv.Folded, qv.Done, qv.Gist = true, "✓ Your words, saved", webGist(d.Text)
		default:
			v.Left++
			if strings.TrimSpace(d.Text) == "" {
				v.Acceptable++
			}
			if strings.TrimSpace(d.Text) != "" && !d.At.IsZero() {
				qv.Saved = "saved " + s.at(t, d.At) + " · not an answer yet"
			}
		}
		v.Questions = append(v.Questions, qv)
	}
	return v
}

// Name is the round's name, such as "Grill round 2".
func (v roundView) Name() string { return roundTitle(v.Stage, v.K) }

// Progress is the open round's progress, such as "4 of 6", for Needs you.
func (v roundView) Progress() string {
	return fmt.Sprintf("%d of %d", v.Total-v.Left, v.Total)
}

// roundRow is a round folded into one Director row: sent, with its answers, or, with none, not sent, its saved
// answers dropped once the task closed.
func (s *Server) roundRow(t Task, p *talkPlan, m Message) *roundView {
	k := p.ordinal[m.Number]
	v := &roundView{ID: m.Tag + "-round-" + strconv.Itoa(k), Tag: m.Tag, Chip: chipLabel(m.Stage, m.Tag), Stage: m.Stage, Number: m.Number, K: k}
	qs, err := ParseRound(m.Text)
	if err != nil {
		v.Bad, v.Line = RenderMarkdown([]byte(m.Text)), "Round "+strconv.Itoa(k)
		return v
	}
	v.Total = len(qs)
	line := fmt.Sprintf("Round %d · %d %s", k, len(qs), plural(len(qs), "question", "questions"))
	a, sent := p.answers[m.Number]
	var answers []RoundAnswer
	switch {
	case sent:
		v.Sent = true
		answers = ParseRoundAnswers(qs, a.Text)
		line += " · sent " + s.at(t, a.At)
		if tally := RoundTally(answers); tally != "" {
			line += " · " + tally
		}
	case t.State == "open":
		v.Void, line = true, "⊘ "+line+" · not sent"
	default:
		v.Void, line = true, "⊘ "+line+" · not sent; its saved answers were dropped"
	}
	v.Line = line
	for i, q := range qs {
		qv := roundQuestionView{N: q.N, Title: q.Title, Rec: q.Recommended}
		if q.Body != "" {
			qv.Body = RenderMarkdown([]byte(q.Body))
		}
		if sent {
			qv.Accepted = answers[i].Accepted
			if w := answers[i].Words; w != "" {
				qv.Answer = RenderMarkdown([]byte(w))
			}
		}
		v.Questions = append(v.Questions, qv)
	}
	return v
}

// webGist is the first line of s, cut to 80 characters, for a folded question's one line.
func webGist(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(line); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return line
}

func latestNumber(m *Message) int {
	if m == nil {
		return 0
	}
	return m.Number
}
