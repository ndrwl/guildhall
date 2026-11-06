package interp

import (
	"cmp"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// batchLinkSchema versions a batch member's link, <step>-<k>-link-<n> (docs/interpreter.md#merge-batches).
const batchLinkSchema = "guildhall.batch-link/1"

// batchLinkDoc is <step>-<k>-link-<n>: one replay of a batch member's head onto its predecessor's link, classified as
// a rebase is and recorded once, never changed. Leader and BatchStay name the batch it was made in: its leader and the
// leader's front stay. A link is not one of the task's own rebases: chain never follows it, so a member that leaves
// the batch is rebased from its own head, as any front is.
type batchLinkDoc struct {
	rebaseDoc
	Leader    string `json:"leader"`
	BatchStay int    `json:"batch_stay"`
}

// batchLink is one recorded batch link of a task, by name.
type batchLink struct {
	name string
	k, n int
	doc  batchLinkDoc
}

// head is the link's replayed head, empty unless the replay was no_conflicts.
func (l batchLink) head() string {
	if l.doc.Kind == config.RebaseNoConflicts {
		return l.doc.Rebased
	}
	return ""
}

// batchLinkName is <step>-<k>-link-<n>.
func batchLinkName(step string, k, n int) string {
	return fmt.Sprintf("%s-link-%d", DecisionID(step, k, false), n)
}

// parseBatchLinkName reads <step>-<k>-link-<n> for step, k and n positive numbers.
func parseBatchLinkName(step, name string) (k, n int, ok bool) {
	rest, ok := strings.CutPrefix(name, step+"-")
	if !ok {
		return 0, 0, false
	}
	ks, ns, ok := strings.Cut(rest, "-link-")
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

// batchLinks returns step's recorded batch links in round and number order.
func (r *run) batchLinks(step string) ([]batchLink, error) {
	var out []batchLink
	for _, d := range r.documents {
		k, n, ok := parseBatchLinkName(step, d.Name)
		if !ok {
			continue
		}
		l := batchLink{name: d.Name, k: k, n: n}
		if _, err := r.readJSONDoc(d.Name, &l.doc); err != nil {
			return nil, err
		}
		if l.doc.Schema != batchLinkSchema || !digest.ValidHead(l.doc.From) {
			return nil, fmt.Errorf("document %s is not a %s link", d.Name, batchLinkSchema)
		}
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b batchLink) int { return cmp.Or(cmp.Compare(a.k, b.k), cmp.Compare(a.n, b.n)) })
	return out, nil
}

// batchMember is the advance of a task behind the leader of the standing batch q.view.Batch, under merge
// authorization id (docs/interpreter.md#merge-batches). Once its predecessor has a link, it replays its own head onto
// that link with the tiered rebase, recorded as <step>-<k>-link-<n>, pushes a no_conflicts replay to its pull request
// and records it as its link; a replay with conflicts, an approval that would not carry over it, or a running attempt
// takes it out of the batch, to be rebased as any front is when its turn comes. It waits in the queue either way.
func (r *run) batchMember(s workflow.Step, repo config.Repository, dl deliverHead, id string, q queue) (Next, error) {
	b := q.view.Batch
	place := b.Member(r.task.ID)
	pred, me := b.Members[place-2], b.Members[place-1]
	if pred.Link == "" {
		return r.queued(s, q, false)
	}
	own, err := r.chain(dl.head, s.ID)
	if err != nil {
		return Next{}, err
	}
	h := chainHead(dl.head, own)
	p, _ := parseDecision(id)
	l, err := r.recordBatchLink(s, p.round, repo, id, *b, me, h, pred.Link)
	switch {
	case errors.Is(err, errRebaseRunning):
		return r.leftBatch(s, q, "an attempt of the task is running, so its head cannot be replayed")
	case err != nil:
		return Next{}, err
	case l.doc.Kind != config.RebaseNoConflicts:
		return r.leftBatch(s, q, fmt.Sprintf("replaying head %s onto the link %s of task %s is %s (%s)", h, pred.Link, pred.TaskID, l.doc.Kind, l.doc.Reason))
	case repo.Merge.Requires.Guildmaster() && repo.Merge.NeedsReapproval(config.RebaseNoConflicts):
		return r.leftBatch(s, q, fmt.Sprintf("its approval does not carry over a no_conflicts replay under reapprove_after_rebase = %s", repo.Merge.Reapprove()))
	}
	if me.Link != l.head() {
		if _, err := r.publishBatchLink(s, repo, l, dl.pr, place, len(b.Members)); err != nil {
			return Next{}, err
		}
		_, slice := workflow.ParseSliceStep(s.ID)
		if _, err := q.q.SetMergeBatchLink(r.batchMutation("merge-batch-link", *b, l.name), r.task.Repository, r.task.ID, slice, l.head()); err != nil {
			return Next{}, err
		}
		return r.queued(s, q, false)
	}
	// A red train is bisected on the members' own link checks.
	if b.State == store.BatchRed {
		return r.bisectMember(s, repo, id, q, l, h)
	}
	// Once the train's checks passed, the member runs its merge gates against its link and marks itself ready.
	if b.State == store.BatchLanding && b.Train != nil && !me.Ready {
		wait, why, err := r.authorizeLink(s, repo, dl, id, l, own, *b)
		switch {
		case err != nil:
			return Next{}, err
		case wait != nil:
			return *wait, nil
		case why != "":
			return r.leftBatch(s, q, why)
		}
		if _, err := q.q.SetMergeBatchReady(r.batchMutation("merge-batch-ready", *b, batchRound(*b)), r.task.Repository, r.task.ID, l.head()); err != nil {
			return Next{}, err
		}
		view, err := q.q.MergeQueue(r.task.Repository)
		if err != nil {
			return Next{}, err
		}
		q.view = view
	}
	return r.queued(s, q, false)
}

// batchMutation is the mutation id of a batch change of kind by this task, unique to the batch b and to what.
func (r *run) batchMutation(kind string, b store.MergeBatch, what string) string {
	return r.mutation(kind, b.Leader, strconv.Itoa(b.Stay), what)
}

// recordBatchLink returns the batch link of head h onto link onto in batch b under authorization id, recording the
// replay as the next <step>-<k>-link-<n> when none was made.
func (r *run) recordBatchLink(s workflow.Step, k int, repo config.Repository, id string, b store.MergeBatch, me store.MergeBatchMember, h, onto string) (batchLink, error) {
	all, err := r.batchLinks(s.ID)
	if err != nil {
		return batchLink{}, err
	}
	if i := slices.IndexFunc(all, func(l batchLink) bool {
		return l.doc.From == h && l.doc.Onto == onto && l.doc.Authorization == id
	}); i >= 0 {
		return all[i], nil
	}
	doc, err := r.replay(repo, h, onto)
	if err != nil {
		return batchLink{}, err
	}
	doc.Schema, doc.Authorization, doc.Stay = batchLinkSchema, id, me.Stay
	n := 1
	for _, l := range all {
		if l.k == k && l.n >= n {
			n = l.n + 1
		}
	}
	l := batchLink{name: batchLinkName(s.ID, k, n), k: k, n: n, doc: batchLinkDoc{rebaseDoc: doc, Leader: b.Leader, BatchStay: b.Stay}}
	return l, r.putJSONDoc(l.name, DecisionID(s.ID, k, false), fmt.Sprintf("link-%d.json", n), l.doc)
}

// publishBatchLink pushes link l to the task's pull request pr, whose checks then run on the batch's prefix up to the
// task, and records it as <link>-pr; a link already pushed returns its record. Unlike a rebase it leaves the task's
// worktree on the task's own head. It tells the Director once.
func (r *run) publishBatchLink(s workflow.Step, repo config.Repository, l batchLink, pr *pullRequestDoc, place, size int) (*pullRequestDoc, error) {
	var out pullRequestDoc
	if ok, err := r.readJSONDoc(l.name+"-pr", &out); err != nil || ok {
		return &out, err
	}
	if pr == nil {
		return nil, fmt.Errorf("%w: batching head %s, which no publish step pushed", ErrUnsupported, l.doc.From)
	}
	title := cmp.Or(pr.Title, r.linkTitle())
	body := fmt.Sprintf("Guildhall task %s/%s, replayed by step %s onto %s at %s (%s), as place %d of %d in the merge batch task %s leads; Guildhall puts the task's own head back if it leaves the batch.\n",
		r.task.Repository, r.task.ID, s.ID, l.doc.Onto, l.head(), l.doc.Kind, place, size, l.doc.Leader)
	if f := r.task.Fixes; f != nil {
		body += fmt.Sprintf("\nFixes a defect introduced by task `%s/%s`.\n", f.Repository, f.ID)
	}
	pushed, err := r.e.Forge.Publish(r.ctx, forge.Change{Repository: repo.Path, Remote: pr.Remote, Branch: r.branch(), Head: l.head(), Title: title, Body: body})
	if err != nil {
		return nil, err
	}
	out = pullRequestDoc{Schema: pullRequestSchema, Remote: pr.Remote, Title: title, PullRequest: pushed}
	if err := r.putPullRequest(l.name+"-pr", path.Join("tasks", r.task.Repository, r.task.ID, l.name, "pull-request.json"), out); err != nil {
		return nil, err
	}
	text := fmt.Sprintf("Replayed head %s onto %s, the link of the task before it in the merge batch task %s leads, as %s (place %d of %d), and pushed it to pull request %s; its checks run on the batch so far.",
		l.doc.From, l.doc.Onto, l.doc.Leader, l.head(), place, size, pushed.URL)
	return &out, r.notify("batch-link."+l.name, store.MessageRebased, text)
}

// leftBatch takes the task out of its batch, telling the Director why, and waits in the queue.
func (r *run) leftBatch(s workflow.Step, q queue, why string) (Next, error) {
	b := *q.view.Batch
	if err := r.leaveBatch(q, why); err != nil {
		return Next{}, err
	}
	text := fmt.Sprintf("Task %s/%s left the merge batch task %s leads: %s. It keeps its place in the merge queue and is rebased onto the base when it reaches the front.",
		r.task.Repository, r.task.ID, b.Leader, why)
	if err := r.notify("batch-left."+b.Leader+"."+strconv.Itoa(b.Stay), store.MessageMergeQueued, text); err != nil {
		return Next{}, err
	}
	view, err := q.q.MergeQueue(r.task.Repository)
	if err != nil {
		return Next{}, err
	}
	q.view = view
	return r.queued(s, q, false)
}

// leaveBatch takes the task out of the standing batch it is a member of behind the leader, if any.
func (r *run) leaveBatch(q queue, why string) error {
	b := q.view.Batch
	if q.q == nil || b == nil || b.Member(r.task.ID) < 2 {
		return nil
	}
	_, err := q.q.LeaveMergeBatch(r.batchMutation("merge-batch-leave", *b, digest.Of([]byte(why)).Hex()[:12]), r.task.Repository, r.task.ID)
	return err
}

// leadLink records head x as the leader's link when the task, at the front, leads a standing batch, base being the
// tip it is up to date with; once every member has its link, the batch moves to testing, and the leader's merge
// lands the batch's train pull request (leadTrain).
func (r *run) leadLink(s workflow.Step, q *queue, x, base string) error {
	b := q.view.Batch
	if q.q == nil || b == nil || b.Leader != r.task.ID {
		return nil
	}
	if b.Members[0].Link != x {
		_, slice := workflow.ParseSliceStep(s.ID)
		if _, err := q.q.SetMergeBatchLink(r.batchMutation("merge-batch-link", *b, x), r.task.Repository, r.task.ID, slice, x); err != nil {
			return err
		}
		view, err := q.q.MergeQueue(r.task.Repository)
		if err != nil {
			return err
		}
		q.view = view
		if b = view.Batch; b == nil {
			return nil
		}
	}
	if b.State != store.BatchAssembling || slices.ContainsFunc(b.Members, func(m store.MergeBatchMember) bool { return m.Link == "" }) {
		return nil
	}
	if !digest.ValidHead(base) {
		base = ""
	}
	// Keyed on the links, so a batch assembled again after a member left is tested again.
	if _, err := q.q.SetMergeBatchState(r.batchMutation("merge-batch-state", *b, string(store.BatchTesting)+":"+batchRound(*b)), r.task.Repository, r.task.ID, store.BatchTesting, base); err != nil {
		return err
	}
	view, err := q.q.MergeQueue(r.task.Repository)
	if err != nil {
		return err
	}
	q.view = view
	return nil
}

// restoreHead pushes head x back to the task's pull request pr when a batch link the task pushed, in a batch it no
// longer waits in, may still be there, recording <link>-restored for each such link; it returns the pull request
// as pushed, or pr when there is none. Head reviewed, the one the publish step pushed, gets that step's text back.
func (r *run) restoreHead(s workflow.Step, repo config.Repository, reviewed, x string, pr *pullRequestDoc) (*pullRequestDoc, error) {
	links, err := r.batchLinks(s.ID)
	if err != nil {
		return nil, err
	}
	var stale []batchLink
	for _, l := range links {
		if r.document(l.name+"-pr") != nil && r.document(l.name+"-restored") == nil {
			stale = append(stale, l)
		}
	}
	if len(stale) == 0 {
		return pr, nil
	}
	if pr == nil {
		return nil, fmt.Errorf("%w: restoring head %s, which no publish step pushed", ErrUnsupported, x)
	}
	title := cmp.Or(pr.Title, r.linkTitle())
	body := fmt.Sprintf("Guildhall task %s/%s at head %s, put back by step %s after the merge batch it waited in.\n", r.task.Repository, r.task.ID, x, s.ID)
	if f := r.task.Fixes; f != nil {
		body += fmt.Sprintf("\nFixes a defect introduced by task `%s/%s`.\n", f.Repository, f.ID)
	}
	// The head the publish step pushed gets its own text back.
	from, _, _ := strings.Cut(s.Inputs["head"][0], ".")
	if i := slices.IndexFunc(r.def.Steps, func(p workflow.Step) bool { return p.ID == from }); i >= 0 && r.def.Steps[i].Type == "publish" && x == reviewed {
		text, err := r.prText(r.def.Steps[i], x)
		if err != nil {
			return nil, err
		}
		title, body = pullRequestText(text)
	}
	pushed, err := r.e.Forge.Publish(r.ctx, forge.Change{Repository: repo.Path, Remote: pr.Remote, Branch: r.branch(), Head: x, Title: title, Body: body})
	if err != nil {
		return nil, err
	}
	out := pullRequestDoc{Schema: pullRequestSchema, Remote: pr.Remote, Title: title, PullRequest: pushed}
	for _, l := range stale {
		if err := r.putPullRequest(l.name+"-restored", path.Join("tasks", r.task.Repository, r.task.ID, l.name, "restored.json"), out); err != nil {
			return nil, err
		}
	}
	return &out, nil
}
