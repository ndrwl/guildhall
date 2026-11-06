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
	"github.com/ndrwl/guildhall/internal/policies"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// trainRecord is the train part of a task's <decision>-merge when it landed in a merge train
// (docs/interpreter.md#merge-batches): the batch's leader and the leader's front stay, the task's own pull request,
// which Guildhall closes, its link head, the head its own work is at, and the authorization its link landed under.
type trainRecord struct {
	Leader      string            `json:"leader"`
	Stay        int               `json:"stay"`
	PullRequest forge.PullRequest `json:"pull_request"`
	Link        string            `json:"link"`
	Head        string            `json:"head"`
	Authorized  string            `json:"authorized"`
}

// TrainBranch is the branch of the train pull request of the batch task leader leads in its stay'th front stay,
// guildhall/train/<leader>-<stay>: a task branch, never the base.
func TrainBranch(leader string, stay int) string {
	return fmt.Sprintf("%strain/%s-%d", branchPrefix, leader, stay)
}

// trainPRName is <step>-<k>-train-<n>-pr, the n'th push of a train pull request the leader made in round k.
func trainPRName(step string, k, n int) string {
	return fmt.Sprintf("%s-train-%d-pr", DecisionID(step, k, false), n)
}

// parseTrainPRName reads <step>-<k>-train-<n>-pr for step, k and n positive numbers.
func parseTrainPRName(step, name string) (k, n int, ok bool) {
	rest, ok := strings.CutPrefix(name, step+"-")
	if !ok {
		return 0, 0, false
	}
	if rest, ok = strings.CutSuffix(rest, "-pr"); !ok {
		return 0, 0, false
	}
	ks, ns, ok := strings.Cut(rest, "-train-")
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

// batchRound is a digest of the heads the batch's members are linked at and its train's, so a batch assembled or
// pushed again after a change makes new mutations rather than replaying the receipts of its earlier round.
func batchRound(b store.MergeBatch) string {
	var parts []string
	for _, m := range b.Members {
		parts = append(parts, m.TaskID+"="+m.Link)
	}
	if t := b.Train; t != nil {
		parts = append(parts, fmt.Sprintf("train=%d@%s", t.Number, t.Head))
	}
	return digest.Of([]byte(strings.Join(parts, ","))).Hex()[:12]
}

// standingQueue is the task's repository's merge queue, with ok false for records that keep none.
func (r *run) standingQueue() (MergeQueue, store.MergeQueue, bool, error) {
	mq, ok := r.e.Records.(MergeQueue)
	if !ok {
		return nil, store.MergeQueue{}, false, nil
	}
	view, err := mq.MergeQueue(r.task.Repository)
	return mq, view, err == nil, err
}

// batchTrain is the train pull request of the standing batch the task is a member of, or nil.
func (r *run) batchTrain() (*store.MergeTrain, error) {
	_, view, ok, err := r.standingQueue()
	if err != nil || !ok {
		return nil, err
	}
	if b := view.Batch; b != nil && b.Member(r.task.ID) > 0 {
		return b.Train, nil
	}
	return nil, nil
}

// leadTrain lands the standing batch of two or more the task leads from the front, once the task's own merge, id
// at its link h on its pull request pr, is authorized (docs/interpreter.md#merge-batches). With every member linked
// and the batch testing, it pushes the last link to the batch's train branch and opens the train pull request, reads
// that pull request's own checks, and on green moves the batch to landing and marks itself ready. When every member
// is ready it squash-merges the train, records the merge on the batch and moves it to landed, and then finishes as
// each member does. A red train is bisected (bisectTrain); a train whose base moved under it is assembled again
// (reassemble); and a leader whose batch shrank to itself closes the train and merges alone. It reports false when
// the task merges its own pull request.
func (r *run) leadTrain(s workflow.Step, id string, repo config.Repository, h string, pr *pullRequestDoc) (bool, Next, error) {
	mq, view, ok, err := r.standingQueue()
	if err != nil || !ok {
		return false, Next{}, err
	}
	b := view.Batch
	if b == nil || b.Leader != r.task.ID {
		return false, Next{}, nil
	}
	if b.State == store.BatchLanded {
		_, next, err := r.landedMember(s, id, repo, deliverHead{head: h, pr: pr}, *b)
		return true, next, err
	}
	if len(b.Members) < 2 {
		// A batch of one merges as the serial queue does; a train opened before it shrank is closed first.
		if b.Train == nil {
			return false, Next{}, nil
		}
		why := "the merge batch shrank to its leader"
		if b.State == store.BatchRed {
			why = "the batch was red and every member after task " + b.Leader + " left it, so its link, already green, merges alone"
		}
		return false, Next{}, r.endTrain(mq, *b, repo, why)
	}
	wait := Next{Wait: WaitMergeQueue, Step: s.ID}
	if b.Members[0].Link != h || b.State == store.BatchAssembling || slices.ContainsFunc(b.Members, func(m store.MergeBatchMember) bool { return m.Link == "" }) {
		// The members are still replaying onto the links before them.
		return true, wait, nil
	}
	// The queue's own authorization and stay name the leader's link checks and rebases, as the front's loop does.
	queued, stay := id, b.Stay
	if i := slices.IndexFunc(view.Entries, func(e store.QueuedEntry) bool { return e.TaskID == r.task.ID }); i >= 0 {
		queued = cmp.Or(view.Entries[i].Authorization, id)
	}
	p, _ := parseDecision(id)
	k := p.round
	if b.State == store.BatchRed {
		stop, alone, next, err := r.bisectTrain(s, repo, mq, *b, queued, h, pr)
		if err != nil || stop {
			return true, next, err
		}
		if alone {
			return false, Next{}, nil
		}
		if b, err = r.batchOf(mq); err != nil || b == nil {
			return true, wait, err
		}
	}
	train, err := r.publishTrain(s, k, repo, mq, *b, pr)
	if err != nil {
		return false, Next{}, err
	}
	v, next, err := r.headChecks(s, k, repo, train.Head, train)
	if err != nil || v == nil {
		return true, next, err
	}
	if v.Verdict != validationPassed {
		// A red train, after its one rerun, is bisected on its members' own link checks.
		if err := r.redTrain(mq, *b, train, v); err != nil {
			return false, Next{}, err
		}
		if b, err = r.batchOf(mq); err != nil || b == nil || b.State != store.BatchRed {
			return true, wait, err
		}
		stop, alone, next, err := r.bisectTrain(s, repo, mq, *b, queued, h, pr)
		switch {
		case err != nil || stop:
			return true, next, err
		case alone:
			return false, Next{}, nil
		}
		return true, wait, nil
	}
	if b, err = r.batchOf(mq); err != nil || b == nil {
		return true, wait, err
	}
	if b.State == store.BatchTesting {
		if _, err := mq.SetMergeBatchState(r.batchMutation("merge-batch-state", *b, string(store.BatchLanding)+":"+batchRound(*b)), r.task.Repository, r.task.ID, store.BatchLanding, ""); err != nil {
			return false, Next{}, err
		}
		if b, err = r.batchOf(mq); err != nil || b == nil {
			return true, wait, err
		}
	}
	if b.State != store.BatchLanding || b.Train == nil || b.Train.Head != train.Head {
		return true, wait, nil
	}
	// The leader's own gates held for its link when its merge was authorized.
	if !b.Members[0].Ready {
		if _, err := mq.SetMergeBatchReady(r.batchMutation("merge-batch-ready", *b, batchRound(*b)), r.task.Repository, r.task.ID, h); err != nil {
			return false, Next{}, err
		}
		if b, err = r.batchOf(mq); err != nil || b == nil {
			return true, wait, err
		}
	}
	if slices.ContainsFunc(b.Members, func(m store.MergeBatchMember) bool { return !m.Ready }) {
		return true, wait, nil
	}
	merged, next, err := r.mergeTrain(s, id, repo, train)
	if errors.Is(err, errTrainBehind) {
		// The base moved under the train: the batch is assembled again on the new tip.
		return true, wait, r.reassemble(s, repo, mq, *b, queued, stay, err)
	}
	if err != nil || merged == nil {
		return true, next, err
	}
	t := *b.Train
	t.MergeCommit = merged.MergeCommit
	if _, err := mq.SetMergeBatchTrain(r.batchMutation("merge-batch-train", *b, "merged:"+merged.MergeCommit), r.task.Repository, r.task.ID, t); err != nil {
		return false, Next{}, err
	}
	if _, err := mq.SetMergeBatchState(r.batchMutation("merge-batch-state", *b, string(store.BatchLanded)+":"+merged.MergeCommit), r.task.Repository, r.task.ID, store.BatchLanded, ""); err != nil {
		return false, Next{}, err
	}
	if b, err = r.batchOf(mq); err != nil {
		return false, Next{}, err
	}
	if b == nil || b.State != store.BatchLanded {
		return false, Next{}, fmt.Errorf("the merge batch task %s leads did not record its landing", r.task.ID)
	}
	_, next, err = r.landedMember(s, id, repo, deliverHead{head: h, pr: pr}, *b)
	return true, next, err
}

// batchOf reads the standing batch again.
func (r *run) batchOf(mq MergeQueue) (*store.MergeBatch, error) {
	view, err := mq.MergeQueue(r.task.Repository)
	return view.Batch, err
}

// publishTrain pushes the batch's last link to its train branch, opening the train pull request against the base or
// moving it to the link, records the push as the next <step>-<k>-train-<n>-pr and the train on the batch, and tells
// the Director once; a push already made at the last link returns its record.
func (r *run) publishTrain(s workflow.Step, k int, repo config.Repository, mq MergeQueue, b store.MergeBatch, pr *pullRequestDoc) (*pullRequestDoc, error) {
	last := b.Members[len(b.Members)-1].Link
	branch := TrainBranch(b.Leader, b.Stay)
	var out *pullRequestDoc
	n := 0
	for _, d := range r.documents {
		dk, dn, ok := parseTrainPRName(s.ID, d.Name)
		if !ok {
			continue
		}
		if dk == k {
			n = max(n, dn)
		}
		var doc pullRequestDoc
		if _, err := r.readJSONDoc(d.Name, &doc); err != nil {
			return nil, err
		}
		if doc.Branch == branch && doc.Head == last {
			out = &doc
		}
	}
	if out == nil {
		title, body, err := r.trainText(b, pr)
		if err != nil {
			return nil, err
		}
		pushed, err := r.e.Forge.Publish(r.ctx, forge.Change{Repository: repo.Path, Remote: pr.Remote, Branch: branch, Head: last, Title: title, Body: body})
		if err != nil {
			return nil, err
		}
		name := trainPRName(s.ID, k, n+1)
		out = &pullRequestDoc{Schema: pullRequestSchema, Remote: pr.Remote, Title: title, PullRequest: pushed}
		if err := r.putPullRequest(name, path.Join("tasks", r.task.Repository, r.task.ID, DecisionID(s.ID, k, false), fmt.Sprintf("train-%d", n+1), "pull-request.json"), *out); err != nil {
			return nil, err
		}
		text := fmt.Sprintf("Pushed the merge batch task %s leads (%d tasks) to branch %s at %s, the link of its last member %s: train pull request %s. Its checks run next, and it lands once they pass and every member passes its merge gates.",
			b.Leader, len(b.Members), branch, last, b.Members[len(b.Members)-1].TaskID, pushed.URL)
		if err := r.notify("batch-train."+name, store.MessageMergeQueued, text); err != nil {
			return nil, err
		}
	}
	if t := b.Train; t == nil || t.Number != out.Number || t.Head != out.Head || t.Remote != out.Remote {
		train := store.MergeTrain{Remote: out.Remote, Number: out.Number, URL: out.URL, Head: out.Head}
		if _, err := mq.SetMergeBatchTrain(r.batchMutation("merge-batch-train", b, fmt.Sprintf("%d-%s", out.Number, out.Head)), r.task.Repository, r.task.ID, train); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// trainText is the train pull request's title and body: "Merge train: <title> (#<n>), …", cut to the forge's limit,
// and a table of each member's task, pull request and link head.
func (r *run) trainText(b store.MergeBatch, pr *pullRequestDoc) (string, string, error) {
	_, view, _, err := r.standingQueue()
	if err != nil {
		return "", "", err
	}
	var titles []string
	var rows strings.Builder
	for i, m := range b.Members {
		t, err := r.e.Records.Task(r.task.Repository, m.TaskID)
		if err != nil {
			return "", "", err
		}
		url := ""
		if m.TaskID == r.task.ID {
			url = pr.URL
		} else if j := slices.IndexFunc(view.Entries, func(e store.QueuedEntry) bool { return e.TaskID == m.TaskID }); j >= 0 {
			url = view.Entries[j].PR
		}
		title := strings.Join(strings.Fields(neutralise(cmp.Or(strings.TrimSpace(t.Title), m.TaskID))), " ")
		if n := prNumber(url); n > 0 {
			title += fmt.Sprintf(" (#%d)", n)
		}
		titles = append(titles, title)
		fmt.Fprintf(&rows, "| %d | `%s/%s` | %s | `%s` |\n", i+1, r.task.Repository, m.TaskID, cell(cmp.Or(url, "none recorded")), m.Link)
	}
	title := capRunes("Merge train: "+strings.Join(titles, ", "), forge.MaxTitle)
	body := fmt.Sprintf("Merge train of the merge batch Guildhall task `%s/%s` leads (front stay %d): the last member's link, every member's change replayed in queue order onto the base.\n\n"+
		"| Place | Task | Pull request | Link head |\n|---|---|---|---|\n%s\n"+
		"Guildhall squash-merges this pull request once its checks pass and every member passes its merge gates, and then closes each member's own pull request with a link here.\n",
		r.task.Repository, b.Leader, b.Stay, rows.String())
	return title, body, nil
}

// prNumber is the pull request number a URL ends in, or 0.
func prNumber(url string) int {
	i := strings.LastIndex(url, "/")
	n, err := strconv.Atoi(url[i+1:])
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// redTrain records the batch whose train's checks v failed, after their one rerun, red, and tells the Director that
// each member's own link checks are read next to find the member that broke it (bisectTrain).
func (r *run) redTrain(mq MergeQueue, b store.MergeBatch, train *pullRequestDoc, v *validationDoc) error {
	mutation := r.batchMutation("merge-batch-state", b, string(store.BatchRed)+":"+batchRound(b))
	if _, err := mq.SetMergeBatchState(mutation, r.task.Repository, r.task.ID, store.BatchRed, ""); err != nil {
		return err
	}
	text := fmt.Sprintf("The checks of merge train %s at %s, of the merge batch task %s/%s leads (%d tasks), failed: %s. Each member's own link checks are read next; the first member whose link is red goes back to build, and the members before it land without it.",
		train.URL, train.Head, r.task.Repository, b.Leader, len(b.Members), strings.Join(v.Failing, ", "))
	return r.notify("batch-red."+b.Leader+"."+strconv.Itoa(b.Stay)+"."+batchRound(b), store.MessageMergeQueued, text)
}

// closeTrain closes the batch's train pull request, when it has one, with the comment "Closed by Guildhall: <why>.
// <then>".
func (r *run) closeTrain(b store.MergeBatch, repo config.Repository, why, then string) error {
	t := b.Train
	if t == nil {
		return nil
	}
	comment := fmt.Sprintf("Closed by Guildhall: %s. %s", why, then)
	_, err := r.e.Forge.Close(r.ctx, forge.Close{Repository: repo.Path, Remote: t.Remote, Number: t.Number, Comment: comment})
	if errors.Is(err, forge.ErrRefused) {
		// A train already closed, as after a crash between the close and the batch's end, stays as it is.
		return nil
	}
	return err
}

// endTrain ends the batch the task leads, for why, closing its train pull request when it has one, and tells the
// Director; the leader then merges alone and the members keep their places.
func (r *run) endTrain(mq MergeQueue, b store.MergeBatch, repo config.Repository, why string) error {
	if err := r.closeTrain(b, repo, why, fmt.Sprintf("The merge batch ends, task %s merges alone, and the other members are batched again.", b.Leader)); err != nil {
		return err
	}
	if _, err := mq.LeaveMergeBatch(r.batchMutation("merge-batch-leave", b, digest.Of([]byte(why)).Hex()[:12]), r.task.Repository, r.task.ID); err != nil {
		return err
	}
	text := fmt.Sprintf("The merge batch task %s/%s leads ended: %s. Task %s merges alone, and the other members keep their places in the merge queue.", r.task.Repository, b.Leader, why, b.Leader)
	return r.notify("batch-ended."+b.Leader+"."+strconv.Itoa(b.Stay)+"."+batchRound(b), store.MessageMergeQueued, text)
}

// mergeTrain squash-merges the train pull request at its head, under the authorization id of the leader's own
// merge: a train the forge already reports merged at that head, as after a crash between the merge and its record,
// is read back rather than merged again. A train behind or conflicting with the base, observed before the merge or
// once the forge refused it, is errTrainBehind; any other merge the forge refuses is recorded as id's refusal and
// waits for the Director. It returns nil with the wait when nothing merged.
func (r *run) mergeTrain(s workflow.Step, id string, repo config.Repository, train *pullRequestDoc) (*forge.PullRequest, Next, error) {
	if reader, ok := r.e.Forge.(PullRequestReader); ok {
		got, err := reader.PullRequest(r.ctx, forge.View{Repository: repo.Path, Remote: train.Remote, Number: train.Number})
		if err != nil {
			return nil, Next{}, err
		}
		if got.State == forge.Merged {
			if got.Head != train.Head {
				return nil, Next{}, fmt.Errorf("%w: train pull request %s merged at %s, not its head %s", ErrEvent, train.URL, got.Head, train.Head)
			}
			return &got, Next{}, nil
		}
	}
	behind := func() error {
		m, err := r.observe(repo, train, train.Head)
		if err != nil || !obstructed(m) {
			return err
		}
		return fmt.Errorf("%w: train pull request %s is %s base %s", errTrainBehind, train.URL, m.State, m.Base)
	}
	if err := behind(); err != nil {
		return nil, Next{}, err
	}
	merged, err := r.e.Forge.Merge(r.ctx, forge.Merge{Repository: repo.Path, Remote: train.Remote, Number: train.Number, Head: train.Head, Title: train.Title})
	if errors.Is(err, forge.ErrRefused) {
		if err := behind(); err != nil {
			return nil, Next{}, err
		}
		reason := err.Error()
		if err := r.recordRefusal(id, refusalDoc{Schema: refusalSchema, Remote: train.Remote, Number: train.Number, URL: train.URL, Head: train.Head, Reason: reason}); err != nil {
			return nil, Next{}, err
		}
		decision := ""
		if r.decision(id) != nil {
			decision = id
		}
		return nil, Next{Wait: WaitDirector, Step: s.ID, Decision: decision},
			r.notify(fmt.Sprintf("merge-refused.%s.%s", id, digest.Of([]byte(reason)).Hex()[:12]), store.MessageMergeRefused,
				fmt.Sprintf("The forge refused to merge train pull request %s at head %s: %s\nThe Director decides how to recover; advancing the task again retries the merge.", train.URL, train.Head, reason))
	}
	if err != nil {
		return nil, Next{}, err
	}
	return &merged, Next{}, nil
}

// landedMember records the merge of a member of the landed batch b, merge id, as <id>-merge: the train pull request
// with its merge commit, and the task's own pull request, link, head and authorization; then it finishes
// (trainTail). The leader's id is its own merge's; a member's link landed under <link>-authorized.
func (r *run) landedMember(s workflow.Step, id string, repo config.Repository, dl deliverHead, b store.MergeBatch) (bool, Next, error) {
	me := b.Members[b.Member(r.task.ID)-1]
	own, err := r.chain(dl.head, s.ID)
	if err != nil {
		return false, Next{}, err
	}
	h := chainHead(dl.head, own)
	var authorized string
	if b.Leader == r.task.ID {
		lid, _, err := r.authorizedAs(s, me.Link)
		if err != nil {
			return false, Next{}, err
		}
		id = cmp.Or(lid, id)
		authorized = id + "-authorized"
		if r.decision(id) != nil {
			authorized = "decision " + id
		}
	} else {
		links, err := r.batchLinks(s.ID)
		if err != nil {
			return false, Next{}, err
		}
		i := slices.IndexFunc(links, func(l batchLink) bool {
			return l.head() == me.Link && l.doc.Leader == b.Leader && l.doc.BatchStay == b.Stay
		})
		if i < 0 {
			return false, Next{}, fmt.Errorf("task %s has no batch link at %s in the merge batch task %s led", r.task.ID, me.Link, b.Leader)
		}
		authorized = links[i].name + "-authorized"
	}
	if t := b.Train; t == nil || t.MergeCommit == "" {
		return false, Next{}, fmt.Errorf("the landed merge batch task %s led records no train merge", b.Leader)
	}
	if r.document(id+"-merge") == nil {
		t := b.Train
		var ownPR forge.PullRequest
		if dl.pr != nil {
			ownPR = forge.PullRequest{Number: dl.pr.Number, URL: dl.pr.URL, Branch: dl.pr.Branch}
		}
		doc := pullRequestDoc{Schema: pullRequestSchema, Remote: t.Remote,
			PullRequest: forge.PullRequest{Number: t.Number, URL: t.URL, Branch: TrainBranch(b.Leader, b.Stay), Head: t.Head, State: forge.Merged, MergeCommit: t.MergeCommit},
			Train:       &trainRecord{Leader: b.Leader, Stay: b.Stay, PullRequest: ownPR, Link: me.Link, Head: h, Authorized: authorized}}
		if err := r.putPullRequest(id+"-merge", path.Join("tasks", r.task.Repository, r.task.ID, id, "merge.json"), doc); err != nil {
			return false, Next{}, err
		}
	}
	return r.trainTail(s, id, repo)
}

// trainMerged returns the id of deliver step s's merge recorded from a landed merge train, or "". The leader's may
// be under a later round than the one it queued under, the authorization of its rebased head.
func (r *run) trainMerged(s workflow.Step) (string, error) {
	for i, p := range r.docPlaces {
		if !p.ok || p.step != s.ID || p.part != "merge" || p.exit || p.index != 0 {
			continue
		}
		var m pullRequestDoc
		if _, err := r.readJSONDoc(r.documents[i].Name, &m); err != nil {
			return "", err
		}
		if m.Train != nil {
			return DecisionID(p.step, p.round, false), nil
		}
	}
	return "", nil
}

// trainTail finishes a task whose merge id landed in a merge train, from its <id>-merge alone: it closes the task's
// own pull request with a comment linking the train, leaving one already closed as it is, and runs the merge's tail,
// which closes the task done or its current slice. Every write is keyed on id, so a replay sends nothing twice.
func (r *run) trainTail(s workflow.Step, id string, repo config.Repository) (bool, Next, error) {
	var m pullRequestDoc
	if _, err := r.readJSONDoc(id+"-merge", &m); err != nil {
		return false, Next{}, err
	}
	t := m.Train
	if t.PullRequest.Number > 0 && r.e.Forge != nil {
		comment := fmt.Sprintf("Landed by Guildhall in merge train %s as %s.", m.URL, m.MergeCommit)
		_, err := r.e.Forge.Close(r.ctx, forge.Close{Repository: repo.Path, Remote: m.Remote, Number: t.PullRequest.Number, Comment: comment})
		if err != nil && !errors.Is(err, forge.ErrRefused) {
			return false, Next{}, err
		}
	}
	return r.mergeTail(s, id, repo, t.Head, &pullRequestDoc{Remote: m.Remote, PullRequest: forge.PullRequest{Number: m.Number, URL: m.URL, Head: t.Head}})
}

// authorizeLink runs the merge gates of a member's link l, in the landing batch b, under the authorization from its
// head was queued under: the train's own checks at the head that lands, read as the front's are; the approval
// carried over the replays own and l, under reapprove_after_rebase; nothing_open; the accepted spec; and the policy
// guard on the member's own change, l's onto to its head. Passing records <link>-authorized, schema
// guildhall.merge-authorization/2. It returns the wait while the train's checks are read, or why the link may not
// land, or neither once it is authorized.
func (r *run) authorizeLink(s workflow.Step, repo config.Repository, dl deliverHead, from string, l batchLink, own []rebaseLink, b store.MergeBatch) (*Next, string, error) {
	if r.document(l.name+"-authorized") != nil {
		return nil, "", nil
	}
	p, _ := parseDecision(from)
	t := b.Train
	trainPR := &pullRequestDoc{Schema: pullRequestSchema, Remote: t.Remote, PullRequest: forge.PullRequest{Number: t.Number, URL: t.URL, Head: t.Head}}
	v, next, err := r.headChecks(s, p.round, repo, t.Head, trainPR)
	if err != nil || v == nil {
		return &next, "", err
	}
	if v.Verdict != validationPassed {
		return nil, fmt.Sprintf("the checks of merge train %s at %s are %s", t.URL, t.Head, v.Verdict), nil
	}
	var pr pullRequestDoc
	if ok, err := r.readJSONDoc(l.name+"-pr", &pr); err != nil || !ok {
		return nil, "", cmp.Or(err, fmt.Errorf("batch link %s has no recorded pull request", l.name))
	}
	link := rebaseLink{name: l.name, step: s.ID, k: l.k, n: l.n, doc: l.doc.rebaseDoc, head: l.head(), covered: l.doc.PatchID != "" && l.doc.PatchID == l.doc.FromPatchID}
	links := slices.Concat(own, []rebaseLink{link})
	x := deliverHead{reviewed: dl.reviewed, head: l.head(), pr: &pr, links: slices.Concat(dl.links, links), checks: v, lands: t.Head}
	if r.e.Workspaces != nil {
		// Only the member's own change, from the link before it, counts: a policy change of an earlier member is its own.
		changed, err := r.e.Workspaces.Changes(r.ctx, repo.Path, l.doc.Onto, l.head(), policies.Path)
		if err != nil {
			return nil, "", fmt.Errorf("whether link %s changes %s: %w", l.head(), policies.Path, err)
		}
		if changed {
			required := true
			repo.Merge.Requires.GuildmasterApproval = &required
			x.policies = true
		}
	}
	outcomes, err := r.reviewOutcomes(s)
	if err != nil {
		return nil, "", err
	}
	conditions, err := r.conditions(s, repo.Merge.Requires, outcomes, x, nil)
	if errors.Is(err, ErrReviewGate) || errors.Is(err, ErrValidationGate) {
		return nil, err.Error(), nil
	}
	if err != nil {
		return nil, "", err
	}
	kind := chainKind(links)
	var why []string
	if repo.Merge.Requires.Guildmaster() && repo.Merge.NeedsReapproval(kind) {
		why = append(why, fmt.Sprintf("a %s replay is at or above reapprove_after_rebase = %s", kind, repo.Merge.Reapprove()))
	}
	if spec, err := r.sameSpec(s, from); err != nil {
		return nil, "", err
	} else if !spec {
		why = append(why, "the accepted spec changed since "+from)
	}
	if x.policies {
		gm, err := r.byGuildmaster(from)
		if err != nil {
			return nil, "", err
		}
		if !gm {
			why = append(why, fmt.Sprintf("%s, and Guildhall authorized %s by itself", policyGuard, from))
		}
	}
	findings := 0
	for _, l := range x.links {
		findings += l.findings
	}
	if findings > 0 {
		why = append(why, fmt.Sprintf("a narrowed review approved a resolution with %d finding(s) or suggestion(s)", findings))
	}
	why = append(why, carryRefusals(conditions)...)
	if len(why) > 0 {
		return nil, "its merge gates do not hold for its link " + l.head() + ": " + strings.Join(why, "; "), nil
	}
	return nil, "", r.carry(s, repo, l.name, x, conditions, from, links, kind)
}
