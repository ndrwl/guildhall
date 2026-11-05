package web

import (
	"cmp"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/spec"
)

// storyShown is how many of the story's newest nodes show before the rest fold under "N earlier entries".
const storyShown = 6

// docKinds is the order the Documents section lists its groups in; any other group follows, by name.
var docKinds = []string{"Review", "Design", "Spec", "Research", "Grill", "Build"}

// taskView is the task page's model, every value formatted for the templates.
type taskView struct {
	Style    template.CSS
	Scripts  []template.JS
	T        Task
	Base     string
	Crumbs   []relatedView
	Steps    []Step
	SliceOf  string
	Where    whereView
	Asks     []askView
	Open     int
	Status   statusView
	Context  []quoteView
	Grill    *grillView
	Docs     []docGroup
	DocCount int
	Story    []nodeView
	Older    []nodeView
	OlderDay string
	ReadAt   string
	Since    string
	// Gone is a refusal about a decision that is no longer an ask, such as one answered meanwhile, shown at the top
	// of Now with the words kept; nil when there is none.
	Gone *askView
	// Talks are the talk shown on Now under the asks: the current stage's first, then any whose tag has no ask or
	// story node to show under.
	Talks []talkView
	// Closed is what Now says on a closed task that has talk, "" otherwise.
	Closed string
	// Round is the current attempt's open grill round, asked as one form after the asks; nil when none is open.
	Round *roundView
}

// joinBoxes is a refused round's words, each box's under its question's number, for the Not saved box.
func joinBoxes(boxes map[int]string) string {
	var parts []string
	for n := 1; n <= MaxRoundQuestions; n++ {
		if w := strings.TrimSpace(boxes[n]); w != "" {
			parts = append(parts, "Q"+strconv.Itoa(n)+": "+w)
		}
	}
	return strings.Join(parts, "\n\n")
}

type relatedView struct {
	Label, Name, Href string
}

// whereView is the band's where-it-stands line: the sentence, then in grey the slice, pull request and checks.
type whereView struct {
	Lead, Rest, Meta string
	You              bool
}

// Sentence is the line's sentence as plain text, for the Status rail and an empty Now.
func (w whereView) Sentence() string {
	if w.Rest == "" {
		return w.Lead
	}
	return w.Lead + " " + w.Rest
}

type statusView struct {
	// Attempt is the latest attempt as the page names it, AttemptRaw its record id, shown as its title, and
	// AttemptWhen its outcome and time, such as "finished 4 Nov 14:20".
	Stands, Step, Attempt, AttemptRaw, AttemptWhen, CI, CIClass string
	PR                                                          *PullRequest
	PRHref                                                      string
	Slices                                                      []sliceView
	Waits                                                       []string
	Stage                                                       string
	// Talk is the rail's Talk line, "" for a task with no talk.
	Talk string
}

type sliceView struct {
	Index         int
	Title, State  string
	PR            *PullRequest
	PRHref        string
	Current, Done bool
}

type askView struct {
	ID, Kind, KindName, Since string
	// First is the ask's headline: its readable sentence when the Source gives one, the question's own first line
	// then leading Rest, the rest of the question; otherwise that first line. Phrase is its short form.
	First, Rest, Phrase string
	// Explained is the Director's explanation, rendered by RenderMarkdown, and ExplainedAt when it was written;
	// ExplainNote is the grey note shown instead when there is none or it was written for an earlier version.
	Explained            template.HTML
	ExplainedAt          string
	ExplainNote          string
	Subject, SubjectName string
	SubjectHref          string
	Tiles                []tileView
	// StaleRecommendation is the line saying the Director's recommendation is stale, "" when it is not.
	StaleRecommendation string
	Deferred            bool
	Findings            []Finding
	FindingsSummary     string
	// Plan says the ask is a plan approval about a spec document; Summary is that spec's Summary block rendered by
	// goldmark, empty when it has none or its file cannot be shown, and the ask then links the spec instead.
	Plan    bool
	Summary template.HTML
	// Questions is that spec's open questions as cards (newQuestionsView), shown after Summary, or on a design ask its
	// design's, or on a follow-ups ask its hallmark report's, shown in a box of their own; the same parse the question
	// text's part comes from, or the line saying there are none; nil when the document has no readable section.
	Questions *questionsView
	// Recommend says the ask offers taking every recommendation of its lettered questions as its recommended answer,
	// so on the Guildmaster's form its lettered cards are picks.
	Recommend bool
	// Hint is the words box's hint, which on an ask with a recommended answer says how to change an answer.
	Hint string
	// MockupHref links a design ask's mock-up as MockupLabel: an HTML mock-up's frame route, opened in a new tab, or
	// the reader page of one the page cannot show inline. Mockup is a Markdown mock-up rendered by goldmark, shown
	// in the ask instead of a link.
	MockupHref, MockupLabel string
	Mockup                  template.HTML
	// Digest is the full subject digest the form carries back, "" for none.
	Digest string
	// Form says the request is from the Guildmaster's login, which answers on the page as Login; Answering says the
	// page answers at all, so anyone else is told only the Guildmaster's login can.
	Form, Answering bool
	Login           string
	Action, Token   string
	// Refused is why the last answer to this ask wrote nothing, Words the words it kept, and Sent the id of the
	// note words only just sent.
	Refused, Words, Sent string
	// Chip is the ask's tag chip and ChipHref where it links; Talk is the talk on the ask, shown inside its card at
	// the anchor TalkID, <id>-talk.
	Chip, ChipHref string
	Talk           []messageView
	TalkID         string
	// Confirm is the Director's latest recommendation on the ask, offered as one tile under its talk; nil when it
	// has none. Prefill is what the words box holds when the request brings no words of its own: the
	// recommendation's words, or the Guildmaster's latest words on the ask before it.
	Confirm *confirmView
	Prefill string
}

