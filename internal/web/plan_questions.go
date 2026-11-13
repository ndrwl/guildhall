package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/spec"
)

// This file holds a plan approval's open questions as the Guildmaster answers them inside the approval
// (docs/web.md#plan-open-questions): each question card gets a grill round's controls and drafts, and the approval's
// Approved or Changes requested writes the answers into its one answer, unanswered questions taking the spec's
// recommendation.

// PlanRound is the round number of a plan approval's drafts: they are saved under the approval's decision id as
// their tag, as one round of questions that the approval's answer sends.
const PlanRound = 1

// PlanQuestion is one open question of a plan approval's spec as its answer names it: its number and the answer the
// spec recommends, such as "(b) one route", "" when the question does not read into lettered options, and its
// lettered options named the same way, by letter, which a pick on its card answers with.
type PlanQuestion struct {
	N           int
	Recommended string
	Options     map[string]string
}

// planItems are the open questions of the spec plan approval a is about, the ones the page shows (spec.QuestionsMax),
// read verified as its Summary is; ok is false for an ask that is not a plan approval, or a spec with no readable
// section or none.
func (s *Server) planItems(t Task, a Ask) ([]spec.Question, bool) {
	if a.Kind != "approval" || a.SubjectDocument == "" {
		return nil, false
	}
	doc := taskDocument(t, a.SubjectDocument)
	if doc == nil || doc.Kind != "Spec" {
		return nil, false
	}
	body, status, _ := s.verified(*doc)
	if status != http.StatusOK {
		return nil, false
	}
	q, err := spec.ReadSpecQuestions(body)
	if err != nil || q.None || len(q.Items) == 0 {
		return nil, false
	}
	return q.Items[:q.Fit(spec.QuestionsMax)], true
}

// planRecommended is the answer question q recommends as its answer names it: its recommended option's letter and
// text, or "" when it has no lettered options.
func planRecommended(q spec.Question) string {
	if !q.Lettered() {
		return ""
	}
	o := q.Pick()[0]
	return "(" + o.Letter + ") " + o.Text
}

// planOptions are question q's lettered options as its answer names them, such as "(b) one route", by letter; nil
// when it has none.
func planOptions(q spec.Question) map[string]string {
	if !q.Lettered() {
		return nil
	}
	out := map[string]string{}
	for _, o := range q.Pick() {
		out[o.Letter] = "(" + o.Letter + ") " + o.Text
	}
	return out
}

// planPicks are the changed picks of picks, the letter by question number, that name an option of one of plan's
// questions other than its recommendation, so each can be that question's answer; nil when none does.
func planPicks(plan []PlanQuestion, picks map[int]string) map[int]string {
	var out map[int]string
	for _, q := range plan {
		l, ok := picks[q.N]
		if o := q.Options[l]; !ok || o == "" || o == q.Recommended {
			continue
		}
		if out == nil {
			out = map[int]string{}
		}
		out[q.N] = l
	}
	return out
}

// PlanQuestions are the open questions of t's plan approval decision as its answer names them, nil when the decision
// is not a plan approval with open questions on the page.
func (s *Server) planQuestions(t Task, decision string) []PlanQuestion {
	for _, a := range t.Asks {
		if a.ID != decision {
			continue
		}
		items, ok := s.planItems(t, a)
		if !ok {
			return nil
		}
		out := make([]PlanQuestion, len(items))
		for i, q := range items {
			out[i] = PlanQuestion{N: q.N, Recommended: planRecommended(q), Options: planOptions(q)}
		}
		return out
	}
	return nil
}

