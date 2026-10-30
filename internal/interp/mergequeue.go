package interp

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// MergeQueue is a repository's merge queue (docs/interpreter.md#the-merge-queue), the store's merge-queue records:
// *store.Store and cli.storeRecords implement it. Records that implement it queue every authorized merge; without
// it, every task is its own queue's front.
type MergeQueue interface {
	MergeQueue(repository string) (store.MergeQueue, error)
	JoinMergeQueue(mutationID, repository, taskID, authorization, record, pr string) (store.Receipt, error)
	LeaveMergeQueue(mutationID, repository, taskID string, keepReady bool) (store.Receipt, error)
	ClaimMergeFront(mutationID, repository, taskID string) (store.Receipt, error)
	ReleaseMergeFront(mutationID, repository, taskID string) (store.Receipt, error)
}

var _ MergeQueue = (*store.Store)(nil)

// rebaseSchema versions the rebase documents deliver and validate record, <step>-<k>-rebase-<n>.
const rebaseSchema = "guildhall.rebase/1"

// maxRebaseRounds is how many rebases one stay at the front makes before it waits for the Director.
const maxRebaseRounds = 3

// rebaseDoc is <step>-<k>-rebase-<n>: one replay of a head onto the base tip mergeability measured, recorded once
// it is classified and never changed. The rebased head is Rebased for no_conflicts; for minor_conflicts it is the
// resolution's squash, recorded as <step>-<k>-rebase-<n>-resolved.
type rebaseDoc struct {
	Schema string `json:"schema"`
	// Authorization is the decision or authorization id deliver worked under; empty at validate.
	Authorization string `json:"authorization,omitempty"`
	// Stay is the front stay the rebase was made in; zero at validate.
	Stay      int               `json:"stay,omitempty"`
	From      string            `json:"from"`
	Onto      string            `json:"onto"`
	MergeBase string            `json:"merge_base"`
	Kind      config.RebaseKind `json:"kind"`
	// Reason says why the kind is what it is.
	Reason      string         `json:"reason"`
	Marked      string         `json:"marked,omitempty"`
	Conflicts   []git.Conflict `json:"conflicts"`
	Lines       int            `json:"lines"`
	FromPatchID string         `json:"from_patch_id"`
	PatchID     string         `json:"patch_id,omitempty"`
	Rebased     string         `json:"rebased,omitempty"`
}

// rebaseLink is one recorded rebase of a task's head, by name, with the head it produced once known.
type rebaseLink struct {
	name string
	step string
	k, n int
	doc  rebaseDoc
	// head is the rebased head, empty while a minor_conflicts resolution is pending; covered reports whether the
	// rebase is proven to change nothing the review approved, or a narrowed review approved its resolution.
	head    string
	covered bool
	// findings counts the findings of the narrowed review that approved a resolution; any sends the merge to the
	// Guildmaster.
	findings int
}

// parseRebaseName reads <step>-<k>-rebase-<n> for step, n and k positive numbers.
func parseRebaseName(step, name string) (k, n int, ok bool) {
	rest, ok := strings.CutPrefix(name, step+"-")
	if !ok {
		return 0, 0, false
	}
	ks, ns, ok := strings.Cut(rest, "-rebase-")
	if !ok {
		return 0, 0, false
	}
	k, errK := strconv.Atoi(ks)
	n, errN := strconv.Atoi(ns)
	if errK != nil || errN != nil || k < 1 || n < 1 || strconv.Itoa(k) != ks || strconv.Itoa(n) != ns {
		return 0, 0, false
	}
	return k, n, true
}

func rebaseName(step string, k, n int) string {
	return fmt.Sprintf("%s-rebase-%d", decisionID(step, k, false), n)
}