// confirmView is the Confirm tile of an ask the Director recommends an answer to: the recommendation's number, which
// the tile posts, and the option it answers with, as its tile names it.
type confirmView struct {
	Number       int
	Option, Word string
	Tone         Tone
	Form         bool
	Decision     string
}

// sayView is the Say box at the end of the current stage's talk: the attempt it is about, the form's action and
// token, and the refusal and words, or the sent note, of the last words it took.
type sayView struct {
	Tag, Action, Token   string
	Refused, Words, Said string
}

type tileView struct {
	Option, Word, Text string
	Tone               Tone
	// Rec marks the recommended answer, which spans the row with the Recommended badge.
	Rec bool
	// Picked, on a plan approval's Approved and Changes requested, is the line CSS shows in place of Text while a
	// pick on its open questions is changed, saying the picks go with the answer.
	Picked string
}

type quoteView struct {
	Text, Cite string
}

type grillView struct {
	Count int
	Name  string
	Href  string
	HTML  template.HTML
}

type docGroup struct {
	Kind string
	Docs []docView
}

type docView struct {
	// Name is the document's store name and Label the name the task page shows, Name when it has none.
	Name, Label, Href, Day, Kind string
	Old                          bool
	Step                         string
	Recorded                     string
	SHA                          string
	RawHref, FrameHref           string
}

type nodeView struct {
	Title, Raw, Class, Word, Time, Body string
	Findings                            []Finding
	Said                                []Said
	Docs                                []docView
	Ask                                 string
	// Events are the node's raw records, each "15:04 <record>", for its Raw events fold.
	Events []string
	// ID is the node's anchor, story-<tag>; TalkNote says its talk is on Now, and Talk is its talk folded under it.
	ID, TalkNote string
	Talk         []messageView
}

// readerView is the reader page's model: the task page's, the document, and its content as goldmark HTML, plain
// text or a sandboxed frame.
type readerView struct {
	taskView
	Doc   docView
	HTML  template.HTML
	Text  string
	Frame bool
}

func (s *Server) day(t time.Time) string   { return t.In(s.o.Location).Format("2 Jan") }
func (s *Server) clock(t time.Time) string { return t.In(s.o.Location).Format("15:04") }
func (s *Server) when(t time.Time) string  { return t.In(s.o.Location).Format("2 Jan 15:04") }

// taskHref is the task page's path.
func taskHref(repo, id string) string { return "/" + pathEscape(repo) + "/" + pathEscape(id) }

// docHref is the reader page's path of a document of task repo/id.
func docHref(repo, id, name string) string { return taskHref(repo, id) + "/doc/" + pathEscape(name) }

