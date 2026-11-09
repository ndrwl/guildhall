package interp

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// A research, plan or build attempt that reports a structured outcome (docs/adapters.md#result-file), already-delivered
// or nothing-to-do, fails with nothing to take. Instead of leaving it to the Director, the engine checks the outcome's
// evidence and asks the Guildmaster, in a close decision bound to the proposed delivery record, whether to close the
// task, or its current slice, or to retry the step (docs/tasks.md#an-attempt-that-reports-it-is-already-delivered).
// Close feeds the same SliceDelivered event the Director's `slice delivered` feeds, so there is one close path; retry
// starts the step's next attempt as a Director retry would. Nothing closes without the Guildmaster's answer.

// closeOptions are the close decision's answers.
var closeOptions = []string{closeChoice, retryChoice}

const (
	closeChoice = "close"
	retryChoice = "retry"
)

// closeProposalSchema versions the close decision's subject, the proposed delivery record.
const closeProposalSchema = "guildhall.close-proposal/1"

// closeProposalPart is the proposal document's part.
const closeProposalPart = "proposal"

// CloseDecision is the id of the close decision attempt n of step opens when its outcome's evidence holds:
// close-<step>-<n>. Its step part is no workflow step, so it is never one of a step's round decisions.
func CloseDecision(step string, n int) string { return DecisionID("close-"+step, n, false) }

// closeProposal is a close decision's subject: the delivery record its close answer records, with the attempt that
// reported it. It holds no time, so the advance that repeats a crashed proposal writes the same bytes.
type closeProposal struct {
	Schema  string `json:"schema"`
	Step    string `json:"step"`
	Attempt int    `json:"attempt"`
	Slice   int    `json:"slice"`
	// Kind is DeliveryNoChange for a nothing-to-do outcome, empty for already-delivered.
	Kind          string                `json:"kind,omitempty"`
	BySlice       int                   `json:"by_slice,omitempty"`
	ByPullRequest *deliveredPullRequest `json:"by_pull_request,omitempty"`
	Reason        string                `json:"reason"`
}

// event is the SliceDelivered event the close answer of decision id feeds.
func (p closeProposal) event(r *run, id string) Event {
	ev := Event{Kind: SliceDelivered, Repository: r.task.Repository, Task: r.task.ID, Slice: p.Slice, BySlice: p.BySlice,
		Reason: p.Reason, NoChange: p.Kind == DeliveryNoChange, Decision: id}
	if p.ByPullRequest != nil {
		ev.PullRequest = p.ByPullRequest.URL
	}
	return ev
}

// closeOrRetry follows failed attempt a of step s when its result reports a structured outcome. With no close
// decision yet, it checks the outcome's evidence: an already-delivered pull request must be of the task's repository
// and merged, as mergedPullRequest reads it, and an earlier slice must have a merge record; a nothing-to-do outcome
// needs none. When it holds, the engine records the proposal, opens the close decision and waits for it (asked). When
// it does not, or the result has no outcome, asked is false and why, when not empty, says why the evidence did not
// hold, for the attempt-failed message. Once answered, close records the delivery through sliceDelivered and closes
// the slice, done reporting a slice before the last closed and the walk goes on with the next, or the task; retry
// starts the step's next attempt with the outcome in its retry context.
func (r *run) closeOrRetry(s workflow.Step, a *store.StageAttempt) (asked, done bool, next Next, why string, err error) {
	id := CloseDecision(s.ID, a.Number)
	d := r.decision(id)
	if d == nil {
		res, rerr := adapter.ReadResult(r.resultFile(s.ID, a.Number), r.identity(s.ID, a.Number))
		if rerr != nil || res.Outcome == nil {
			return false, false, Next{}, "", nil
		}
		p, cerr := r.proposeClose(s, a.Number, res)
		if cerr != nil {
			if r.ctx.Err() != nil {
				return false, false, Next{}, "", r.ctx.Err()
			}
			return false, false, Next{}, strings.TrimPrefix(cerr.Error(), "interp: "), nil
		}
		if d, err = r.openClose(s, id, p); err != nil {
			return false, false, Next{}, "", err
		}
	}
	wait := Next{Wait: WaitDecision, Step: s.ID, Decision: id}
	switch {
	case d.Void:
		return false, false, Next{}, fmt.Sprintf("its close decision %s is void: %s", id, d.VoidReason), nil
	case !d.Answered:
		return true, false, wait, "", r.notifyDecision(*d, s.ID)
	case d.Choice == retryChoice:
		reason := fmt.Sprintf("The Guildmaster answered retry to close decision %s.", id)
		if d.Note != "" {
			reason += " " + d.Note
		}
		r.retry = &Event{Kind: AttemptRetried, Repository: r.task.Repository, Task: r.task.ID, Step: s.ID, Attempt: a.Number, Reason: reason}
		next, err := r.start(s)
		if next.Wait == WaitQueued {
			return true, false, next, "", err
		}
		return true, false, next, "", r.retryLaunched(s.ID, next.Attempt, err)
	case d.Choice != closeChoice:
		return false, false, Next{}, "", fmt.Errorf("close decision %s answered %q", id, d.Choice)
	}
	var p closeProposal
	if ok, err := r.readJSONDoc(id+"-"+closeProposalPart, &p); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("close decision %s has no proposal", id)
		}
		return false, false, Next{}, "", err
	}
	if err := r.sliceDelivered(p.event(r, id)); err != nil {
		return false, false, Next{}, "", err
	}
	if r.slice.final() {
		_, next, err := r.closeDelivered()
		return true, false, next, "", err
	}
	k := r.slice.current
	doc, err := r.delivery(k)
	if err != nil {
		return false, false, Next{}, "", err
	}
	repo, err := r.repository()
	if err != nil {
		return false, false, Next{}, "", err
	}
	if err := r.closeSliceDelivered(k, deliveredID(k), repo, &doc); err != nil {
		return false, false, Next{}, "", err
	}
	// The walk stops here and the advance starts the next slice, as after a slice's merge.
	r.sliceMerged = true
	return true, true, Next{}, "", nil
}