// rebases returns step's recorded rebases in round and number order.
func (r *run) rebases(step string) ([]rebaseLink, error) {
	var out []rebaseLink
	for _, d := range r.documents {
		k, n, ok := parseRebaseName(step, d.Name)
		if !ok {
			continue
		}
		data, err := r.read(d)
		if err != nil {
			return nil, err
		}
		l := rebaseLink{name: d.Name, step: step, k: k, n: n}
		if err := json.Unmarshal(data, &l.doc); err != nil {
			return nil, fmt.Errorf("document %s: %w", d.Name, err)
		}
		if l.doc.Schema != rebaseSchema || !digest.ValidHead(l.doc.From) {
			return nil, fmt.Errorf("document %s is not a %s rebase", d.Name, rebaseSchema)
		}
		if err := r.resolveLink(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b rebaseLink) int { return cmp.Or(cmp.Compare(a.k, b.k), cmp.Compare(a.n, b.n)) })
	return out, nil
}

// resolveLink sets the link's head and coverage from its document and, for a resolved minor_conflicts rebase, its
// resolution and narrowed review.
func (r *run) resolveLink(l *rebaseLink) error {
	switch l.doc.Kind {
	case config.RebaseNoConflicts:
		l.head = l.doc.Rebased
		l.covered = l.doc.PatchID != "" && l.doc.PatchID == l.doc.FromPatchID
		return nil
	case config.RebaseMinorConflicts:
		return r.resolveMinor(l)
	}
	return nil
}

// chain follows the recorded rebases of steps from head h: each link replays the head the previous one produced. It
// stops at a link with no rebased head yet or at a head no rebase starts from.
func (r *run) chain(h string, steps ...string) ([]rebaseLink, error) {
	var all []rebaseLink
	for _, s := range steps {
		ls, err := r.rebases(s)
		if err != nil {
			return nil, err
		}
		all = append(all, ls...)
	}
	var out []rebaseLink
	for cur := h; ; {
		i := slices.IndexFunc(all, func(l rebaseLink) bool { return l.doc.From == cur && l.doc.Kind != config.RebaseMajorConflicts })
		if i < 0 {
			return out, nil
		}
		l := all[i]
		all = slices.Delete(all, i, i+1)
		out = append(out, l)
		if l.head == "" || l.head == cur {
			return out, nil
		}
		cur = l.head
	}
}

// chainHead is the head the chain ends at, h for an empty chain or one whose last link is unresolved.
func chainHead(h string, links []rebaseLink) string {
	for i := len(links) - 1; i >= 0; i-- {
		if links[i].head != "" {
			return links[i].head
		}
	}
	return h
}

// chainKind is the hardest kind among links; an unknown kind is harder than any, so it always needs a new decision.
func chainKind(links []rebaseLink) config.RebaseKind {
	k := config.RebaseNoConflicts
	for _, l := range links {
		if l.doc.Kind.Rank() < 0 {
			return l.doc.Kind
		}
		if l.doc.Kind.Rank() > k.Rank() {
			k = l.doc.Kind
		}
	}
	return k
}

// covered checks that every link of the chain to head x is proven to change nothing the review approved.
func covered(links []rebaseLink, x string) error {
	for _, l := range links {
		if !l.covered {
			return fmt.Errorf("%w: rebase %s of %s onto %s is neither patch-equivalent nor approved by a narrowed review", ErrReviewGate, l.name, l.doc.From, l.doc.Onto)
		}
		if l.head == x {
			return nil
		}
	}
	if len(links) == 0 {
		return nil
	}
	return fmt.Errorf("%w: no recorded rebase ends at head %s", ErrReviewGate, x)
}

