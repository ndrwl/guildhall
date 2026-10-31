package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// webLockWait bounds how long a task page waits for a writer's store lock before it answers "store busy, reload".
var webLockWait = 5 * time.Second

// webSource is the task pages' Source: it fills web.Task from Clerk's readers (clerkRead, stands, the status feed's
// slices and decisions, timeline and files), so the page and Clerk agree and Clerk's own output is unchanged. Each
// call opens the store, reads, and closes it before the page renders.
type webSource struct {
	a *App
	c clerkPaths
}

// Task reads repo/id's page model.
func (w webSource) Task(repo, id string) (web.Task, error) {
	busy := func(err error) error {
		if errors.Is(err, store.ErrLocked) {
			return web.ErrBusy
		}
		return err
	}
	d, err := w.a.clerkReadWait(context.Background(), w.c, webLockWait)
	if err != nil {
		return web.Task{}, busy(err)
	}
	ct, ok := d.byKey[repo+"/"+id]
	if !ok {
		return web.Task{}, web.ErrNotFound
	}
	h, err := withRecords(storeRecords{path: w.c.store, wait: webLockWait}, true, nil, func(s *store.Store) (store.TaskHistory, error) {
		return s.TaskHistory(repo, id)
	})
	if errors.Is(err, store.ErrNotFound) {
		return web.Task{}, web.ErrNotFound
	}
	if err != nil {
		return web.Task{}, busy(err)
	}
	var def *workflow.Definition
	if d.resolver != nil {
		def, _ = d.resolver(repo)(ct.view.Workflow)
	}
	b := webBuilder{d: d, ct: ct, h: h, tl: timeline(h), fl: files(h), def: def, state: w.c.state}
	return b.task(), nil
}

// webBuilder builds one task's page model from what webSource read.
type webBuilder struct {
	d     clerkData
	ct    clerkTask
	h     store.TaskHistory
	tl    taskTimeline
	fl    taskFiles
	def   *workflow.Definition
	state string
}

func (b webBuilder) task() web.Task {
	v := b.ct.view
	t := web.Task{Repository: v.Repository, ID: v.ID, Title: v.Title, State: string(v.State), Stands: b.d.stands(b.ct), ReadAt: b.d.now}
	t.Related = b.related()
	t.Documents = b.documents()
	t.Asks = b.asks(t.Documents)
	for _, it := range b.d.status.NeedsMe {
		if it.Repository != v.Repository || it.TaskID != v.ID || it.Kind == decisionKind || it.Kind == installKind {
			continue
		}
		t.DirectorWaits = append(t.DirectorWaits, oneLine(it.Reason))
	}
	if n, ok := b.next(); ok && v.State == store.TaskOpen && interp.Wait(n.Wait) == interp.WaitDirectorStage {
		base, _ := workflow.ParseSliceStep(n.Step)
		t.DirectorStage = base
	}
	for _, c := range b.tl.Context {
		t.Context = append(t.Context, web.Quote{Text: c.Text, Author: c.Author, At: c.AddedAt})
	}
	t.Slices = b.slices()
	t.Status = b.status()
	t.Steps = b.steps()
	t.Story = b.story(t.Documents)
	return t
}

// next is the task's next.json, when it has one that reads.
func (b webBuilder) next() (clerkNext, bool) {
	var n clerkNext
	if b.ct.status.Next == nil || json.Unmarshal(b.ct.status.Next, &n) != nil {
		return n, false
	}
	return n, true
}

// current is the task's current slice, 1 for a one-slice task.
func (b webBuilder) current() int {
	if sl := b.ct.status.Slices; sl != nil && sl.Current > 0 {
		return sl.Current
	}
	return 1
}

// related are the tasks this task names: its dependencies, the task it fixes and the tasks whose documents are
// linked to it, each once.
func (b webBuilder) related() []web.Related {
	v := b.ct.view
	var out []web.Related
	add := func(label string, r store.TaskRef) {
		for _, o := range out {
			if o.Repository == r.Repository && o.ID == r.ID {
				return
			}
		}
		out = append(out, web.Related{Label: label, Repository: r.Repository, ID: r.ID})
	}
	if f := v.Fixes; f != nil {
		add("fixes", *f)
	}
	for _, dep := range v.DependsOn {
		add("depends on", dep)
	}
	for _, l := range b.activeLinks() {
		add("linked from", store.TaskRef{Repository: l.Source.Repository, ID: l.Source.TaskID})
	}
	return out
}