func (s *Server) taskView(t Task, st pageState) taskView {
	v := taskView{Style: style, Scripts: scripts, T: t, Base: taskHref(t.Repository, t.ID), Steps: t.Steps, ReadAt: s.clock(t.ReadAt)}
	for _, r := range t.Related {
		v.Crumbs = append(v.Crumbs, relatedView{Label: r.Label, Name: relatedName(t.Repository, r), Href: taskHref(r.Repository, r.ID)})
	}
	if sl := t.Slices; sl != nil {
		v.SliceOf = fmt.Sprintf("slice %d of %d", sl.Current, sl.Total)
	}
	var since *time.Time
	found := false
	talk := newTalkPlan(t, st)
	for _, a := range t.Asks {
		av := s.askView(t, a)
		s.askState(t, &av, v.Base, st)
		s.askTalk(t, talk, a, &av)
		found = found || a.ID == st.Decision
		if !a.Deferred {
			v.Open++
			if a.OpenedAt != nil && (since == nil || a.OpenedAt.Before(*since)) {
				since = a.OpenedAt
			}
		}
		v.Asks = append(v.Asks, av)
	}
	if v.Round = s.openRound(t, talk, v.Base, st); v.Round != nil {
		// An open round waits on the Guildmaster as an ask does.
		v.Open++
	} else if st.RoundRefused != "" {
		// The round is gone, such as one sent meanwhile: its refusal and words show at the top of Now.
		v.Gone = &askView{KindName: "Not saved", First: "Your round was not saved", Refused: st.RoundRefused, Words: joinBoxes(st.RoundBoxes)}
	}
	if since != nil {
		v.Since = "since " + s.when(*since)
	}
	if !found && st.Refused != "" {
		v.Gone = &askView{ID: st.Decision, KindName: "Not recorded", First: "Your answer was not recorded", Refused: st.Refused, Words: st.Words}
		if st.Decision != "" {
			v.Gone.First = "#" + st.Decision + " is not waiting on you"
		}
	}
	v.Status = s.statusView(t, v.Open)
	v.Where = whereLine(t, v.Status, v.SliceOf)
	v.Status.Stands = v.Where.Sentence()
	stage := s.stageTalk(t, talk, v.Base, st)
	if stage != nil {
		v.Talks = append(v.Talks, *stage)
	}
	if st.SayRefused != "" && (stage == nil || stage.Say == nil) {
		// The Say box is gone, such as on a task closed meanwhile: its refusal and words show at the top of Now.
		v.Gone = &askView{KindName: "Not sent", First: "Your words were not sent", Refused: st.SayRefused, Words: st.SayWords}
	}
	v.Closed = closedLine(t)
	for _, q := range t.Context {
		cite := "task context"
		if !q.At.IsZero() {
			cite = s.when(q.At) + " · task context"
		}
		if q.Author != "" && q.Author != "guildmaster" {
			cite += " · recorded by " + q.Author
		}
		v.Context = append(v.Context, quoteView{Text: q.Text, Cite: cite})
	}
	v.Grill = s.grill(t)
	v.Docs, v.DocCount = s.docGroups(t)
	nodes := make([]nodeView, 0, len(t.Story))
	for i := len(t.Story) - 1; i >= 0; i-- {
		nv := s.nodeView(t, t.Story[i])
		s.nodeTalk(t, talk, t.Story[i], &nv)
		nodes = append(nodes, nv)
	}
	v.Talks = append(v.Talks, s.otherTalk(t, talk)...)
	if len(nodes) > storyShown {
		v.Story, v.Older = nodes[:storyShown], nodes[storyShown:]
		if first := t.Story[0].Start; first != nil {
			v.OlderDay = s.day(*first)
		}
	} else {
		v.Story = nodes
	}
	// The steps the workflow plans that have not started head the story as one hollow node, outside the six.
	var later []string
	for _, st := range t.Steps {
		if st.State == StateLater {
			later = append(later, st.Name)
		}
	}
	if len(later) > 0 && t.State == "open" {
		title := later[0] + " comes next"
		if n := len(later); n > 1 {
			title = strings.ToLower(strings.Join(later[:n-1], ", ") + " and " + later[n-1] + " come next")
			title = upperFirst(title)
		}
		v.Story = append([]nodeView{{Title: title, Class: string(StateLater), Word: "not started"}}, v.Story...)
	}
	return v
}

// whereLine is the band's where-it-stands line: the Source's sentence, or Clerk's phrase when it gives none, then
// the slice, pull request and checks.
func whereLine(t Task, st statusView, sliceOf string) whereView {
	w := whereView{Lead: t.Where.Lead, Rest: t.Where.Rest, You: t.Where.You}
	if w.Lead == "" && t.Stands != "" {
		w.Lead = upperFirst(t.Stands) + "."
	}
	var meta []string
	if sliceOf != "" {
		meta = append(meta, upperFirst(sliceOf))
	}
	if pr := st.PR; pr != nil {
		state := "open"
		if pr.Merged {
			state = "merged"
		}
		meta = append(meta, "PR #"+strconv.Itoa(pr.Number)+" "+state)
	}
	switch t.Status.CI {
	case CIPassed, CIFailed, CIPending:
		meta = append(meta, "CI "+t.Status.CI)
	}
	w.Meta = strings.Join(meta, " · ")
	return w
}

