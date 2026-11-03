package web

import (
	"fmt"
	"html/template"
	"slices"
	"strings"
)

// talkShown is how many of a talk's newest messages show on Now before the rest fold under "N earlier messages".
const talkShown = 3

// messageView is one ledger row of the conversation: its classes (msg, dir for the Director, wait for a
// recommendation its open ask still waits on), who wrote it, where the Guildmaster wrote it (page or chat), when,
// and its Markdown body rendered by goldmark.
type messageView struct {
	Class, Who, Src, Time string
	// Rec is the answer a recommendation recommends, as its tile names it.
	Rec  string
	Body template.HTML
}

// talkView is the talk on one attempt or decision shown on Now: its tag, its anchor <tag>-talk ("" when the talk
// shows inside its ask and this is only the Say box), its chip, and its messages oldest first, the newest talkShown
// shown and the earlier ones folded; Say is the Say box at its end, on the current stage's talk only.
type talkView struct {
	Tag, ID, Chip  string
	Shown, Earlier []messageView
	Say            *sayView
}

// chipLabel is a tag as its chip says it: "<stage> · <tag>", or the tag alone when it names no stage.
func chipLabel(stage, tag string) string {
	if stage == "" || stage == tag {
		return tag
	}
	return stage + " · " + tag
}

// talkID is the anchor of the talk on tag.
func talkID(tag string) string { return tag + "-talk" }

// storyID is the anchor of the story node of tag.
func storyID(tag string) string { return "story-" + tag }

// talkPlan is where each tag's talk shows: under its ask, in a talk on Now, or folded under its story node.
type talkPlan struct {
	byTag map[string][]Message
	// order is the tags in the order of their first message.
	order []string
	// now says a tag's talk shows on Now, "ask" inside its ask or "stage" under the asks.
	now map[string]string
	// story says a tag's talk is folded under its story node.
	story map[string]bool
	// waiting are the open asks a recommendation can still wait on.
	waiting map[string]bool
	// nodes are the tags that have a story node.
	nodes map[string]bool
	// guildmaster says the page is the Guildmaster's own, so their messages say You.
	guildmaster bool
}

func newTalkPlan(t Task, st pageState) *talkPlan {
	p := &talkPlan{byTag: map[string][]Message{}, now: map[string]string{}, story: map[string]bool{}, waiting: map[string]bool{},
		nodes: map[string]bool{}, guildmaster: st.Answer}
	for _, n := range t.Story {
		if n.Tag != "" {
			p.nodes[n.Tag] = true
		}
	}
	msgs := slices.Clone(t.Conversation)
	slices.SortStableFunc(msgs, func(a, b Message) int { return a.Number - b.Number })
	for _, m := range msgs {
		if _, ok := p.byTag[m.Tag]; !ok {
			p.order = append(p.order, m.Tag)
		}
		p.byTag[m.Tag] = append(p.byTag[m.Tag], m)
	}
	for _, a := range t.Asks {
		if !a.Deferred {
			p.waiting[a.ID] = true
		}
	}
	return p
}

func (s *Server) messageView(t Task, p *talkPlan, m Message) messageView {
	v := messageView{Class: "msg", Body: RenderMarkdown([]byte(m.Text))}
	switch m.Author {
	case AuthorDirector:
		v.Class, v.Who = "msg dir", "Director"
		if m.Kind == KindRecommendation {
			v.Rec = OptionWord(m.Option)
			if p.waiting[m.Tag] {
				v.Class += " wait"
			}
		}
	default:
		v.Who = "Guildmaster"
		if p.guildmaster {
			v.Who = "You"
		}
		switch m.Author {
		case AuthorGuildmasterPage:
			v.Src = "page"
		case AuthorGuildmasterChat:
			v.Src = "chat"
		}
		if m.Kind == KindConfirmed {
			v.Class += " conf"
			v.Body = confirmedBody(m)
		}
	}
	if !m.At.IsZero() {
		v.Time = s.when(m.At)
		if s.day(m.At) == s.day(t.ReadAt) {
			v.Time = s.clock(m.At)
		}
	}
	return v
}

// confirmedBody is a confirmation as its row says it, "↩ Confirmed: <answer>", over the words the answer carried,
// which follow the option after a blank line, rendered as Markdown.
func confirmedBody(m Message) template.HTML {
	head := "<p>↩ Confirmed: <b>" + template.HTMLEscapeString(OptionWord(m.Option)) + "</b></p>\n"
	words := m.Text
	if rest, ok := strings.CutPrefix(words, m.Option); ok {
		words = rest
	}
	if words = strings.TrimSpace(words); words == "" {
		return template.HTML(head) //nolint:gosec // escaped above
	}
	return template.HTML(head) + RenderMarkdown([]byte(words)) //nolint:gosec // escaped above, and goldmark's safe output
}

func (s *Server) messageViews(t Task, p *talkPlan, msgs []Message) []messageView {
	var out []messageView
	for _, m := range msgs {
		out = append(out, s.messageView(t, p, m))
	}
	return out
}

// askTalk puts the talk on an ask inside its card, under "Talk on #<id>", and gives the ask its tag chip, which links
// to its talk, or to its story node when it has none.
func (s *Server) askTalk(t Task, p *talkPlan, a Ask, v *askView) {
	v.Chip, v.ChipHref = chipLabel(a.Stage, a.ID), "#"+a.ID
	if p.nodes[a.ID] {
		v.ChipHref = "#" + storyID(a.ID)
	}
	if msgs := p.byTag[a.ID]; len(msgs) > 0 {
		v.Talk, v.TalkID, v.ChipHref = s.messageViews(t, p, msgs), talkID(a.ID), "#"+talkID(a.ID)
		p.now[a.ID] = "ask"
	}
	confirm(p.byTag[a.ID], a, v)
}