// activeLinks are the task's links that stand: each add not followed by a remove of the same source document.
func (b webBuilder) activeLinks() []timedLink {
	var out []timedLink
	for _, l := range b.tl.Links {
		key := func(x timedLink) string { return x.Source.Repository + "/" + x.Source.TaskID + "/" + x.Source.Name }
		out = slices.DeleteFunc(out, func(x timedLink) bool { return key(x) == key(l) })
		if l.Op == store.TaskLinkAdd {
			out = append(out, l)
		}
	}
	return out
}

// webDocKind is the group the page lists a document of step type kind under.
var webDocKind = map[string]string{
	"grill": "Grill", "research": "Research", "plan": "Spec", "guildmaster-approval": "Spec", "build": "Build",
	"review": "Review", "document-review": "Review", "publish": "Delivery", "validate": "Delivery", "deliver": "Delivery",
	"handoff-wait": "Handoff",
}

// docPlace reads a document name as <step>-<round>-<part>, the engine's naming (interp's documentName): the step
// with its slice qualifier removed, its slice and its round, zero when the name has no round.
func docPlace(name string) (step string, slice, round int) {
	segs := strings.Split(name, "-")
	for i := 1; i < len(segs); i++ {
		r := strings.TrimPrefix(segs[i], "x")
		if n, err := strconv.Atoi(r); err == nil && n > 0 && strconv.Itoa(n) == r && segs[i-1] != "" {
			base, k := workflow.ParseSliceStep(strings.Join(segs[:i], "-"))
			return base, k, n
		}
	}
	base, k := workflow.ParseSliceStep(name)
	return base, k, 0
}

// stepType is the type of step id in the bound workflow, "" when the workflow has no such step.
func (b webBuilder) stepType(id string) string {
	if b.def == nil {
		return ""
	}
	for _, s := range b.def.Steps {
		if s.ID == id {
			return s.Type
		}
	}
	return ""
}

func (b webBuilder) docKind(name string) (kind, step string, round int) {
	if strings.HasPrefix(name, "send-back-") {
		return "Feedback", "", 0
	}
	step, _, round = docPlace(name)
	if k, ok := webDocKind[b.stepType(step)]; ok {
		return k, step, round
	}
	if step == "design" || step == "designer" {
		return "Design", step, round
	}
	return "Other", step, round
}

// documents are the task's recorded documents but its attempts' input bindings, then the documents of other tasks
// linked to it, each with the time it was recorded or linked.
func (b webBuilder) documents() []web.Document {
	v := b.ct.view
	var out []web.Document
	for _, d := range b.fl.Documents {
		if strings.HasSuffix(d.Name, "-inputs") {
			continue
		}
		kind, step, round := b.docKind(d.Name)
		out = append(out, web.Document{Repository: v.Repository, TaskID: v.ID, Name: d.Name, Path: d.Path, SHA256: d.SHA256, Kind: kind, Step: step, Attempt: round, RecordedAt: d.RecordedAt})
	}
	for _, l := range b.activeLinks() {
		s := l.Source
		at := l.AddedAt
		_, step, round := b.docKind(s.Name)
		out = append(out, web.Document{Repository: s.Repository, TaskID: s.TaskID, Name: s.Name, Path: s.Path, SHA256: s.SHA256, Kind: "Linked", Step: step, Attempt: round, RecordedAt: &at})
	}
	return out
}