// relatedName names a related task by id, or <repo>/<id> when it is in another repository.
func relatedName(repo string, r Related) string {
	if r.Repository == repo {
		return r.ID
	}
	return r.Repository + "/" + r.ID
}

func (s *Server) statusView(t Task, open int) statusView {
	st := statusView{Stands: t.Stands, Waits: t.DirectorWaits, Stage: t.DirectorStage, Talk: s.talkLine(t)}
	if open > 0 {
		st.Stands = "waiting on you"
	}
	if t.Status.Step != "" {
		st.Step = t.Status.Step
		if t.Status.Round > 0 {
			st.Step += ", round " + strconv.Itoa(t.Status.Round)
		}
	}
	if a := t.Status.Attempt; a != nil {
		st.AttemptRaw = a.Step + "-" + strconv.Itoa(a.Number)
		st.Attempt = cmp.Or(a.Name, st.AttemptRaw)
		var outcome string
		switch a.Outcome {
		case "":
			outcome = "running"
			if a.At != nil {
				outcome += " since " + s.clock(*a.At)
			}
		default:
			outcome = a.Outcome
			if a.Outcome == "succeeded" {
				outcome = "finished"
			}
			if a.At != nil {
				outcome += " " + s.when(*a.At)
			}
		}
		st.AttemptWhen = outcome
	}
	if pr := t.Status.PR; pr != nil {
		st.PR, st.PRHref = pr, safeURL(pr.URL)
	}
	switch t.Status.CI {
	case CIPassed:
		st.CI, st.CIClass = "✓ passed", "ok"
	case CIFailed:
		st.CI, st.CIClass = "✗ failed", "bad"
	case CIPending:
		st.CI, st.CIClass = "▸ pending", "run"
	}
	if sl := t.Slices; sl != nil {
		for _, it := range sl.Items {
			v := sliceView{Index: it.Index, Title: it.Title, State: it.State, PR: it.PR, Current: it.Index == sl.Current, Done: it.State == "merged"}
			if it.PR != nil {
				v.PRHref = safeURL(it.PR.URL)
			}
			st.Slices = append(st.Slices, v)
		}
	}
	return st
}

// safeURL is u when it is an http or https URL, else "", so a recorded pull request URL is linked only when it is
// a web address.
func safeURL(u string) string {
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return u
	}
	return ""
}

func (s *Server) askView(t Task, a Ask) askView {
	q := strings.TrimSpace(strings.ReplaceAll(a.Question, "\r\n", "\n"))
	first, rest, _ := strings.Cut(q, "\n")
	v := askView{ID: a.ID, Kind: a.Kind, KindName: KindName(a.Kind), First: first, Rest: strings.TrimSpace(rest), Phrase: upperFirst(a.Phrase), Deferred: a.Deferred,
		Findings: a.Findings}
	if a.OpenedAt != nil {
		v.Since = "open since " + s.when(*a.OpenedAt)
	}
	if a.Subject != "" {
		v.Subject = shortDigest(a.Subject)
	}
	if a.SubjectDocument != "" {
		v.SubjectName, v.SubjectHref = a.SubjectDocument, docHref(t.Repository, t.ID, a.SubjectDocument)
		if doc := taskDocument(t, a.SubjectDocument); a.Kind == "approval" && doc != nil && doc.Kind == "Spec" {
			s.planSummary(*doc, &v)
		} else if a.Kind == "approval" && doc != nil && doc.Kind == "Design" {
			s.designQuestions(*doc, &v)
		} else if a.Kind == "follow-ups" && doc != nil {
			s.reportQuestions(*doc, &v)
		}
	}
	if doc := taskDocument(t, a.Mockup); a.Mockup != "" && doc != nil {
		s.mockup(t, *doc, &v)
	}
	// A plan or design approval whose questions carry recommendations offers taking them all as its recommended
	// answer; a follow-ups ask's pick and none are about ids, not about its questions, and keep their tiles.
	recommend := a.Kind == "approval" && v.Questions != nil && v.Questions.Lettered && slices.Contains(a.Options, "approved")
	v.Recommend = recommend
	v.Tiles = answerTiles(a.Kind, a.Options, recommend)
	// The Director's one recommendation marks its tile when current; a stale one marks none and says so.
	if r := a.Recommendation; r != nil && r.Stale {
		v.StaleRecommendation = fmt.Sprintf(staleRecommendation, OptionWord(r.Option))
	} else if r != nil && !recommend {
		markRecommended(v.Tiles, r.Option)
	}
	v.Hint = wordsHint
	if recommend {
		v.Hint = recommendedHint
	}
	if n := len(a.Findings); n > 0 {
		v.FindingsSummary = fmt.Sprintf("The open findings · %d", n)
	}
	// The headline is the readable sentence; the question's own first line is kept at the head of its rest, folded.
	if a.Headline != "" {
		v.Rest = strings.TrimSpace(v.First + "\n\n" + v.Rest)
		v.First = a.Headline
	}
	s.explanation(a, &v)
	v.Digest = a.Subject
	return v
}