// replay replays head onto base and classifies it against the repository's thresholds: no conflicts with equal
// patch ids is no_conflicts; no conflicts with unequal ones, or at most minor_conflict_files files and
// minor_conflict_lines lines, every one with markers, is minor_conflicts; anything else is major_conflicts.
func (r *run) replay(repo config.Repository, head, base string) (rebaseDoc, error) {
	if r.e.Workspaces == nil {
		return rebaseDoc{}, fmt.Errorf("%w: rebasing without workspaces", ErrUnsupported)
	}
	if err := r.refuseRunning(); err != nil {
		return rebaseDoc{}, err
	}
	rb, err := r.e.Workspaces.Rebase(r.ctx, git.Replay{Repository: repo.Path, Base: base, Head: head})
	if err != nil {
		return rebaseDoc{}, err
	}
	d := rebaseDoc{Schema: rebaseSchema, From: head, Onto: base, MergeBase: rb.MergeBase, Marked: rb.Marked, Conflicts: rb.Conflicts,
		Lines: rb.ConflictLines(), FromPatchID: rb.HeadPatchID, PatchID: rb.PatchID}
	if d.Conflicts == nil {
		d.Conflicts = []git.Conflict{}
	}
	var files []string
	for _, c := range rb.Conflicts {
		files = append(files, c.Path)
	}
	markers := !slices.ContainsFunc(rb.Conflicts, func(c git.Conflict) bool { return !c.Markers })
	switch {
	case len(rb.Conflicts) == 0 && rb.PatchID == rb.HeadPatchID && rb.Commit != "":
		d.Kind, d.Rebased, d.Reason = config.RebaseNoConflicts, rb.Commit, "no conflicts, and the replayed change is patch-identical to the reviewed one"
	case len(rb.Conflicts) == 0:
		d.Kind, d.Rebased = config.RebaseMinorConflicts, rb.Commit
		d.Reason = "no conflicts, but the replayed change differs from the reviewed one in its context lines"
	case !markers:
		d.Kind, d.Reason = config.RebaseMajorConflicts, "a conflict without markers (binary, delete/modify or rename) in "+strings.Join(files, ", ")
	case len(files) <= repo.Merge.MinorFiles() && d.Lines <= repo.Merge.MinorLines():
		d.Kind = config.RebaseMinorConflicts
		d.Reason = fmt.Sprintf("%d conflicting line(s) in %s, within %d file(s) and %d line(s)", d.Lines, strings.Join(files, ", "), repo.Merge.MinorFiles(), repo.Merge.MinorLines())
	default:
		d.Kind = config.RebaseMajorConflicts
		d.Reason = fmt.Sprintf("%d conflicting line(s) in %s, over %d file(s) or %d line(s)", d.Lines, strings.Join(files, ", "), repo.Merge.MinorFiles(), repo.Merge.MinorLines())
	}
	return d, nil
}

// errRebaseRunning reports a rebase refused because an attempt of the task is running.
var errRebaseRunning = errors.New("cannot rebase while an attempt runs")

// refuseRunning refuses a rebase, squash or worktree move while any attempt of the task runs, from a fresh read of
// its attempts rather than the list the advance began with, since another process may have started one since.
func (r *run) refuseRunning() error {
	attempts, err := r.e.Records.Attempts(r.task.Repository, r.task.ID)
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(attempts, func(a store.StageAttempt) bool { return a.Outcome == "" }); i >= 0 {
		return fmt.Errorf("%w: step %s attempt %d is still running", errRebaseRunning, attempts[i].Step, attempts[i].Number)
	}
	return nil
}

// putJSONDoc records v as document name of the task at <tasks>/<repo>/<task>/<dir>/<file>.
func (r *run) putJSONDoc(name, dir, file string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
		Path: path.Join("tasks", r.task.Repository, r.task.ID, dir, file), SHA256: digest.Of(data).Hex()}, data)
}

// readJSONDoc reads the recorded document name into v; it reports false when there is none.
func (r *run) readJSONDoc(name string, v any) (bool, error) {
	ref := r.document(name)
	if ref == nil {
		return false, nil
	}
	data, err := r.read(*ref)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("document %s: %w", ref.Name, err)
	}
	return true, nil
}