// asks are the task's open decisions from the status feed, open ones first and deferred ones after, each with the
// document its subject digest names and, for a loop exit, its step's newest review findings.
func (b webBuilder) asks(docs []web.Document) []web.Ask {
	v := b.ct.view
	var open, deferred []web.Ask
	for _, dec := range b.d.status.Decisions {
		if dec.Repository != v.Repository || dec.TaskID != v.ID {
			continue
		}
		a := web.Ask{ID: dec.ID, Kind: string(dec.Kind), Question: dec.Question, Options: dec.Options, OpenedAt: dec.OpenedAt, Deferred: dec.Deferred}
		if s := dec.Subject; s != nil {
			a.Subject = s.Digest
			a.SubjectDocument = subjectDocument(docs, v, *s)
		}
		if dec.Kind == store.DecisionLoopExit {
			a.Findings = b.openFindings(dec.ID)
		}
		if dec.Deferred {
			deferred = append(deferred, a)
		} else {
			open = append(open, a)
		}
	}
	return append(open, deferred...)
}

// subjectDocument names the task's own document whose sha256 is a document subject's digest, "" when none is.
func subjectDocument(docs []web.Document, v taskView, s store.Subject) string {
	hexPart, ok := strings.CutPrefix(s.Digest, "sha256:")
	if s.Kind != store.SubjectDocument || !ok {
		return ""
	}
	for _, d := range docs {
		if d.Repository == v.Repository && d.TaskID == v.ID && d.SHA256 == hexPart {
			return d.Name
		}
	}
	return ""
}

// openFindings are the findings of the newest review outcome of the step a loop-exit decision id names.
func (b webBuilder) openFindings(id string) []web.Finding {
	step, slice, _ := docPlace(id)
	var newest *timedFindings
	for i := range b.tl.Findings {
		f := &b.tl.Findings[i]
		if s, k, _ := docPlace(f.Document); s == step && k == slice {
			newest = f
		}
	}
	if newest == nil {
		return nil
	}
	var out []web.Finding
	for _, f := range newest.Findings {
		out = append(out, web.Finding{ID: f.ID, Rule: f.Rule, Summary: oneLine(f.Summary)})
	}
	return out
}

func (b webBuilder) slices() *web.Slices {
	sl := b.ct.status.Slices
	if sl == nil {
		return nil
	}
	out := &web.Slices{Current: sl.Current, Total: sl.Total}
	for _, it := range sl.Items {
		s := web.Slice{Index: it.Index, Title: oneLine(it.Title), State: it.State}
		if pr := it.PullRequest; pr != nil {
			s.PR = &web.PullRequest{Number: pr.Number, URL: pr.URL, Merged: it.State == interp.SliceMerged}
		}
		out.Items = append(out.Items, s)
	}
	return out
}

func (b webBuilder) status() web.Status {
	var st web.Status
	if n, ok := b.next(); ok && n.Step != "" {
		base, k := workflow.ParseSliceStep(n.Step)
		st.Step = webStepName(base)
		for _, at := range b.tl.Attempts {
			if s, sk := workflow.ParseSliceStep(at.Step); s == base && sk == k {
				st.Round = max(st.Round, at.Number)
			}
		}
	}
	if len(b.tl.Attempts) > 0 {
		at := b.tl.Attempts[len(b.tl.Attempts)-1]
		a := &web.Attempt{Step: at.Step, Number: at.Number, Outcome: string(at.Outcome), At: cmp.Or(at.FinishedAt, at.StartedAt)}
		st.Attempt = a
	}
	if pr := b.ct.pr; pr != nil {
		st.PR = &web.PullRequest{Number: pr.Number, URL: pr.URL, Merged: b.ct.merging}
		switch b.ct.checksState() {
		case "CI passed":
			st.CI = web.CIPassed
		case "CI failed":
			st.CI = web.CIFailed
		case "CI pending":
			st.CI = web.CIPending
		}
	}
	return st
}

// webStepName is a step id as the page names it: its first letter upper-cased, - made a space.
func webStepName(id string) string {
	return web.OptionWord(id)
}

// decisionStep is the step and slice a decision id <step>-<round> or <step>-x<k> names, and ok false for an id of
// another form, such as follow-ups.
func decisionStep(id string) (string, int, bool) {
	i := strings.LastIndex(id, "-")
	if i <= 0 {
		return "", 0, false
	}
	r := strings.TrimPrefix(id[i+1:], "x")
	if n, err := strconv.Atoi(r); err != nil || n < 1 {
		return "", 0, false
	}
	base, k := workflow.ParseSliceStep(id[:i])
	return base, k, true
}