// proposeClose checks the evidence of the outcome attempt n of s reported in res and returns the delivery its close
// would record; an error says why the evidence does not hold. checkResultOutcome already refused an outcome the step
// may not report and a by_slice outside a multi-PR task's slice range.
func (r *run) proposeClose(s workflow.Step, n int, res adapter.Result) (closeProposal, error) {
	o := res.Outcome
	p := closeProposal{Schema: closeProposalSchema, Step: s.ID, Attempt: n, Slice: r.slice.current, Reason: o.Reason}
	if r.currentMerged() {
		return p, fmt.Errorf("slice %d has a merge record, so it closes as merged", r.slice.current)
	}
	switch {
	case o.Kind == adapter.OutcomeNothingToDo:
		p.Kind = DeliveryNoChange
		return p, nil
	case o.BySlice > 0:
		if r.sliceMerge(r.base.Slices.To, o.BySlice) < 0 {
			return p, fmt.Errorf("slice %d has no merge record, so it delivered nothing", o.BySlice)
		}
		p.BySlice = o.BySlice
	default:
		pr, err := r.mergedPullRequest(o.ByPR)
		if err != nil {
			return p, err
		}
		p.ByPullRequest = pr
	}
	p.Reason = strings.TrimSpace(res.Summary)
	if p.Reason == "" {
		p.Reason = fmt.Sprintf("attempt %d of step %s reported it %s", n, s.ID, o.Label())
	}
	return p, nil
}

// openClose records proposal p and opens close decision id bound to it.
func (r *run) openClose(s workflow.Step, id string, p closeProposal) (*store.Decision, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	name := id + "-" + closeProposalPart
	ref := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
		Path: path.Join("tasks", r.task.Repository, r.task.ID, id, closeProposalPart+".json"), SHA256: digest.Of(data).Hex()}
	if r.document(name) == nil {
		if err := r.put(ref, data); err != nil {
			return nil, err
		}
	} else {
		ref = *r.document(name)
	}
	nd := store.Decision{Repository: r.task.Repository, TaskID: r.task.ID, ID: id, Kind: store.DecisionClose,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + ref.SHA256}, Options: closeOptions,
		Question: boundedQuestion(r.closeQuestion(s, p), closeOptions)}
	if _, err := r.e.Records.OpenDecision(r.mutation("decision", id), nd); err != nil {
		return nil, err
	}
	return r.addDecision(nd), nil
}

// closeQuestion is the close decision's question: "Close <task> as delivered by #<n>?", "… by slice <j>?" or
// "Close <task>: no change needed?", a slice before a multi-PR task's last named as its slice, then the evidence
// Guildhall checked, the attempt's reason and what each answer does.
func (r *run) closeQuestion(s workflow.Step, p closeProposal) string {
	what := r.task.ID
	if r.slice.multi() {
		what = fmt.Sprintf("%s of %s", r.slice.of(p.Slice), r.task.ID)
	}
	var b strings.Builder
	switch {
	case p.Kind == DeliveryNoChange:
		fmt.Fprintf(&b, "Close %s: no change needed?\n", what)
		fmt.Fprintf(&b, "Attempt %d of step %s reports the task needs no change: %s\n", p.Attempt, s.ID, p.Reason)
	case p.BySlice > 0:
		fmt.Fprintf(&b, "Close %s as delivered by slice %d?\n", what, p.BySlice)
		fmt.Fprintf(&b, "Attempt %d of step %s reports slice %d already shipped this work, and slice %d has merged.\n", p.Attempt, s.ID, p.BySlice, p.BySlice)
		fmt.Fprintf(&b, "Its summary: %s\n", p.Reason)
	default:
		pr := p.ByPullRequest
		fmt.Fprintf(&b, "Close %s as delivered by #%d?\n", what, pr.Number)
		fmt.Fprintf(&b, "Attempt %d of step %s reports %s already shipped this change; the forge reports it merged at %s.\n", p.Attempt, s.ID, pr.URL, pr.MergeCommit)
		fmt.Fprintf(&b, "Its summary: %s\n", p.Reason)
	}
	after := "the task is done"
	if r.slice.multi() && !r.slice.final() {
		after = fmt.Sprintf("slice %d starts", p.Slice+1)
	}
	fmt.Fprintf(&b, "close: close it with nothing merged for it; %s.\nretry: run step %s again, with this report in its retry context.", after, s.ID)
	return b.String()
}

// voidClose voids attempt n of step's close decision while it is open, with reason, when the Director retries the
// attempt instead; the retry answers it.
func (r *run) voidClose(step string, n int, reason string) error {
	id := CloseDecision(step, n)
	d := r.decision(id)
	if d == nil || !d.Open() {
		return nil
	}
	if _, err := r.e.Records.VoidDecision(r.mutation("void", "retry", id), r.task.Repository, r.task.ID, id, reason); err != nil {
		return err
	}
	d.Void, d.VoidReason = true, reason
	return nil
}