// explainedNone and explainedStale are an ask's explanation block when the Director has not explained it, and when
// its explanation was written for an earlier version of the ask.
// explainedRest and explainedAsk end the note, pointing at the rest of the question when the ask folds one.
const (
	explainedNone  = "The Director has not explained this ask yet."
	explainedStale = "The Director's explanation was written for an earlier version of this ask, so it is not shown."
	explainedRest  = " The full request is under the rest of the question; ask the Director in chat if you want its view first."
	explainedAsk   = " Ask the Director in chat if you want its view first."
)

// staleRecommendation is the line an ask shows for the Director's recommendation made against an earlier version,
// naming its answer.
const staleRecommendation = "The Director recommended %s against an earlier version; it is stale."

// explanation adds the Director's explanation of ask a to v, once v's rest of the question is final: its text through
// RenderMarkdown, the only way it reaches the page, and when it was written; or the grey note saying there is none,
// or that it was written for an earlier version and is hidden.
func (s *Server) explanation(a Ask, v *askView) {
	tail := explainedAsk
	if v.Rest != "" {
		tail = explainedRest
	}
	switch e := a.Explanation; {
	case e == nil:
		v.ExplainNote = explainedNone + tail
	case e.Stale:
		v.ExplainNote = explainedStale + tail
	default:
		v.Explained = RenderMarkdown([]byte(e.Text))
		if !e.At.IsZero() {
			v.ExplainedAt = s.when(e.At)
		}
	}
}

// ReadLink says the ask's subject line links its subject document: always when it has one, except on a plan approval
// whose spec has no summary to show, where the fallback line already links the spec.
func (v askView) ReadLink() bool {
	return v.SubjectName != "" && (!v.Plan || v.Summary != "")
}

// taskDocument is the task's own document called name, nil when it has none.
func taskDocument(t Task, name string) *Document {
	for i := range t.Documents {
		if d := &t.Documents[i]; d.Repository == t.Repository && d.TaskID == t.ID && d.Name == name {
			return d
		}
	}
	return nil
}

// planSummary adds a plan approval's summary to its ask, an approval whose subject is the spec document doc (a
// design round's approval is about its design and shows none): the spec's Summary block, read verified and rendered
// by goldmark, or, when the spec has no block or its file cannot be shown, the fallback link. The question's own copy
// of the summary, or its no-summary line, is taken out of the rest of the question, so the summary shows once.
//
// After the summary come the spec's open questions as cards (newQuestionsView), from the same parse as the part the
// question text carries (spec.PlanPart); a spec with no readable section, such as one recorded before the plan check,
// shows none.
func (s *Server) planSummary(doc Document, v *askView) {
	v.Plan = true
	var summary, questions string
	if body, status, _ := s.verified(doc); status == http.StatusOK {
		if text, ok := spec.Summary(body); ok {
			summary, v.Summary = text, RenderMarkdown([]byte(text))
		}
		if q, err := spec.ReadSpecQuestions(body); err == nil {
			questions, v.Questions = q.PlanPart(), newQuestionsView(q, v.ID)
		}
	}
	v.Rest = stripSummary(v.Rest, summary, questions)
}

// designQuestions adds a design round's open questions to its ask, an approval whose subject is the design document
// doc: the design's open questions as cards, read verified as a plan approval's are, from the same parse as the part
// render.DesignQuestion writes into the question (spec.SpecPart), which is taken out of the rest of the question so
// it shows once. A design with no readable section, such as one recorded before the design check, or whose file does
// not verify, shows none and keeps its question whole.
func (s *Server) designQuestions(doc Document, v *askView) {
	body, status, _ := s.verified(doc)
	if status != http.StatusOK {
		return
	}
	q, err := spec.ReadSpecQuestions(body)
	if err != nil {
		return
	}
	v.Questions = newQuestionsView(q, v.ID)
	v.Rest = stripSummary(v.Rest, "", q.Part())
}

