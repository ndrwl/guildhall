package web

import (
	"cmp"
	"fmt"
	"html/template"
	"math"
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
	T        Task
	Base     string
	Crumbs   []relatedView
	Steps    []Step
	SliceOf  string
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
}

type relatedView struct {
	Label, Name, Href string
}

type statusView struct {
	Stands, Step, Attempt, CI, CIClass string
	PR                                 *PullRequest
	PRHref                             string
	Slices                             []sliceView
	Waits                              []string
	Stage                              string
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
	First, Rest               string
	Subject, SubjectName      string
	SubjectHref               string
	Tiles                     []tileView
	Deferred                  bool
	Findings                  []Finding
	FindingsSummary           string
	// Plan says the ask is a plan approval about a spec document; Summary is that spec's Summary block rendered by
	// goldmark, empty when it has none or its file cannot be shown, and the ask then links the spec instead.
	Plan    bool
	Summary template.HTML
	// Questions is that spec's open-questions part (spec.SpecPart), shown after Summary, or on a design ask its
	// design's, or on a follow-ups ask its hallmark report's (spec.ReportPart), shown in a box of its own: the
	// questions in full, rendered by goldmark, or the line saying there are none; empty when the document has no
	// readable section.
	Questions template.HTML
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
}

type tileView struct {
	Option, Word, Text string
	Tone               Tone
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
	Name, Href, Day, Kind string
	Old                   bool
	Step                  string
	Recorded              string
	SHA                   string
	RawHref, FrameHref    string
}

type nodeView struct {
	Title, Class, Word, Time, Body string
	Findings                       []Finding
	Said                           []Said
	Docs                           []docView
	Ask                            string
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
	v := taskView{Style: style, T: t, Base: taskHref(t.Repository, t.ID), Steps: t.Steps, ReadAt: s.clock(t.ReadAt)}
	for _, r := range t.Related {
		v.Crumbs = append(v.Crumbs, relatedView{Label: r.Label, Name: relatedName(t.Repository, r), Href: taskHref(r.Repository, r.ID)})
	}
	if sl := t.Slices; sl != nil {
		v.SliceOf = fmt.Sprintf("slice %d of %d", sl.Current, sl.Total)
	}
	var since *time.Time
	found := false
	for _, a := range t.Asks {
		av := s.askView(t, a)
		s.askState(&av, v.Base, st)
		found = found || a.ID == st.Decision
		if !a.Deferred {
			v.Open++
			if a.OpenedAt != nil && (since == nil || a.OpenedAt.Before(*since)) {
				since = a.OpenedAt
			}
		}
		v.Asks = append(v.Asks, av)
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
		nodes = append(nodes, s.nodeView(t, t.Story[i]))
	}
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
		v.Story = append([]nodeView{{Title: "Later", Class: string(StateLater), Word: "not started", Body: strings.Join(later, " · ")}}, v.Story...)
	}
	return v
}

// relatedName names a related task by id, or <repo>/<id> when it is in another repository.
func relatedName(repo string, r Related) string {
	if r.Repository == repo {
		return r.ID
	}
	return r.Repository + "/" + r.ID
}