// webPositive and webNegative are answers that end a step well and answers that stop it; any other answer to a
// decision with options, such as changes_requested, continue or hold, sends the work back or round again.
var (
	webPositive = []string{"approved", "accept", "merge", "approve", "pick", "received"}
	webNegative = []string{"rejected", "reject", "decline"}
)

// steps is the stepper: each step of the bound workflow in order, with the state of its records in the current
// slice (a step outside the slice range, of slice 1), or later when it has none.
func (b webBuilder) steps() []web.Step {
	if b.def == nil {
		return nil
	}
	cur := b.current()
	var out []web.Step
	for _, s := range b.def.Steps {
		slice := 1
		if b.def.InSlices(s.ID) {
			slice = cur
		}
		st := web.Step{Name: webStepName(s.ID), State: web.StateLater, Note: "later"}
		var last *timedAttempt
		rounds := 0
		for i := range b.tl.Attempts {
			at := &b.tl.Attempts[i]
			if base, k := workflow.ParseSliceStep(at.Step); base == s.ID && k == slice {
				last, rounds = at, rounds+1
			}
		}
		if last != nil {
			switch last.Outcome {
			case "":
				st.State, st.Note = web.StateRunning, "running"
			case store.AttemptSucceeded:
				st.State, st.Note = web.StateDone, webCount(rounds, "round")
			default:
				st.State, st.Note = web.StateFailed, string(last.Outcome)
			}
		}
		for _, dec := range b.tl.Decisions {
			base, k, ok := decisionStep(dec.ID)
			if !ok || base != s.ID || k != slice {
				continue
			}
			switch {
			case dec.Open():
				st.State, st.Note = web.StateYou, "waiting on you"
			case dec.Void:
			case slices.Contains(webPositive, dec.Choice):
				st.State, st.Note = web.StateDone, dec.ID+" "+dec.Choice
			case dec.Choice != "" && slices.Contains(webNegative, dec.Choice):
				st.State, st.Note = web.StateFailed, dec.ID+" "+dec.Choice
			case dec.Choice != "":
				st.State, st.Note = web.StateBack, dec.ID+" "+strings.ReplaceAll(dec.Choice, "_", " ")
			case dec.Answered:
				st.State, st.Note = web.StateDone, dec.ID+" answered"
			}
		}
		if st.State == web.StateYou {
			st.Note = "waiting on you"
		}
		out = append(out, st)
	}
	return out
}