// planDrafts adds the Guildmaster's drafts to a plan approval's question cards, from t's drafts under the decision:
// each card's box, its mark, folded to one line once answered, or the line saying when unmarked words were saved;
// the heading's count of answered questions; and on Approved and Changes requested the tile line naming which
// questions take the recommendation, with the line CSS shows instead while a pick is changed (PlanPickedNote). A
// question picked on its card folds as answered too. The highest of the drafts' numbers is what the form posts as
// drafts. boxes, when not nil, are the words a refused press kept, shown in place of the saved drafts.
func (s *Server) planDrafts(t Task, v *askView, boxes map[int]string) {
	q := v.Questions
	drafts := map[int]Draft{}
	for _, d := range t.Drafts {
		if d.Tag == v.ID && d.Round == PlanRound {
			drafts[d.Question] = d
			q.Drafts = max(q.Drafts, d.Number)
		}
	}
	q.Controls = true
	var answered, left []int
	for i := range q.Cards {
		c := &q.Cards[i]
		d := drafts[c.N]
		c.ID, c.Words, c.Mark = roundQuestionID(v.ID, c.N), d.Text, d.Mark
		if words, ok := boxes[c.N]; ok {
			c.Words = words
		}
		switch d.Mark {
		case DraftAccept:
			c.Folded, c.Done, c.Gist = true, "✓ Accepted", webGist(c.Rec)
		case DraftWords:
			c.Folded, c.Done, c.Gist = true, "✓ Your words, saved", webGist(d.Text)
		case DraftPick:
			c.Folded, c.Done, c.Gist = true, "✓ Picked", webGist(pickedOption(c.Options, d.Pick))
		default:
			if strings.TrimSpace(d.Text) != "" && !d.At.IsZero() {
				c.Saved = "saved " + s.at(t, d.At) + " · not an answer yet"
			}
		}
		if c.Folded {
			answered = append(answered, c.N)
		} else {
			left = append(left, c.N)
		}
	}
	q.Answered = len(answered)
	for i := range v.Tiles {
		t := &v.Tiles[i]
		switch t.Option {
		case "approved":
			t.Picked = PlanPickedNote
			if len(answered) > 0 {
				t.Text = PlanTileNote(len(answered), left)
			}
		case "changes_requested":
			t.Picked = "send it back " + PlanPickedNote + "; add your words"
			if len(answered) > 0 {
				t.Text = "send it back " + PlanTileNote(len(answered), left) + "; add your words"
			}
		}
	}
}

// PlanPickedNote is what a plan approval's tile says, in place of its line, while a pick on its open questions is
// changed: the picks are answers, sent with the tile.
const PlanPickedNote = "with your picks and answers above; the rest take the recommendation"

// pickedOption is the option letter l names among options, as an answer names it, or "(l)" when it names none.
func pickedOption(options map[string]string, l string) string {
	if o := options[l]; o != "" {
		return o
	}
	return "(" + l + ")"
}

// PlanTileNote names what an approval's tile sends with answered of its open questions answered and left, by
// number, still open, such as "with your 2 answers; Q3 and Q4 take the recommendation".
func PlanTileNote(answered int, left []int) string {
	note := fmt.Sprintf("with your %d %s", answered, plural(answered, "answer", "answers"))
	switch len(left) {
	case 0:
		return note
	case 1:
		return note + "; " + Unanswered(left) + " takes the recommendation"
	}
	return note + "; " + Unanswered(left) + " take the recommendation"
}

// PlanAnswersHead starts the list of a plan approval's open-question answers inside its answer.
const PlanAnswersHead = "Open questions, answered on the page:"

// PlanAnswersText is the list of answers to a plan approval's open questions qs that follows its answer's option and
// words, one Markdown list item per question in order: "Q<n>: accepted recommendation <rec>", with any words after,
// "Q<n>: picked (b) <option>", with any words after, "Q<n>: <words>", or for a question in no answers, "Q<n>: no answer; takes the recommendation <rec>". It is "" when
// answers is empty: an approval with no question answered is the approval alone, which takes every recommendation.
func PlanAnswersText(qs []PlanQuestion, answers map[int]RoundAnswer) string {
	if len(answers) == 0 {
		return ""
	}
	lines := []string{PlanAnswersHead, ""}
	for _, q := range qs {
		rec := "the spec's recommendation"
		if q.Recommended != "" {
			rec = "the recommendation " + q.Recommended
		}
		a, ok := answers[q.N]
		var item string
		words := strings.TrimSpace(a.Words)
		switch {
		case !ok:
			item = "no answer; takes " + rec
		case a.Pick != "" && words == "":
			item = "picked " + pickedOption(q.Options, a.Pick)
		case a.Pick != "":
			item = "picked " + pickedOption(q.Options, a.Pick) + "; " + words
		case a.Accepted && words == "":
			item = "accepted " + rec
		case a.Accepted:
			item = "accepted " + rec + "; " + words
		default:
			item = words
		}
		lines = append(lines, "- Q"+strconv.Itoa(q.N)+": "+listItemIndent(item))
	}
	return strings.Join(lines, "\n")
}

// listItemIndent indents every line of s after its first by two spaces, so it stays in its list item; blank lines
// stay blank.
func listItemIndent(s string) string {
	ls := strings.Split(s, "\n")
	for i := 1; i < len(ls); i++ {
		if strings.TrimSpace(ls[i]) != "" {
			ls[i] = "  " + ls[i]
		} else {
			ls[i] = ""
		}
	}
	return strings.Join(ls, "\n")
}
