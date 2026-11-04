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

// Tasks reads the task list's model: every task open or closed in the last 7 days, as Clerk's /guild all lists them,
// each with its where-it-stands sentence, its group and since when; the open ones in Clerk's order, by priority
// then id, and the finished ones newest first.
func (w webSource) Tasks() (web.TaskList, error) {
	d, err := w.a.clerkReadWait(context.Background(), w.c, webLockWait)
	if err != nil {
		return web.TaskList{}, webBusy(err)
	}
	var shown []clerkTask
	for _, v := range d.tasks {
		ct := d.byKey[v.Repository+"/"+v.ID]
		if v.State != store.TaskOpen && (ct.closedAt.IsZero() || d.now.Sub(ct.closedAt) > clerkWindow) {
			continue
		}
		shown = append(shown, ct)
	}
	slices.SortStableFunc(shown, func(a, b clerkTask) int {
		ao, bo := a.view.State == store.TaskOpen, b.view.State == store.TaskOpen
		switch {
		case ao && !bo:
			return -1
		case !ao && bo:
			return 1
		case !ao:
			return cmp.Or(b.closedAt.Compare(a.closedAt), strings.Compare(a.view.ID, b.view.ID))
		}
		return cmp.Or(cmp.Compare(a.view.Priority, b.view.Priority), strings.Compare(a.view.ID, b.view.ID), strings.Compare(a.view.Repository, b.view.Repository))
	})
	out := web.TaskList{ReadAt: d.now}
	for _, ct := range shown {
		b := webBuilder{d: d, ct: ct, state: w.c.state}
		if d.resolver != nil {
			b.def, _ = d.resolver(ct.view.Repository)(ct.view.Workflow)
		}
		out.Tasks = append(out.Tasks, b.listed())
	}
	return out, nil
}

// listed is the task's row of the task list. It reads no history: its asks are the status feed's, and its words
// those of the where-it-stands line.
func (b webBuilder) listed() web.Listed {
	v := b.ct.view
	l := web.Listed{Repository: v.Repository, ID: v.ID, Title: oneLine(v.Title), Stands: b.d.stands(b.ct)}
	if v.State != store.TaskOpen {
		at := b.ct.closedAt
		l.Group, l.Where, l.Since = web.GroupFinished, b.where(nil, "", nil), &at
		return l
	}
	asks := b.asks(nil)
	l.Where = b.where(asks, b.directorStage(), b.directorWaits())
	l.Group = b.group(l.Where)
	for _, a := range asks {
		if l.Group == web.GroupYou && !a.Deferred && a.OpenedAt != nil && (l.Since == nil || a.OpenedAt.Before(*l.Since)) {
			l.Since = a.OpenedAt
		}
	}
	if at := b.ct.changedAt; l.Since == nil && !at.IsZero() {
		l.Since = &at
	}
	return l
}

// group is the task list's group of an open task: waiting for the Guildmaster when its where-it-stands sentence
// says so; moving while an attempt starts or runs, it waits for a slot, CI or the merge queue; otherwise waiting on
// something else, such as its dependencies, the Director, a pause or its first start.
func (b webBuilder) group(w web.Where) web.Group {
	if w.You {
		return web.GroupYou
	}
	n, ok := b.next()
	if !ok {
		return web.GroupOther
	}
	switch interp.Wait(n.Wait) {
	case interp.WaitAttempt, interp.WaitQueued, interp.WaitChecks, interp.WaitMergeQueue:
		return web.GroupMoving
	}
	return web.GroupOther
}

// directorWaits are the task's needs_me reasons that wait on the Director: all but its decisions and the install
// item.
func (b webBuilder) directorWaits() []string {
	v := b.ct.view
	var out []string
	for _, it := range b.d.status.NeedsMe {
		if it.Repository != v.Repository || it.TaskID != v.ID || it.Kind == decisionKind || it.Kind == installKind {
			continue
		}
		out = append(out, oneLine(it.Reason))
	}
	return out
}

// directorStage is the step of the Director stage the open task waits on, such as grill, "" when it waits on none.
func (b webBuilder) directorStage() string {
	if n, ok := b.next(); ok && b.ct.view.State == store.TaskOpen && interp.Wait(n.Wait) == interp.WaitDirectorStage {
		base, _ := workflow.ParseSliceStep(n.Step)
		return base
	}
	return ""
}