// webCount says n of a noun: "1 round", "3 rounds".
func webCount(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// webFeedbackBytes bounds the send-back feedback quoted on a story node.
const webFeedbackBytes = 2048

// story is the task's history, oldest first: the task added, each attempt, each decision, each slice merge and a
// cancel.
func (b webBuilder) story(docs []web.Document) []web.Node {
	v := b.ct.view
	var nodes []web.Node
	if len(b.h.Receipts) > 0 {
		at := b.h.Receipts[0].CommittedAt
		n := web.Node{Title: "Task added", State: web.StateDone, Word: "finished", Start: &at}
		if len(b.tl.Context) > 0 {
			n.Body = "with your words, shown under “The task”"
		}
		nodes = append(nodes, n)
	}
	own := func(prefix string) []string {
		var out []string
		for _, d := range docs {
			if d.Repository == v.Repository && d.TaskID == v.ID && strings.HasPrefix(d.Name, prefix) {
				out = append(out, d.Name)
			}
		}
		return out
	}
	for i, at := range b.tl.Attempts {
		base, k := workflow.ParseSliceStep(at.Step)
		n := web.Node{Title: webStepName(base) + " · round " + strconv.Itoa(at.Number), Start: at.StartedAt, End: at.FinishedAt}
		if k > 1 {
			n.Title += " · slice " + strconv.Itoa(k)
		}
		switch {
		case at.IntakeRejection != "":
			n.State, n.Word, n.Body = web.StateFailed, "refused at intake", oneLine(at.IntakeRejection)
		case at.Outcome == "":
			n.State, n.Word = web.StateRunning, "running"
		case at.Outcome == store.AttemptSucceeded:
			n.State, n.Word = web.StateDone, "finished"
		default:
			n.State, n.Word = web.StateFailed, string(at.Outcome)
		}
		if n.Body == "" {
			key := artisan.AttemptKey{Repository: v.Repository, TaskID: v.ID, Step: at.Step, Attempt: at.Number}
			if s := clerkSummary(filepath.Join(key.Dir(b.state), artisan.ResultFile)); s != "" {
				n.Body = webCut(oneLine(s), 400)
			}
		}
		prefix := webDecisionID(at.Step, at.Number) + "-"
		n.Documents = own(prefix)
		if f := b.findingsOf(prefix); f != nil && len(f.Findings) > 0 {
			n.Findings = b.fixed(i, at.Step, f)
			if back := b.sentBackTo(i); back != "" && at.Outcome == store.AttemptSucceeded {
				n.State, n.Word = web.StateBack, "changes requested · sent back to "+strings.ToLower(webStepName(back))
			}
		}
		if fb := b.feedback(at.Step, at.Number); fb != "" {
			n.Said = append(n.Said, web.Said{Text: fb, Where: "the Director's feedback, sent back to " + at.Step, Back: true})
			n.Documents = append(n.Documents, "send-back-"+at.Step+"-"+strconv.Itoa(at.Number))
		}
		nodes = append(nodes, n)
	}
	for _, dec := range b.tl.Decisions {
		n := web.Node{Title: web.KindName(string(dec.Kind)) + " · " + dec.ID, Start: dec.OpenedAt, End: dec.AnsweredAt}
		switch {
		case dec.Void:
			n.State, n.Word = web.StateVoid, "void"
			if dec.VoidReason != "" {
				n.Body = "void: " + oneLine(dec.VoidReason)
			}
		case dec.Open() && b.deferred(dec.ID):
			n.State, n.Word, n.Ask = web.StateYou, "deferred · still yours", dec.ID
		case dec.Open():
			n.State, n.Word, n.Ask = web.StateYou, "waiting on you", dec.ID
		case slices.Contains(webPositive, dec.Choice):
			n.State, n.Word = web.StateDone, web.OptionWord(dec.Choice)
		case dec.Choice != "" && slices.Contains(webNegative, dec.Choice):
			n.State, n.Word = web.StateFailed, web.OptionWord(dec.Choice)
		case dec.Choice != "":
			n.State, n.Word = web.StateBack, web.OptionWord(dec.Choice)
		default:
			n.State, n.Word = web.StateDone, "answered"
		}
		if dec.Answered {
			words := dec.Note
			if len(dec.Options) == 0 {
				words = strings.TrimSpace(dec.Answer)
			}
			if words != "" {
				n.Said = append(n.Said, web.Said{Text: words, Where: webAnswerWhere(dec.Decision), Back: n.State == web.StateBack})
			} else if dec.Choice != "" {
				n.Body = "answered " + dec.Choice + " · " + webAnswerWhere(dec.Decision)
			}
		}
		if s := dec.Subject; s != nil {
			if name := subjectDocument(docs, v, *s); name != "" {
				n.Documents = append(n.Documents, name)
			}
		}
		for _, name := range own(dec.ID + "-") {
			if !slices.Contains(n.Documents, name) {
				n.Documents = append(n.Documents, name)
			}
		}
		nodes = append(nodes, n)
	}
	for _, d := range b.fl.Documents {
		if !interp.IsMergeRecord(d.Name) {
			continue
		}
		title := "Merged"
		if b.ct.status.Slices != nil {
			title = "Slice " + strconv.Itoa(interp.DocumentSlice(d.Name)) + " merged"
		}
		nodes = append(nodes, web.Node{Title: title, State: web.StateDone, Word: "merged", Start: d.RecordedAt, Documents: []string{d.Name}})
	}
	if v.State == store.TaskCancelled {
		at := v.CancelledAt
		n := web.Node{Title: "Cancelled", State: web.StateFailed, Word: "cancelled", Body: oneLine(v.CancelReason)}
		if !at.IsZero() {
			n.Start = &at
		}
		nodes = append(nodes, n)
	}
	slices.SortStableFunc(nodes, func(x, y web.Node) int { return webNodeTime(x).Compare(webNodeTime(y)) })
	return nodes
}

// webAnswerWhere says where a decision was answered: on the task page by its login, or otherwise in chat, through
// the Director, or with decide answer, which record no place.
func webAnswerWhere(d store.Decision) string {
	if d.AnsweredVia == store.AnsweredViaPage {
		return "answered on the task page by " + d.AnsweredBy
	}
	return "answered via the Director or decide answer"
}

// webNodeTime is when a node began, the zero time when it is not known, so such nodes sort first.
func webNodeTime(n web.Node) time.Time {
	switch {
	case n.Start != nil:
		return *n.Start
	case n.End != nil:
		return *n.End
	}
	return time.Time{}
}

// deferred reports whether the status feed holds decision id of the task as deferred.
func (b webBuilder) deferred(id string) bool {
	for _, dec := range b.d.status.Decisions {
		if dec.Repository == b.ct.view.Repository && dec.TaskID == b.ct.view.ID && dec.ID == id {
			return dec.Deferred
		}
	}
	return false
}

// findingsOf is the review outcome among the task's findings whose document name has prefix, nil when none has.
func (b webBuilder) findingsOf(prefix string) *timedFindings {
	for i := range b.tl.Findings {
		if strings.HasPrefix(b.tl.Findings[i].Document, prefix) {
			return &b.tl.Findings[i]
		}
	}
	return nil
}

// fixed are f's findings, each marked fixed when a later review outcome of the same step resolved it.
func (b webBuilder) fixed(i int, step string, f *timedFindings) []web.Finding {
	resolved := map[string]bool{}
	for _, later := range b.tl.Attempts[i+1:] {
		if later.Step != step {
			continue
		}
		if lf := b.findingsOf(webDecisionID(later.Step, later.Number) + "-"); lf != nil {
			for _, d := range lf.Dispositions {
				if d.State == workflow.Resolved {
					resolved[d.Finding] = true
				}
			}
		}
	}
	var out []web.Finding
	for _, x := range f.Findings {
		out = append(out, web.Finding{ID: x.ID, Rule: x.Rule, Summary: oneLine(x.Summary), Fixed: resolved[x.ID]})
	}
	return out
}

// sentBackTo is the step an attempt's work went back to: an earlier step of the bound workflow, in the same slice,
// with an attempt that started after attempt i; "" when the work went on.
func (b webBuilder) sentBackTo(i int) string {
	if b.def == nil {
		return ""
	}
	at := b.tl.Attempts[i]
	base, k := workflow.ParseSliceStep(at.Step)
	index := func(id string) int {
		return slices.IndexFunc(b.def.Steps, func(s workflow.Step) bool { return s.ID == id })
	}
	for _, later := range b.tl.Attempts[i+1:] {
		lb, lk := workflow.ParseSliceStep(later.Step)
		if lk == k && index(lb) >= 0 && index(lb) < index(base) {
			return lb
		}
	}
	return ""
}

// feedback is the Director's send-back feedback that asked for another attempt after attempt n of step, read from
// its document send-back-<step>-<n> when the file matches its record, cut to webFeedbackBytes; "" when there is
// none.
func (b webBuilder) feedback(step string, n int) string {
	name := "send-back-" + step + "-" + strconv.Itoa(n)
	for _, d := range b.fl.Documents {
		if d.Name != name {
			continue
		}
		data, err := web.ReadVerified(filepath.Join(b.state, "documents"), d.Path, d.SHA256)
		if err != nil || !utf8.Valid(data) {
			return ""
		}
		return webCut(strings.TrimSpace(string(data)), webFeedbackBytes)
	}
	return ""
}

// webDecisionID is the engine's prefix of every record of attempt or round n of step, <step>-<n>.
func webDecisionID(step string, n int) string {
	return step + "-" + strconv.Itoa(n)
}

// webCut cuts s to at most n bytes on a rune boundary, ending a cut with ….
func webCut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}