func (s *Server) statusView(t Task, open int) statusView {
	st := statusView{Stands: t.Stands, Waits: t.DirectorWaits, Stage: t.DirectorStage}
	if open > 0 {
		st.Stands = "waiting on you"
	}
	if t.Status.Step != "" {
		st.Step = t.Status.Step
		if t.Status.Round > 0 {
			st.Step += " · round " + strconv.Itoa(t.Status.Round)
		}
	}
	if a := t.Status.Attempt; a != nil {
		st.Attempt = a.Step + "-" + strconv.Itoa(a.Number) + " · "
		switch a.Outcome {
		case "":
			st.Attempt += "running"
			if a.At != nil {
				st.Attempt += " since " + s.clock(*a.At)
			}
		default:
			st.Attempt += a.Outcome
			if a.At != nil {
				st.Attempt += " " + s.when(*a.At)
			}
		}
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
	v := askView{ID: a.ID, Kind: a.Kind, KindName: KindName(a.Kind), First: first, Rest: strings.TrimSpace(rest), Deferred: a.Deferred, Findings: a.Findings}
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
	for _, o := range a.Options {
		m, _ := OptionMeaning(a.Kind, o)
		v.Tiles = append(v.Tiles, tileView{Option: o, Word: OptionWord(o), Text: m.Text, Tone: m.Tone})
	}
	if n := len(a.Findings); n > 0 {
		v.FindingsSummary = fmt.Sprintf("The open findings · %d", n)
	}
	v.Digest = a.Subject
	return v
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
// After the summary come the spec's open questions in full (spec.SpecPart), the same part the question text carries,
// rendered the same way; a spec with no readable section, such as one recorded before the plan check, shows none.
func (s *Server) planSummary(doc Document, v *askView) {
	v.Plan = true
	var summary, questions string
	if body, status, _ := s.verified(doc); status == http.StatusOK {
		if text, ok := spec.Summary(body); ok {
			summary, v.Summary = text, RenderMarkdown([]byte(text))
		}
		if part, ok := spec.SpecPart(body); ok {
			questions, v.Questions = part, questionsHTML(part)
		}
	}
	v.Rest = stripSummary(v.Rest, summary, questions)
}

// designQuestions adds a design round's open questions to its ask, an approval whose subject is the design document
// doc: the design's open questions in full (spec.SpecPart), read verified and rendered as a plan approval's are, the
// same part render.DesignQuestion writes into the question, which is taken out of the rest of the question so it
// shows once. A design with no readable section, such as one recorded before the design check, or whose file does
// not verify, shows none and keeps its question whole.
func (s *Server) designQuestions(doc Document, v *askView) {
	body, status, _ := s.verified(doc)
	if status != http.StatusOK {
		return
	}
	part, ok := spec.SpecPart(body)
	if !ok {
		return
	}
	v.Questions = questionsHTML(part)
	v.Rest = stripSummary(v.Rest, "", part)
}

// reportQuestions adds a hallmark report's questions to its follow-ups ask, whose subject is the report document doc:
// the report's `## Questions` in full (spec.ReportPart), read verified and rendered as a plan approval's are, the
// part the follow-ups question carries after its ranked list, which is taken out of the rest of the question so it
// shows once. The box is cut only at QuestionsMax, so it shows every question the question text had room for. A
// report with no readable section, or whose file does not verify, shows none and keeps its question whole.
func (s *Server) reportQuestions(doc Document, v *askView) {
	body, status, _ := s.verified(doc)
	if status != http.StatusOK {
		return
	}
	part, ok := spec.ReportPart(body, math.MaxInt)
	if !ok {
		return
	}
	v.Questions = questionsHTML(part)
	v.Rest = stripReportQuestions(v.Rest)
}

// followUpsAnswer starts the answer format that ends a follow-ups question, after its questions.
const followUpsAnswer = "\n\nAnswer pick, then "

// stripReportQuestions takes a follow-ups question's open-questions part out of rest, the question after its first
// line: from the part's heading, after the ranked list, up to the answer format, whatever the stored copy holds.
func stripReportQuestions(rest string) string {
	i := strings.Index(rest, "\n\n"+spec.QuestionsInFull+"\n")
	if i < 0 {
		i = strings.Index(rest, "\n\n"+spec.QuestionsNone)
	}
	if i < 0 {
		return rest
	}
	if end := strings.LastIndex(rest, followUpsAnswer); end > i {
		return rest[:i] + rest[end:]
	}
	return rest[:i]
}

// questionsHTML renders an open-questions part (spec.Part) for an ask's box: "Open questions in full" and the
// questions rendered by goldmark, each question's lines as written, or the line saying there are none.
func questionsHTML(part string) template.HTML {
	if q, ok := strings.CutPrefix(part, spec.QuestionsInFull+"\n"); ok {
		// Each question's lines show as written: its Options: and Recommended: lines on lines of their own.
		return template.HTML(`<p class="ph">Open questions in full</p><div class="qs">`) + RenderMarkdown([]byte(q)) + template.HTML(`</div>`)
	}
	return template.HTML(`<p class="ph">` + template.HTMLEscapeString(part) + `</p>`)
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
// line: its spec.NoSummaryLine line always, its spec.SummaryHeader block when the page shows summary, the block it
// read from the spec, in its place, and then its open-questions part when the page shows questions, the part it read
// from the spec. A copy that differs from the page's is cut up to the next part of the question.
func stripSummary(rest, summary, questions string) string {
	header := spec.SummaryHeader + "\n"
	if line, after, _ := strings.Cut(rest, "\n"); strings.HasPrefix(line, spec.NoSummaryLine) {
		rest = strings.TrimSpace(after)
	} else if summary != "" && strings.HasPrefix(rest, header) {
		if after, ok := cutPart(rest, header+summary); ok {
			rest = after
		} else {
			return toNextPart(rest)
		}
	}
	if questions == "" || (!strings.HasPrefix(rest, spec.QuestionsInFull+"\n") && !strings.HasPrefix(rest, spec.QuestionsNone)) {
		return rest
	}
	if after, ok := cutPart(rest, questions); ok {
		return after
	}
	return toNextPart(rest)
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
// refusal and words, or the sent note, of the ask st is about.
func (s *Server) askState(v *askView, base string, st pageState) {
	v.Answering, v.Form, v.Login = s.answering(), st.Answer, st.Login
	if v.Form {
		v.Action, v.Token = base+"/answer", st.Token
	}
	if v.ID == st.Decision {
		v.Refused, v.Words, v.Sent = st.Refused, st.Words, st.Sent
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
	g := &grillView{Name: newest.Name, Href: docHref(t.Repository, t.ID, newest.Name)}
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
	v := docView{Name: d.Name, Href: docHref(d.Repository, d.TaskID, d.Name), Kind: d.Kind, SHA: d.SHA256}
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
	v := nodeView{Title: n.Title, Class: string(n.State), Word: n.Word, Body: n.Body, Findings: n.Findings, Said: n.Said, Ask: n.Ask}
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
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
}