// publishLink pushes link l's rebased head to the task's pull request, after moving the worktree to it, and records
// the pull request as <link>-pr; a link already pushed returns its record. It tells the Director once.
func (r *run) publishLink(s workflow.Step, repo config.Repository, l rebaseLink, pr *pullRequestDoc) (*pullRequestDoc, error) {
	var out pullRequestDoc
	if ok, err := r.readJSONDoc(l.name+"-pr", &out); err != nil || ok {
		return &out, err
	}
	if pr == nil {
		return nil, fmt.Errorf("%w: rebasing head %s, which no publish step pushed", ErrUnsupported, l.doc.From)
	}
	if err := r.adopt(repo, l.doc.From, l.head); err != nil {
		return nil, err
	}
	title := forgeTitle(r.task.Title, r.task.ID)
	body := fmt.Sprintf("Guildhall task %s/%s, rebased by step %s onto %s at %s (%s).\n", r.task.Repository, r.task.ID, s.ID, l.doc.Onto, l.head, l.doc.Kind)
	if f := r.task.Fixes; f != nil {
		body += fmt.Sprintf("\nFixes a defect introduced by task `%s/%s`.\n", f.Repository, f.ID)
	}
	pushed, err := r.e.Forge.Publish(r.ctx, forge.Change{Repository: repo.Path, Remote: pr.Remote, Branch: TaskBranch(r.task.ID), Head: l.head, Title: title, Body: body})
	if err != nil {
		return nil, err
	}
	out = pullRequestDoc{Schema: pullRequestSchema, Remote: pr.Remote, PullRequest: pushed}
	if err := r.putPullRequest(l.name+"-pr", path.Join("tasks", r.task.Repository, r.task.ID, l.name, "pull-request.json"), out); err != nil {
		return nil, err
	}
	text := fmt.Sprintf("Rebased head %s onto %s as %s (%s: %s) and pushed it to pull request %s; its checks run next.", l.doc.From, l.doc.Onto, l.head, l.doc.Kind, l.doc.Reason, pushed.URL)
	return &out, r.notify("rebased."+l.name, store.MessageRebased, text)
}

// headChecks reads the checks of rebased head x on pull request pr for step s, as validate does, paced by
// ChecksRefresh, and records a finished read as <step>-<k>-checks-<x>. It returns the recorded result, or nil with the
// wait while they are pending.
func (r *run) headChecks(s workflow.Step, k int, repo config.Repository, x string, pr *pullRequestDoc) (*validationDoc, Next, error) {
	name := fmt.Sprintf("%s-checks-%s", decisionID(s.ID, k, false), x)
	var v validationDoc
	if ok, err := r.readJSONDoc(name, &v); err != nil || ok {
		return &v, Next{}, err
	}
	wait := Next{Wait: WaitChecks, Step: s.ID}
	if due, _, err := r.checksDue(s, x); err != nil || !due {
		return nil, wait, err
	}
	checks, err := r.e.Forge.Checks(r.ctx, forge.Commit{Repository: repo.Path, Remote: pr.Remote, Head: x})
	if err != nil {
		return nil, Next{}, err
	}
	if err := r.checksRead(s, x, false); err != nil {
		return nil, Next{}, err
	}
	v = validationDoc{Schema: validationSchema, Remote: pr.Remote, Number: pr.Number, URL: pr.URL, Head: x, Verdict: validationPassed, Checks: checks}
	if v.Checks == nil {
		v.Checks = []forge.Check{}
	}
	finished := len(checks) > 0 || repo.NoCI
	v.NoCI = len(checks) == 0 && repo.NoCI
	for _, c := range checks {
		switch c.State {
		case forge.CheckFailed:
			v.Verdict, v.Failing = validationFailed, append(v.Failing, c.Name)
		case forge.CheckPending:
			finished = false
		}
	}
	if v.Verdict == validationPassed && !finished {
		return nil, wait, r.notify(fmt.Sprintf("checks-pending.%s.%s", s.ID, x[:12]), store.MessageChecksPending,
			fmt.Sprintf("Step %s waits for the checks of rebased head %s on pull request %s to finish.", s.ID, x, pr.URL))
	}
	return &v, Next{}, r.putJSONDoc(name, decisionID(s.ID, k, false), "checks-"+x+".json", v)
}

// observe reads the pull request's mergeability at head h, without recording it; "not observed" without a reader
// or a pull request.
func (r *run) observe(repo config.Repository, pr *pullRequestDoc, h string) (forge.Mergeability, error) {
	reader, ok := r.e.Forge.(MergeabilityReader)
	if !ok || pr == nil {
		return forge.Mergeability{Head: h, State: "not observed"}, nil
	}
	m, err := reader.Mergeability(r.ctx, forge.Merge{Repository: repo.Path, Remote: pr.Remote, Number: pr.Number, Head: h})
	if err != nil {
		return forge.Mergeability{}, err
	}
	if m.Head != h {
		return forge.Mergeability{}, fmt.Errorf("%w: mergeability head %s is not %s", ErrEvent, m.Head, h)
	}
	m.State = cmp.Or(m.State, "clear")
	m.URL = cmp.Or(m.URL, pr.URL)
	if obstructed(m) && (!digest.ValidHead(m.Base) || m.Behind < 0) {
		return forge.Mergeability{}, fmt.Errorf("%w: invalid mergeability evidence", ErrEvent)
	}
	return m, nil
}

