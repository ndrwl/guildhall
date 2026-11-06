package interp

import (
	"cmp"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// errTrainBehind reports a train pull request behind or conflicting with the base: the base moved under it, so the
// batch is assembled again on the new tip (docs/interpreter.md#red-batches).
var errTrainBehind = errors.New("the base moved under the merge train")

// linkVerdict reads the checks of the task's link in the red batch b, its pull request pr at that link, as the
// front's rebased head's are, with their one rerun under flaky_checks, and records the verdict on the batch once.
// It returns nil with the wait while the checks are pending.
func (r *run) linkVerdict(s workflow.Step, k int, repo config.Repository, mq MergeQueue, b store.MergeBatch, link string, pr *pullRequestDoc) (*validationDoc, Next, error) {
	v, next, err := r.headChecks(s, k, repo, link, pr)
	if err != nil || v == nil {
		return nil, next, err
	}
	if b.Members[b.Member(r.task.ID)-1].Verdict == "" {
		verdict := store.VerdictPassed
		if v.Verdict != validationPassed {
			verdict = store.VerdictFailed
		}
		if _, err := mq.SetMergeBatchVerdict(r.batchMutation("merge-batch-verdict", b, link+":"+verdict), r.task.Repository, r.task.ID, link, verdict); err != nil {
			return nil, Next{}, err
		}
	}
	return v, Next{}, nil
}

// linkFailed is why a red link sends its member back: "the checks of batch link <place> (<head>) failed: <checks>".
func linkFailed(place int, link string, v *validationDoc) string {
	why := fmt.Sprintf("the checks of batch link %d (%s) failed: %s", place, link, strings.Join(v.Failing, ", "))
	if len(v.Reruns) > 0 {
		again := make([]string, len(v.Reruns))
		for i, e := range v.Reruns {
			again[i] = e.Check
		}
		why += fmt.Sprintf(" (%s failed again after one rerun)", strings.Join(again, ", "))
	}
	return why
}

// bisectTrain is the leader's advance of its red batch b at its link h, pr its pull request there, under the queue's
// authorization queued. It records its own link's verdict; a red one makes the leader the culprit, which closes the
// train and goes back to build, the members keeping their places. Otherwise it waits until every member recorded a
// verdict and the culprit, the first member whose link is red, left with the members after it. With every link
// green, a train that held exactly these links ends the batch and the leader merges alone (alone); a train that held
// more is re-pointed to the last green link and tested again (neither stop nor alone). A batch shrunk to its leader
// merges alone through leadTrain.
func (r *run) bisectTrain(s workflow.Step, repo config.Repository, mq MergeQueue, b store.MergeBatch, queued, h string, pr *pullRequestDoc) (stop, alone bool, next Next, err error) {
	wait := Next{Wait: WaitMergeQueue, Step: s.ID}
	p, _ := parseDecision(queued)
	k := p.round
	v, next, err := r.linkVerdict(s, k, repo, mq, b, h, pr)
	if err != nil || v == nil {
		return true, false, next, err
	}
	if v.Verdict != validationPassed {
		why := linkFailed(1, h, v)
		if err := r.closeTrain(b, repo, why, fmt.Sprintf("Task %s goes back to build, and the other members keep their places and are batched again.", b.Leader)); err != nil {
			return true, false, Next{}, err
		}
		onto := b.Base
		all, err := r.rebases(s.ID)
		if err != nil {
			return true, false, Next{}, err
		}
		for _, l := range all {
			if l.head == h {
				onto = l.doc.Onto
			}
		}
		next, err := r.escalate(s, k, escalationDoc{Authorization: queued, From: h, Onto: onto, Kind: config.RebaseMajorConflicts, Files: []string{}, Reason: why})
		return true, false, next, err
	}
	nb, err := r.batchOf(mq)
	if err != nil || nb == nil {
		return true, false, wait, err
	}
	for _, m := range nb.Members {
		// A member still reading its link waits; a red one is the culprit, or behind it, and leaves.
		if m.Verdict != store.VerdictPassed {
			return true, false, wait, nil
		}
	}
	if t := nb.Train; t == nil || t.Head == nb.Members[len(nb.Members)-1].Link {
		why := "the checks of merge train " + trainURL(nb) + " failed while every member's own link checks passed"
		return false, true, Next{}, r.endTrain(mq, *nb, repo, why)
	}
	if _, err := mq.SetMergeBatchState(r.batchMutation("merge-batch-state", *nb, string(store.BatchTesting)+":"+batchRound(*nb)), r.task.Repository, r.task.ID, store.BatchTesting, ""); err != nil {
		return true, false, Next{}, err
	}
	var ids []string
	for _, m := range nb.Members {
		ids = append(ids, m.TaskID)
	}
	text := fmt.Sprintf("The merge batch task %s/%s leads is down to its green members %s after its red train %s; its train is moved to the last green link %s and tested again.",
		r.task.Repository, nb.Leader, strings.Join(ids, ", "), trainURL(nb), nb.Members[len(nb.Members)-1].Link)
	return false, false, Next{}, r.notify("batch-bisected."+nb.Leader+"."+strconv.Itoa(nb.Stay)+"."+batchRound(*nb), store.MessageMergeQueued, text)
}

// trainURL is the URL of the batch's train pull request, or "(none)".
func trainURL(b *store.MergeBatch) string {
	if b.Train == nil {
		return "(none)"
	}
	return b.Train.URL
}

// bisectMember is the advance of a member behind the leader of the red batch q.view.Batch, at its link l of its own
// head h, under authorization id. It records its link's verdict; a red link whose predecessors are all green makes
// the member the culprit, which goes back to build through the failed-checks escalation, keeping its ready time, and
// takes the members after it out of the batch. Otherwise it waits in the queue.
func (r *run) bisectMember(s workflow.Step, repo config.Repository, id string, q queue, l batchLink, h string) (Next, error) {
	var pr pullRequestDoc
	if ok, err := r.readJSONDoc(l.name+"-pr", &pr); err != nil || !ok {
		return Next{}, cmp.Or(err, fmt.Errorf("batch link %s has no recorded pull request", l.name))
	}
	p, _ := parseDecision(id)
	v, next, err := r.linkVerdict(s, p.round, repo, q.q, *q.view.Batch, l.head(), &pr)
	if err != nil || v == nil {
		return next, err
	}
	view, err := q.q.MergeQueue(r.task.Repository)
	if err != nil {
		return Next{}, err
	}
	q.view = view
	b := view.Batch
	if v.Verdict == validationPassed || b == nil || b.Member(r.task.ID) == 0 {
		return r.queued(s, q, false)
	}
	place := b.Member(r.task.ID)
	for _, m := range b.Members[:place-1] {
		if m.Verdict != store.VerdictPassed {
			return r.queued(s, q, false)
		}
	}
	why := linkFailed(place, l.head(), v)
	after := "no other member is after it"
	if rest := b.Members[place:]; len(rest) > 0 {
		var ids []string
		for _, m := range rest {
			ids = append(ids, m.TaskID)
		}
		after = "the members after it, " + strings.Join(ids, ", ") + ", leave the batch and keep their places"
	}
	text := fmt.Sprintf("Task %s/%s broke the red merge train %s of the merge batch task %s leads: %s. It goes back to build keeping its ready time, %s, and the %d member(s) before it land without it.",
		r.task.Repository, r.task.ID, trainURL(b), b.Leader, why, after, place-1)
	if err := r.notify("batch-culprit."+b.Leader+"."+strconv.Itoa(b.Stay)+"."+l.name, store.MessageMergeQueued, text); err != nil {
		return Next{}, err
	}
	return r.escalate(s, p.round, escalationDoc{Authorization: id, From: h, Onto: l.doc.Onto, Kind: config.RebaseMajorConflicts, Files: []string{}, Reason: why})
}

// reassemble sends the batch b, whose train the base moved under (cause), back to assembling, so the leader rebases
// onto the new tip and the members replay onto it again and the train is re-pointed. The leader's rebase counts
// against its maxRebaseRounds in this stay, under the queue's authorization queued; once they are spent, the batch
// ends and the leader goes on alone.
func (r *run) reassemble(s workflow.Step, repo config.Repository, mq MergeQueue, b store.MergeBatch, queued string, stay int, cause error) error {
	all, err := r.rebases(s.ID)
	if err != nil {
		return err
	}
	if rebaseRounds(all, queued, stay) >= maxRebaseRounds {
		return r.endTrain(mq, b, repo, fmt.Sprintf("%v, and task %s already rebased %d times in this stay at the front", cause, b.Leader, maxRebaseRounds))
	}
	if _, err := mq.SetMergeBatchState(r.batchMutation("merge-batch-state", b, string(store.BatchAssembling)+":"+batchRound(b)), r.task.Repository, r.task.ID, store.BatchAssembling, ""); err != nil {
		return err
	}
	text := fmt.Sprintf("The merge batch task %s/%s leads is assembled again: %v. Task %s rebases onto the new tip, the members replay onto it, and the train is moved to the new last link.",
		r.task.Repository, b.Leader, cause, b.Leader)
	return r.notify("batch-reassembled."+b.Leader+"."+strconv.Itoa(b.Stay)+"."+batchRound(b), store.MessageMergeQueued, text)
}

// endStuckBatch ends the batch the task leads from the front of q once it cannot rebase again in this stay; the
// leader goes on alone and waits for the Director, and the members keep their places.
func (r *run) endStuckBatch(q queue, repo config.Repository) error {
	if q.q == nil {
		return nil
	}
	b, err := r.batchOf(q.q)
	if err != nil || b == nil || b.Leader != r.task.ID || b.State == store.BatchLanded {
		return err
	}
	why := fmt.Sprintf("the base moved under it and task %s already rebased %d times in this stay at the front", b.Leader, maxRebaseRounds)
	return r.endTrain(q.q, *b, repo, why)
}