// reportQuestions adds a hallmark report's questions to its follow-ups ask, whose subject is the report document doc:
// the report's `## Questions` as cards (spec.ReportQuestions), read verified as a plan approval's are, from the same
// parse as the part the follow-ups question carries after its ranked list (spec.ReportPart), which is taken out of
// the rest of the question so it shows once. The cards are cut only at QuestionsMax, so they show every question the
// question text had room for. A report with no readable section, or whose file does not verify, shows none and
// keeps its question whole.
func (s *Server) reportQuestions(doc Document, v *askView) {
	body, status, _ := s.verified(doc)
	if status != http.StatusOK {
		return
	}
	q, err := spec.ReportQuestions(body)
	if err != nil {
		return
	}
	v.Questions = newQuestionsView(q, v.ID)
	v.Rest = stripReportQuestions(v.Rest)
}

// followUpsAnswer starts the answer format that ends a follow-ups question, after its questions.
const followUpsAnswer = "\n\nAnswer pick, then "

// partHeads start an ask's open-questions part in its stored text: today's heading, the heading of asks recorded
// before d153, and the line saying there are none.
var partHeads = []string{spec.QuestionsHeadingPrefix, spec.OldQuestionsInFull + "\n", spec.QuestionsNone}

// isPart says rest starts with an open-questions part, in either shape.
func isPart(rest string) bool {
	return slices.ContainsFunc(partHeads, func(h string) bool { return strings.HasPrefix(rest, h) })
}

// stripReportQuestions takes a follow-ups question's open-questions part out of rest, the question after its first
// line: from the part's heading, in either shape, after the ranked list, up to the answer format, whatever the
// stored copy holds.
func stripReportQuestions(rest string) string {
	i := -1
	for _, h := range partHeads {
		if j := strings.Index(rest, "\n\n"+h); j >= 0 && (i < 0 || j < i) {
			i = j
		}
	}
	if i < 0 {
		return rest
	}
	if end := strings.LastIndex(rest, followUpsAnswer); end > i {
		return rest[:i] + rest[end:]
	}
	return rest[:i]
}

// mockup adds a design ask's mock-up document doc to its ask: an HTML mock-up as an "Open mock-up" link to its frame
// route, so it opens sandboxed at the full window width; a Markdown mock-up, read verified, rendered inline, so its
// fenced sample output shows as preformatted text; and any other, or a Markdown file that does not verify, as a link
// to its reader page, which says why it is not shown.
func (s *Server) mockup(t Task, doc Document, v *askView) {
	href := docHref(t.Repository, t.ID, doc.Name)
	switch {
	case isHTML(doc.Path):
		v.MockupHref, v.MockupLabel = href+"/frame", "Open mock-up"
		return
	case isMarkdown(doc.Path):
		if body, status, _ := s.verified(doc); status == http.StatusOK {
			v.Mockup = RenderMarkdown(body)
			return
		}
	}
	v.MockupHref, v.MockupLabel = href, "read "+doc.Name
}

// questionParts are the parts of an approval question that follow its summary and open questions (render.Question),
// and of a design round's question, its line saying what each answer does (render.DesignQuestion), by the markers
// spec names for them, each after a blank line.
var questionParts = []string{"\n\n" + spec.InstructionsHeader + "\n", "\n\n" + spec.InputsHeader + "\n", "\n\n" + spec.AnswerLine, "\n\n" + spec.DesignAnswersLine}

// stripSummary takes an approval question's own summary part off the front of rest, the question after its first
// line: its spec.NoSummaryLine line always, its spec.SummaryHeader block, or spec.OldSummaryHeader in an ask recorded
// before d153, when the page shows summary, the block it read from the spec, in its place, and then its
// open-questions part, in either shape, when the page shows questions, the part it read from the spec. A copy that
// differs from the page's, as an older ask's does, is cut up to the next part of the question.
func stripSummary(rest, summary, questions string) string {
	if line, after, _ := strings.Cut(rest, "\n"); strings.HasPrefix(line, spec.NoSummaryLine) {
		rest = strings.TrimSpace(after)
	} else if head, ok := summaryHead(rest); summary != "" && ok {
		if after, ok := cutPart(rest, head+summary); ok {
			rest = after
		} else {
			return toNextPart(rest)
		}
	}
	if questions == "" || !isPart(rest) {
		return rest
	}
	if after, ok := cutPart(rest, questions); ok {
		return after
	}
	return toNextPart(rest)
}