func obstructed(m forge.Mergeability) bool { return m.State == "conflicting" || m.State == "behind" }

// queue is the task's place in its repository's merge queue for one advance.
type queue struct {
	q     MergeQueue
	view  store.MergeQueue
	stay  int
	front bool
}

// enqueue queues the task under authorization id, whose receipt is record, with its pull request pr, and reports its
// place. With records that keep no queue the task is always the front, in stay 1.
func (r *run) enqueue(id, record string, pr *pullRequestDoc) (queue, error) {
	mq, ok := r.e.Records.(MergeQueue)
	if !ok {
		return queue{stay: 1, front: true}, nil
	}
	view, err := mq.MergeQueue(r.task.Repository)
	if err != nil {
		return queue{}, err
	}
	i := slices.IndexFunc(view.Entries, func(e store.QueuedEntry) bool { return e.TaskID == r.task.ID })
	if i < 0 || view.Entries[i].Authorization != id {
		url := ""
		if pr != nil {
			url = pr.URL
		}
		if _, err := mq.JoinMergeQueue(r.mutation("merge-queue", id), r.task.Repository, r.task.ID, id, record, url); err != nil {
			return queue{}, err
		}
		if view, err = mq.MergeQueue(r.task.Repository); err != nil {
			return queue{}, err
		}
	}
	out := queue{q: mq, view: view}
	if f := view.Front; f != nil && f.TaskID == r.task.ID {
		out.stay, out.front = f.Stay, true
	}
	return out, nil
}

// claim claims the front for the task when it is first in the queue.
func (r *run) claim(q *queue) error {
	if q.front || q.q == nil {
		return nil
	}
	i := slices.IndexFunc(q.view.Entries, func(e store.QueuedEntry) bool { return e.TaskID == r.task.ID })
	if i < 0 {
		return fmt.Errorf("task %s is not in the merge queue of %s", r.task.ID, r.task.Repository)
	}
	stay := q.view.Entries[i].Stays + 1
	_, err := q.q.ClaimMergeFront(r.mutation("merge-front", strconv.Itoa(stay)), r.task.Repository, r.task.ID)
	switch {
	case errors.Is(err, store.ErrMergeQueued):
		return nil
	case err != nil:
		return err
	}
	q.stay, q.front = stay, true
	return nil
}

// queued is the wait of a task behind the front, with one merge-queued message per place and front.
func (r *run) queued(s workflow.Step, q queue, held bool) (Next, error) {
	next := Next{Wait: WaitMergeQueue, Step: s.ID}
	pos := q.view.Position(r.task.ID)
	front := "no task"
	if f := q.view.Front; f != nil {
		front = "task " + f.TaskID
	}
	text := fmt.Sprintf("Task %s/%s is authorized to merge and waits in the merge queue at place %d of %d; %s holds the front.", r.task.Repository, r.task.ID, pos, len(q.view.Entries), front)
	if held {
		text = fmt.Sprintf("The merge of task %s/%s is held (task hold-merge): it stays authorized and keeps its place by ready time, but the queue skips it until it is unheld.", r.task.Repository, r.task.ID)
	}
	return next, r.notify("merge-queued."+digest.Of([]byte(text)).Hex()[:12], store.MessageMergeQueued, text)
}

// leave takes the task out of the queue: keeping its ready time after a build send-back, removing it after a merge.
func (r *run) leave(why string, keepReady bool) error {
	mq, ok := r.e.Records.(MergeQueue)
	if !ok {
		return nil
	}
	_, err := mq.LeaveMergeQueue(r.mutation("merge-leave", why), r.task.Repository, r.task.ID, keepReady)
	return err
}

// escalationSchema versions the record of a rebase that became a build send-back, <step>-<k>-escalated-<n>.
const escalationSchema = "guildhall.rebase-escalation/1"