// Task reads repo/id's page model.
func (w webSource) Task(repo, id string) (web.Task, error) {
	d, err := w.a.clerkReadWait(context.Background(), w.c, webLockWait)
	if err != nil {
		return web.Task{}, webBusy(err)
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
		return web.Task{}, webBusy(err)
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
	t.DirectorWaits = b.directorWaits()
	t.DirectorStage = b.directorStage()
	for _, c := range b.tl.Context {
		t.Context = append(t.Context, web.Quote{Text: c.Text, Author: c.Author, At: c.AddedAt})
	}
	t.Where = b.where(t.Asks, t.DirectorStage, t.DirectorWaits)
	t.Slices = b.slices()
	t.Status = b.status()
	t.Steps = b.steps()
	t.Grill = b.grillLine()
	t.Story = b.story(t.Documents)
	for _, e := range b.h.Conversation {
		t.Conversation = append(t.Conversation, web.Message{Number: e.Number, Author: e.Author, Tag: e.Tag, Stage: webTagStage(e.Tag),
			Kind: e.Kind, Text: e.Text, At: e.At, Option: e.Option, Words: e.Words, Confirms: e.Confirms, Round: e.Round})
	}
	for _, d := range b.h.Drafts {
		t.Drafts = append(t.Drafts, web.Draft{Tag: d.Tag, Round: d.Round, Question: d.Question, Mark: d.Mark, Text: d.Text, At: d.At})
	}
	if n := len(b.tl.Attempts); n > 0 && v.State == store.TaskOpen {
		at := b.tl.Attempts[n-1]
		t.Tag = interp.DecisionID(at.Step, at.Number, false)
		t.TagStage = webTagStage(t.Tag)
	}
	return t
}

// webTagStage is the step a conversation tag or decision id belongs to, as its chip names it, such as grill for
// grill-2 or accept for accept--s2-1; "" for a name of no step, such as a question q1.
func webTagStage(tag string) string {
	p, ok := interp.RecordPlace(tag)
	if !ok {
		return ""
	}
	base, _ := workflow.ParseSliceStep(p.Step)
	return base
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

// recordPlace is where interp.RecordPlace puts a record name, or for a name of no record form, such as follow-ups,
// the whole name as its step, with its slice qualifier read off, and round 0.
func recordPlace(name string) interp.Place {
	if p, ok := interp.RecordPlace(name); ok {
		return p
	}
	step, k := workflow.ParseSliceStep(name)
	return interp.Place{Step: step, Slice: k}
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
	p := recordPlace(name)
	if p.Part == "send-back" {
		return "Feedback", "", 0
	}
	step, round = p.Step, p.Round
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
	rounds := map[string]int{}
	for _, d := range b.fl.Documents {
		if p, ok := interp.RecordPlace(d.Name); ok {
			rounds[docRoundKey(p)]++
		}
	}
	for _, d := range b.fl.Documents {
		if recordPlace(d.Name).Part == "inputs" {
			continue
		}
		kind, step, round := b.docKind(d.Name)
		out = append(out, web.Document{Repository: v.Repository, TaskID: v.ID, Name: d.Name, Label: b.docLabel(d.Name, rounds), Path: d.Path, SHA256: d.SHA256, Kind: kind, Step: step, Attempt: round, RecordedAt: d.RecordedAt})
	}
	for _, l := range b.activeLinks() {
		s := l.Source
		at := l.AddedAt
		_, step, round := b.docKind(s.Name)
		// Another task's document is named by that task and the round it is from, its workflow unread.
		label := (webBuilder{}).docLabel(s.Name, nil)
		if label != s.Name {
			label = s.TaskID + "'s " + lowerFirst(label)
		}
		out = append(out, web.Document{Repository: s.Repository, TaskID: s.TaskID, Name: s.Name, Label: label, Path: s.Path, SHA256: s.SHA256, Kind: "Linked", Step: step, Attempt: round, RecordedAt: &at})
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
		a := web.Ask{ID: dec.ID, Kind: string(dec.Kind), Question: dec.Question, Options: dec.Options, OpenedAt: dec.OpenedAt, Deferred: dec.Deferred,
			Stage: webTagStage(dec.ID)}
		a.Recommendation = b.recommendation(dec)
		a.Headline, a.Phrase = b.askHeadline(a), b.askPhrase(a)
		if e := dec.Explanation; e != nil {
			a.Explanation = &web.Explanation{Text: e.Text, Recommend: e.Recommend, At: e.At, Stale: !e.Current}
		}
		if s := dec.Subject; s != nil {
			a.Subject = s.Digest
			a.SubjectDocument = subjectDocument(docs, v, *s)
		}
		if dec.Kind == store.DecisionApproval {
			a.Mockup = b.mockupDocument(docs, a.SubjectDocument)
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

// recommendation is the Director's one recommendation on dec, read from the store's own record: stale once its digest
// is not the decision's. A decision with no record, opened before records were kept, falls back to its question's
// recommended field, then to its current explanation's recommendation, neither with a Confirm; nil for none.
func (b webBuilder) recommendation(dec statusDecision) *web.Recommendation {
	var hd *store.Decision
	for i := range b.h.Decisions {
		if b.h.Decisions[i].ID == dec.ID {
			hd = &b.h.Decisions[i]
		}
	}
	if hd == nil {
		return nil
	}
	for _, r := range b.h.Recommendations {
		if r.Decision == dec.ID {
			return &web.Recommendation{Option: r.Option, Words: r.Words, Number: r.Number, At: r.At, Stale: !r.Current(*hd)}
		}
	}
	if hd.Recommended != "" {
		return &web.Recommendation{Option: hd.Recommended}
	}
	if e := dec.Explanation; e != nil && e.Current && e.Recommend != "" {
		return &web.Recommendation{Option: e.Recommend, At: e.At}
	}
	return nil
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

// mockupDocument names a design ask's mock-up: when subject, the document a design round's approval is bound to, is
// a designer attempt's design, the mock-up interp.MockupDocument names among the task's documents, its named mockup
// part or the legacy designer-<k>-design-2; "" otherwise.
func (b webBuilder) mockupDocument(docs []web.Document, subject string) string {
	v := b.ct.view
	name, _ := interp.MockupDocument(b.def, subject, func(name string) bool {
		return slices.ContainsFunc(docs, func(d web.Document) bool {
			return d.Repository == v.Repository && d.TaskID == v.ID && d.Name == name
		})
	})
	return name
}

// openFindings are the findings of the newest review outcome of the step a loop-exit decision id names.
func (b webBuilder) openFindings(id string) []web.Finding {
	at := recordPlace(id)
	var newest *timedFindings
	for i := range b.tl.Findings {
		f := &b.tl.Findings[i]
		if p := recordPlace(f.Document); p.Step == at.Step && p.Slice == at.Slice {
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
		st.Step = placeName(b.stepWordsOf(base).noun, k)
		for _, at := range b.tl.Attempts {
			if s, sk := workflow.ParseSliceStep(at.Step); s == base && sk == k {
				st.Round = max(st.Round, at.Number)
			}
		}
	}
	if len(b.tl.Attempts) > 0 {
		at := b.tl.Attempts[len(b.tl.Attempts)-1]
		a := &web.Attempt{Step: at.Step, Name: b.attemptName(at.Step, at.Number), Number: at.Number, Outcome: string(at.Outcome), At: cmp.Or(at.FinishedAt, at.StartedAt)}
		st.Attempt = a
	}
	if pr := b.ct.pr; pr != nil {
		st.PR = &web.PullRequest{Number: pr.Number, URL: pr.URL, Merged: b.ct.merging}
		switch b.ct.checksState() {
		case "CI passed":
			st.CI = web.CIPassed
		case "CI failed":
			st.CI = web.CIFailed
		case "CI pending", "no CI run":
			st.CI = web.CIPending
		}
	}
	return st
}

// webStepName is a step id as the page names it: its first letter upper-cased, - made a space.
func webStepName(id string) string {
	return web.OptionWord(id)
}

// steps is the stepper: each step of the bound workflow in order, with the state of its records in the current
// slice (a step outside the slice range, of slice 1), or later when it has none.
func (b webBuilder) steps() []web.Step {
	if b.def == nil {
		return nil
	}
	cur := b.current()
	def, _ := workflow.ApplyGrillMode(b.def, b.grillMode().Mode)
	var out []web.Step
	for _, s := range def.Steps {
		slice := 1
		if def.InSlices(s.ID) {
			slice = cur
		}
		st := web.Step{Name: webStepName(s.ID), State: web.StateLater, Note: "later"}
		// An Artisan grill has no rounds with the Guildmaster, so its row names it and says finished, not rounds.
		unattended := s.Type == "grill" && s.Actor() == workflow.ArtisanActor
		if unattended {
			st.Name = webUnattendedGrill
		}
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
				if unattended {
					st.Note = "finished"
				}
			default:
				st.State, st.Note = web.StateFailed, string(last.Outcome)
			}
		}
		for _, dec := range b.tl.Decisions {
			if p, ok := interp.RecordPlace(dec.ID); !ok || p.Part != "" || p.Step != s.ID || p.Slice != slice {
				continue
			}
			switch {
			case dec.Open():
				st.State, st.Note = web.StateYou, "waiting on you"
			case dec.Void:
			case dec.Choice != "":
				// No positive or negative option has a _, so this keeps their words raw as before.
				word := strings.ReplaceAll(dec.Choice, "_", " ")
				st.State, st.Note, st.Raw = web.DecisionState(string(dec.Kind), dec.Choice), b.decisionNoun(dec.Decision)+" "+word, dec.ID+" "+word
			case dec.Answered:
				st.State, st.Note, st.Raw = web.StateDone, b.decisionNoun(dec.Decision)+" answered", dec.ID+" answered"
			}
		}
		if st.State == web.StateYou {
			st.Note, st.Raw = "waiting on you", ""
		}
		out = append(out, st)
	}
	return out
}

// grillMode is the task's grill mode in effect: its latest record, or live for a task with none.
func (b webBuilder) grillMode() store.GrillMode {
	return store.CurrentGrillMode(b.h.GrillModes)
}

// grillLine is the band's grill-mode line, task show's "grill: unattended (director): <reason>" or "grill: live";
// "" for a task whose workflow has no grill step and that has no grill-mode record, such as a bug.
func (b webBuilder) grillLine() string {
	g := b.grillMode()
	if g.Number == 0 && (b.def == nil || !slices.ContainsFunc(b.def.Steps, func(s workflow.Step) bool { return s.Type == "grill" })) {
		return ""
	}
	return g.Line()
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
		n := web.Node{Title: "You added the task", State: web.StateDone, Word: "finished", Start: &at, Events: []web.Event{{At: &at, Text: "task " + v.ID + " added"}}}
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
		id := interp.DecisionID(at.Step, at.Number, false)
		n := web.Node{Raw: id, Start: at.StartedAt, End: at.FinishedAt, Events: b.attemptEvents(at, id), Tag: id}
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
		prefix := id + "-"
		n.Documents = own(prefix)
		back, findings := "", false
		if f := b.findingsOf(prefix); f != nil && len(f.Findings) > 0 {
			findings = true
			n.Findings = b.fixed(i, at.Step, f)
			if back = b.sentBackTo(i); back != "" && at.Outcome == store.AttemptSucceeded {
				n.State, n.Word = web.StateBack, "changes requested · sent back to "+strings.ToLower(webStepName(back))
			}
		}
		n.Title = b.attemptSentence(at, back, findings)
		if fb := b.feedback(at.Step, at.Number); fb != "" {
			name := interp.SendBackName(at.Step, at.Number)
			n.Said = append(n.Said, web.Said{Text: fb, Where: "the Director's feedback, sent back to " + at.Step, Back: true})
			n.Documents = append(n.Documents, name)
			n.Events = append(n.Events, web.Event{At: b.recordedAt(name), Text: "document " + name + " recorded"})
		}
		nodes = append(nodes, n)
	}
	for _, dec := range b.tl.Decisions {
		n := web.Node{Title: b.decisionSentence(dec), Raw: dec.ID, Start: dec.OpenedAt, End: dec.AnsweredAt, Events: decisionEvents(dec), Tag: dec.ID}
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
		case dec.Choice != "":
			n.State, n.Word = web.DecisionState(string(dec.Kind), dec.Choice), web.OptionWord(dec.Choice)
		default:
			n.State, n.Word = web.StateDone, "answered"
		}
		if dec.Answered {
			words := strings.TrimSpace(dec.Answer)
			if dec.Choice != "" {
				words = dec.Note
			}
			if words != "" {
				// A carry-on answer's words stay violet, as a send-back's do (the Guildmaster's call at d135 accept-1).
				m, _ := web.OptionMeaning(string(dec.Kind), dec.Choice)
				n.Said = append(n.Said, web.Said{Text: words, Where: webAnswerWhere(dec.Decision), Back: n.State == web.StateBack || m.Tone == web.ToneCarryOn})
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
		title := "The work was merged"
		if b.ct.status.Slices != nil {
			title = "Slice " + strconv.Itoa(interp.DocumentSlice(d.Name)) + " was merged"
		}
		nodes = append(nodes, web.Node{Title: title, Raw: d.Name, State: web.StateDone, Word: "merged", Start: d.RecordedAt, Documents: []string{d.Name},
			Events: []web.Event{{At: d.RecordedAt, Text: "document " + d.Name + " recorded"}}})
	}
	if v.State == store.TaskCancelled {
		at := v.CancelledAt
		n := web.Node{Title: "The task was cancelled", State: web.StateFailed, Word: "cancelled", Body: oneLine(v.CancelReason)}
		ev := web.Event{Text: "task " + v.ID + " cancelled"}
		if !at.IsZero() {
			n.Start, ev.At = &at, &at
		}
		if r := oneLine(v.CancelReason); r != "" {
			ev.Text += " · " + r
		}
		n.Events = []web.Event{ev}
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
		if lf := b.findingsOf(interp.DecisionID(later.Step, later.Number, false) + "-"); lf != nil {
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
	name := interp.SendBackName(step, n)
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

// attemptEvents are the raw records behind an attempt's story node: its start and its finish or intake refusal.
func (b webBuilder) attemptEvents(at timedAttempt, id string) []web.Event {
	started := "attempt " + id + " started"
	base, _ := workflow.ParseSliceStep(at.Step)
	if typ := b.stepType(base); typ != "" {
		started += " · " + typ
	}
	out := []web.Event{{At: at.StartedAt, Text: started}}
	switch {
	case at.IntakeRejection != "":
		out = append(out, web.Event{At: at.FinishedAt, Text: "attempt " + id + " refused at intake · " + oneLine(at.IntakeRejection)})
	case at.Outcome != "":
		out = append(out, web.Event{At: at.FinishedAt, Text: "attempt " + id + " " + string(at.Outcome)})
	}
	return out
}

// decisionEvents are the raw records behind a decision's story node: its opening, with its kind and subject, and its
// answer or void.
func decisionEvents(dec timedDecision) []web.Event {
	opened := "decision " + dec.ID + " opened"
	if dec.Kind != "" {
		opened += " · " + string(dec.Kind)
	}
	if s := dec.Subject; s != nil && s.Digest != "" {
		opened += " · subject " + webShortDigest(s.Digest)
	}
	out := []web.Event{{At: dec.OpenedAt, Text: opened}}
	switch {
	case dec.Void:
		text := "decision " + dec.ID + " void"
		if r := oneLine(dec.VoidReason); r != "" {
			text += " · " + r
		}
		out = append(out, web.Event{Text: text})
	case dec.Answered:
		text := "decision " + dec.ID + " answered"
		if dec.Choice != "" {
			text += " " + dec.Choice
		}
		if dec.AnsweredVia == store.AnsweredViaPage {
			text += " · on the task page by " + dec.AnsweredBy
		}
		out = append(out, web.Event{At: dec.AnsweredAt, Text: text})
	}
	return out
}

// webShortDigest is a digest as a raw event names it: a document digest's first 12 hex, a head's first 7.
func webShortDigest(d string) string {
	if hexPart, ok := strings.CutPrefix(d, "sha256:"); ok {
		return "sha256:" + hexPart[:min(12, len(hexPart))]
	}
	return d[:min(7, len(d))]
}

// recordedAt is when the task's document name was recorded, nil when it holds none or the time is unknown.
func (b webBuilder) recordedAt(name string) *time.Time {
	for _, d := range b.fl.Documents {
		if d.Name == name {
			return d.RecordedAt
		}
	}
	return nil
}