// summaryHead is the Summary block's heading line rest starts with, in either shape.
func summaryHead(rest string) (string, bool) {
	for _, h := range []string{spec.SummaryHeader + "\n", spec.OldSummaryHeader + "\n"} {
		if strings.HasPrefix(rest, h) {
			return h, true
		}
	}
	return "", false
}

// cutPart takes part off the front of rest when it is a whole part, ending rest or followed by a new line.
func cutPart(rest, part string) (string, bool) {
	after, ok := strings.CutPrefix(rest, part)
	if !ok || (after != "" && after[0] != '\n') {
		return rest, false
	}
	return strings.TrimSpace(after), true
}

// toNextPart is rest from its first questionParts on, or nothing when it has none.
func toNextPart(rest string) string {
	end := len(rest)
	for _, p := range questionParts {
		if i := strings.Index(rest, p); i >= 0 && i < end {
			end = i
		}
	}
	return strings.TrimSpace(rest[end:])
}

// askState adds what the request brings to an ask: the form when it is from the Guildmaster's login, and the
// refusal, words and picks, or the sent note, of the ask st is about. On a plan approval on a page that answers, its
// open questions get their drafts and controls, with the words a refused press kept; and on the form the lettered
// cards of an ask that offers taking every recommendation are picks, a folded card showing none.
func (s *Server) askState(t Task, v *askView, base string, st pageState) {
	v.Answering, v.Form, v.Login = s.answering(), st.Answer, st.Login
	if v.Form {
		v.Action, v.Token = base+"/answer", st.Token
	}
	var boxes map[int]string
	if v.ID == st.Decision {
		v.Refused, v.Words, v.Sent = st.Refused, st.Words, st.Sent
		if st.Refused != "" {
			boxes = st.RoundBoxes
		}
	}
	if q := v.Questions; v.Plan && v.Answering && q != nil && len(q.Cards) > 0 {
		s.planDrafts(t, v, boxes)
		q.Form = v.Form
	}
	if v.Form && v.Recommend && v.Questions != nil {
		var picks map[int]string
		if v.ID == st.Decision {
			picks = st.Picks
		}
		v.Questions.withPicks(picks)
	}
}

// shortDigest is a digest as the page shows it: a head's first 7 characters, a document digest's first 12 hex.
func shortDigest(d string) string {
	if hexPart, ok := strings.CutPrefix(d, "sha256:"); ok {
		return "sha256:" + hexPart[:min(12, len(hexPart))]
	}
	return d[:min(7, len(d))]
}

// KindName is a decision kind as an ask's tag says it, such as "Loop exit" for loop-exit; an untyped decision is a
// "Question".
func KindName(kind string) string {
	if kind == "" {
		return "Question"
	}
	return OptionWord(kind)
}