// escalationDoc is <step>-<k>-escalated-<n>: a rebase, red CI or narrowed review that sent the build back.
type escalationDoc struct {
	Schema        string            `json:"schema"`
	Authorization string            `json:"authorization,omitempty"`
	From          string            `json:"from"`
	Onto          string            `json:"onto"`
	Kind          config.RebaseKind `json:"kind"`
	Files         []string          `json:"files"`
	Reason        string            `json:"reason"`
	// Carry reports that the rebuilt head may carry the authorization over under reapprove_after_rebase = never:
	// true for major conflicts, false for red CI, findings or a behaviour change, which need a new decision.
	Carry bool `json:"carry"`
	// SpecDigest is the accepted spec the authorization was given under: its -authorized document's, or, for a merge
	// decision, the one the review outcomes of the approved head were bound to when it escalated. A carry-over to the
	// rebuilt head needs the same spec.
	SpecDigest string `json:"spec_digest,omitempty"`
}

// escalations returns step's recorded escalations in order.
func (r *run) escalations(step string) ([]escalationDoc, []string, error) {
	var out []escalationDoc
	var names []string
	for _, d := range r.documents {
		rest, ok := strings.CutPrefix(d.Name, step+"-")
		if !ok || !strings.Contains(rest, "-escalated-") {
			continue
		}
		var e escalationDoc
		if _, err := r.readJSONDoc(d.Name, &e); err != nil {
			return nil, nil, err
		}
		if e.Schema != escalationSchema {
			continue
		}
		out, names = append(out, e), append(names, d.Name)
	}
	return out, names, nil
}

// buildStep is the Artisan step that produced the head step s takes, following the head inputs back.
func (r *run) buildStep(s workflow.Step) (workflow.Step, error) {
	for cur := s; ; {
		refs := cur.Inputs["head"]
		if len(refs) == 0 {
			return workflow.Step{}, fmt.Errorf("%w: step %s has no head input to send back", ErrUnsupported, cur.ID)
		}
		from, _, _ := strings.Cut(refs[0], ".")
		i := slices.IndexFunc(r.def.Steps, func(p workflow.Step) bool { return p.ID == from })
		if i < 0 {
			return workflow.Step{}, fmt.Errorf("step %s is not in the workflow", from)
		}
		if r.def.Steps[i].Actor() == workflow.ArtisanActor {
			return r.def.Steps[i], nil
		}
		cur = r.def.Steps[i]
	}
}

// escalate records that the rebase of head from cannot stand, takes the task out of the queue keeping its ready
// time, and sends the build that produced the head back with feedback naming the base, the files and the reason, as
// step send-back does; normal propagation then reruns review, publish and validate.
func (r *run) escalate(s workflow.Step, k int, e escalationDoc) (Next, error) {
	e.Schema = escalationSchema
	if e.Files == nil {
		e.Files = []string{}
	}
	if e.SpecDigest == "" {
		e.SpecDigest = r.specDigest(s)
		var a authorizationDoc
		if ok, err := r.readJSONDoc(e.Authorization+"-authorized", &a); err != nil {
			return Next{}, err
		} else if ok && a.SpecDigest != "" {
			e.SpecDigest = a.SpecDigest
		}
	}
	n := 1
	es, _, err := r.escalations(s.ID)
	if err != nil {
		return Next{}, err
	}
	i := slices.IndexFunc(es, func(x escalationDoc) bool {
		return x.From == e.From && x.Onto == e.Onto && x.Authorization == e.Authorization
	})
	name := fmt.Sprintf("%s-escalated-%d", decisionID(s.ID, k, false), n)
	if i >= 0 {
		e = es[i]
	} else {
		for r.document(name) != nil {
			n++
			name = fmt.Sprintf("%s-escalated-%d", decisionID(s.ID, k, false), n)
		}
		if err := r.putJSONDoc(name, decisionID(s.ID, k, false), fmt.Sprintf("escalated-%d.json", n), e); err != nil {
			return Next{}, err
		}
	}
	if err := r.leave("escalated:"+e.From, true); err != nil {
		return Next{}, err
	}
	build, err := r.buildStep(s)
	if err != nil {
		return Next{}, err
	}
	feedback := fmt.Sprintf("Guildhall could not bring head %s up to date with base %s by itself (%s: %s).\nConflicting files: %s.\nRebase the task's work onto base %s, resolve the conflicts so the change still does what the accepted spec asks, run the checks, and commit.",
		e.From, e.Onto, e.Kind, e.Reason, cmp.Or(strings.Join(e.Files, ", "), "none"), e.Onto)
	a := r.latestAttempt(build.ID)
	if a == nil || a.Outcome != store.AttemptSucceeded {
		return Next{}, fmt.Errorf("%w: step %s has no succeeded attempt to send back", ErrUnsupported, build.ID)
	}
	if r.document(sendBackName(build.ID, a.Number)) == nil {
		if err := r.sentBack(Event{Kind: StepSentBack, Step: build.ID, Attempt: a.Number, Reason: feedback}); err != nil {
			return Next{}, err
		}
	}
	text := fmt.Sprintf("Head %s could not be rebased onto %s by Guildhall (%s: %s); step %s attempt %d is sent back with the conflicts as feedback, and the task rejoins the merge queue at its first ready time once its new head is authorized.",
		e.From, e.Onto, e.Kind, e.Reason, build.ID, a.Number)
	if err := r.notify("rebase-escalated."+name, store.MessageRebaseEscalated, text); err != nil {
		return Next{}, err
	}
	return r.start(build)
}