// confirm offers the Director's latest recommendation on ask a as its Confirm tile, when it recommends one of the
// ask's answers, and pre-fills the words box with the recommendation's words, or else the Guildmaster's latest
// words on the ask before it.
func confirm(msgs []Message, a Ask, v *askView) {
	rec := -1
	for i, m := range msgs {
		if m.Author == AuthorDirector && m.Kind == KindRecommendation {
			rec = i
		}
	}
	if rec < 0 || !slices.Contains(a.Options, msgs[rec].Option) {
		return
	}
	m := msgs[rec]
	meaning, _ := OptionMeaning(a.Kind, m.Option)
	v.Confirm = &confirmView{Number: m.Number, Option: m.Option, Word: OptionWord(m.Option), Tone: meaning.Tone, Form: v.Form, Decision: a.ID}
	v.Prefill = m.Words
	for i := rec - 1; i >= 0 && v.Prefill == ""; i-- {
		if msgs[i].Author != AuthorDirector && msgs[i].Kind == "say" {
			v.Prefill = msgs[i].Text
		}
	}
}

// talkView is the talk on tag as Now shows it, its earlier messages folded.
func (s *Server) talkView(t Task, p *talkPlan, tag string) talkView {
	msgs := p.byTag[tag]
	v := talkView{Tag: tag, ID: talkID(tag), Chip: chipLabel(msgs[0].Stage, tag)}
	all := s.messageViews(t, p, msgs)
	if n := len(all) - talkShown; n > 0 {
		v.Earlier, v.Shown = all[:n], all[n:]
	} else {
		v.Shown = all
	}
	return v
}

// stageTalk is the current stage's talk under the asks of an open task, with the Say box at its end for the
// Guildmaster's login; nil when it has neither. When the stage's talk is an ask's, it shows inside that ask and this
// holds only the Say box.
func (s *Server) stageTalk(t Task, p *talkPlan, base string, st pageState) *talkView {
	if t.State != "open" || t.Tag == "" {
		return nil
	}
	var say *sayView
	if st.Answer && s.answering() {
		say = &sayView{Tag: t.Tag, Action: base + "/say", Token: st.Token, Refused: st.SayRefused, Words: st.SayWords, Said: st.Said}
	}
	switch {
	case len(p.byTag[t.Tag]) > 0 && p.now[t.Tag] == "":
		p.now[t.Tag] = "stage"
		v := s.talkView(t, p, t.Tag)
		v.Say = say
		return &v
	case say == nil:
		return nil
	case p.now[t.Tag] == "":
		return &talkView{Tag: t.Tag, ID: talkID(t.Tag), Chip: chipLabel(t.TagStage, t.Tag), Say: say}
	}
	return &talkView{Tag: t.Tag, Chip: chipLabel(t.TagStage, t.Tag), Say: say}
}

// nodeTalk folds the talk on a node's tag under the node, or says where on Now it shows instead.
func (s *Server) nodeTalk(t Task, p *talkPlan, n Node, v *nodeView) {
	if n.Tag == "" {
		return
	}
	v.ID = storyID(n.Tag)
	msgs := p.byTag[n.Tag]
	if len(msgs) == 0 {
		return
	}
	switch p.now[n.Tag] {
	case "ask":
		v.TalkNote = fmt.Sprintf("Its talk is on Now, inside the ask: %d %s.", len(msgs), plural(len(msgs), "message", "messages"))
	case "stage":
		v.TalkNote = "Its talk is on Now, under the asks."
	default:
		if !p.story[n.Tag] {
			p.story[n.Tag] = true
			v.Talk = s.messageViews(t, p, msgs)
		}
	}
}

// otherTalk is the talk on every tag that has no ask, stage or story node to show under, on Now, so no message is
// left off the page.
func (s *Server) otherTalk(t Task, p *talkPlan) []talkView {
	var out []talkView
	for _, tag := range p.order {
		if p.now[tag] == "" && !p.story[tag] {
			out = append(out, s.talkView(t, p, tag))
		}
	}
	return out
}

// talkLine is the rail's Talk line, "<n> messages · last <time>", "" for a task with no talk.
func (s *Server) talkLine(t Task) string {
	n := len(t.Conversation)
	if n == 0 {
		return ""
	}
	last := t.Conversation[0].At
	for _, m := range t.Conversation {
		if m.At.After(last) {
			last = m.At
		}
	}
	line := fmt.Sprintf("%d %s", n, plural(n, "message", "messages"))
	if !last.IsZero() {
		at := s.when(last)
		if s.day(last) == s.day(t.ReadAt) {
			at = s.clock(last)
		}
		line += " · last " + at
	}
	return line
}

// closedLine is what Now says on a closed task that has talk: that it is done or cancelled, and that its talk stays,
// read-only; "" for an open task or one with no talk.
func closedLine(t Task) string {
	if len(t.Conversation) == 0 {
		return ""
	}
	switch t.State {
	case "done":
		return "This task is done and merged. Its talk stays below, read-only."
	case "cancelled":
		return "This task is cancelled. Its talk stays below, read-only."
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