// upperFirst is s with its first letter upper-cased.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// lowerFirst is s with its first letter lower-cased.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// OptionWord is an option as a tile says it: its first letter upper-cased and its - and _ made spaces, so
// changes_requested is "Changes requested".
func OptionWord(o string) string {
	w := strings.NewReplacer("_", " ", "-", " ").Replace(o)
	if w == "" {
		return w
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

// grill is the What the grill decided fold: the newest grill document's "## Decisions" list, rendered by goldmark,
// or a link to the document when it has no such section or its file cannot be shown; nil when the task has no grill
// document.
func (s *Server) grill(t Task) *grillView {
	var newest *Document
	for i := range t.Documents {
		d := &t.Documents[i]
		if d.Kind != "Grill" || d.Repository != t.Repository || d.TaskID != t.ID || !isMarkdown(d.Path) {
			continue
		}
		if newest == nil || newerDoc(*d, *newest) {
			newest = d
		}
	}
	if newest == nil {
		return nil
	}
	g := &grillView{Name: lowerFirst(cmp.Or(newest.Label, newest.Name)), Href: docHref(t.Repository, t.ID, newest.Name)}
	body, status, _ := s.verified(*newest)
	if status != http.StatusOK {
		return g
	}
	if section, n, ok := grillDecisions(body); ok && n > 0 {
		g.Count, g.HTML = n, RenderMarkdown([]byte(section))
	}
	return g
}

// newerDoc orders documents newest first: by recorded time, then by attempt, then by name.
func newerDoc(a, b Document) bool {
	return compareDocs(a, b) < 0
}

func compareDocs(a, b Document) int {
	var at, bt time.Time
	if a.RecordedAt != nil {
		at = *a.RecordedAt
	}
	if b.RecordedAt != nil {
		bt = *b.RecordedAt
	}
	return cmp.Or(bt.Compare(at), cmp.Compare(b.Attempt, a.Attempt), strings.Compare(b.Name, a.Name))
}

func (s *Server) docView(t Task, d Document) docView {
	v := docView{Name: d.Name, Label: cmp.Or(d.Label, d.Name), Href: docHref(d.Repository, d.TaskID, d.Name), Kind: d.Kind, SHA: d.SHA256}
	v.RawHref, v.FrameHref = v.Href+"/raw", v.Href+"/frame"
	if d.RecordedAt != nil {
		v.Day, v.Recorded = s.day(*d.RecordedAt), s.when(*d.RecordedAt)
	}
	switch {
	case d.Step != "" && d.Attempt > 0:
		v.Step = d.Step + " · " + strconv.Itoa(d.Attempt)
	case d.Step != "":
		v.Step = d.Step
	}
	return v
}

// docGroups groups the task's own documents by kind, in docKinds' order and then by name, each newest first with
// the older ones marked; then one group per other task whose documents are linked, named <repo>/<task>.
func (s *Server) docGroups(t Task) ([]docGroup, int) {
	own := map[string][]Document{}
	linked := map[string][]Document{}
	for _, d := range t.Documents {
		if d.Repository == t.Repository && d.TaskID == t.ID {
			k := cmp.Or(d.Kind, "Other")
			own[k] = append(own[k], d)
		} else {
			linked[relatedName(t.Repository, Related{Repository: d.Repository, ID: d.TaskID})] = append(linked[relatedName(t.Repository, Related{Repository: d.Repository, ID: d.TaskID})], d)
		}
	}
	var kinds []string
	for _, k := range docKinds {
		if len(own[k]) > 0 {
			kinds = append(kinds, k)
		}
	}
	var others []string
	for k := range own {
		if !slices.Contains(docKinds, k) {
			others = append(others, k)
		}
	}
	slices.Sort(others)
	kinds = append(kinds, others...)
	var out []docGroup
	n := 0
	for _, k := range kinds {
		docs := own[k]
		slices.SortStableFunc(docs, compareDocs)
		g := docGroup{Kind: k}
		for i, d := range docs {
			dv := s.docView(t, d)
			dv.Old = i > 0
			g.Docs = append(g.Docs, dv)
			n++
		}
		out = append(out, g)
	}
	var tasks []string
	for k := range linked {
		tasks = append(tasks, k)
	}
	slices.Sort(tasks)
	for _, k := range tasks {
		docs := linked[k]
		slices.SortStableFunc(docs, compareDocs)
		g := docGroup{Kind: k}
		for _, d := range docs {
			g.Docs = append(g.Docs, s.docView(t, d))
			n++
		}
		out = append(out, g)
	}
	return out, n
}

func (s *Server) nodeView(t Task, n Node) nodeView {
	v := nodeView{Title: n.Title, Raw: n.Raw, Class: string(n.State), Word: n.Word, Body: n.Body, Findings: n.Findings, Said: n.Said, Ask: n.Ask}
	for _, e := range n.Events {
		line := e.Text
		if e.At != nil {
			line = s.clock(*e.At) + " " + line
		}
		v.Events = append(v.Events, line)
	}
	switch {
	case n.Start != nil && n.End != nil && !n.End.Equal(*n.Start):
		if s.day(*n.Start) == s.day(*n.End) {
			v.Time = s.when(*n.Start) + "–" + s.clock(*n.End)
		} else {
			v.Time = s.when(*n.Start) + "–" + s.when(*n.End)
		}
	case n.Start != nil:
		v.Time = s.when(*n.Start)
	case n.End != nil:
		v.Time = s.when(*n.End)
	}
	for _, name := range n.Documents {
		for _, d := range t.Documents {
			if d.Name == name && d.Repository == t.Repository && d.TaskID == t.ID {
				v.Docs = append(v.Docs, s.docView(t, d))
				break
			}
		}
	}
	return v
}

// funcs are the templates' helpers.
var funcs = template.FuncMap{
	"plural": plural,
	"lower":  strings.ToLower,
}