// carryFrom returns, for a head with no authorization of its own, the escalation whose build send-back of an earlier
// authorized head left that authorization to carry over under reapprove_after_rebase = never; false when there is
// none.
func (r *run) carryFrom(s workflow.Step) (escalationDoc, bool, error) {
	es, _, err := r.escalations(s.ID)
	if err != nil || len(es) == 0 {
		return escalationDoc{}, false, err
	}
	if e := es[len(es)-1]; e.Carry && e.Authorization != "" {
		return e, true, nil
	}
	return escalationDoc{}, false, nil
}

// openMerge opens deliver step s's merge decision for round k bound to head x.
func (r *run) openMerge(s workflow.Step, k int, x, question string) (Next, error) {
	d := store.Decision{Repository: r.task.Repository, TaskID: r.task.ID, ID: decisionID(s.ID, k, false), Kind: store.DecisionMerge,
		Subject: &store.Subject{Kind: store.SubjectHead, Digest: x}, Options: mergeOptions, Question: boundedQuestion(question, mergeOptions)}
	if _, err := r.e.Records.OpenDecision(r.mutation("decision", d.ID), d); err != nil {
		return Next{}, err
	}
	r.addDecision(d)
	return Next{Wait: WaitDecision, Step: s.ID, Decision: d.ID}, r.notifyDecision(d, s.ID)
}

// decisionOn returns the latest merge decision of step s bound to head x, or nil.
func (r *run) decisionOn(s workflow.Step, x string) *store.Decision {
	var out *store.Decision
	for _, d := range r.rounds(s.ID) {
		if d.Subject != nil && d.Subject.Digest == x {
			out = d
		}
	}
	return out
}

// mergeQuestion is the text of a merge decision for head x, listing the findings and suggestions of outcomes.
func (r *run) mergeQuestion(s workflow.Step, k int, x string, pr *pullRequestDoc, conditions []condition, outcomes []reviewOutcome, lead string) string {
	for _, c := range conditions {
		if c.Requirement == "guildmaster_approval" && c.RequiredBy != "" {
			lead += fmt.Sprintf("The Guildmaster's answer is required because %s, the repository's policies, whatever merge_requires says.\n", c.RequiredBy)
		}
	}
	question := lead + fmt.Sprintf("Merge needed for %s/%s, step %s, attempt %d: head %s.\n", r.task.Repository, r.task.ID, s.ID, k, x)
	if pr != nil {
		question += fmt.Sprintf("Pull request: %s\n", pr.URL)
	}
	question += "\nMerge requirements of repository " + r.task.Repository + ":\n"
	for _, c := range conditions {
		question += "- " + c.Requirement + ": " + c.line() + "\n"
	}
	return question + "A merge answer overrides a failing nothing_open, and nothing else. An authorized merge joins the repository's merge queue, and Guildhall may rebase the head onto the base before it merges.\n" + r.openItems(outcomes)
}
