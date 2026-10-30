package interp

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// PullRequestReader reads one pull request as the forge reports it now, changing nothing; it is optional on the forge,
// and the outside-merge rule reads a task's recorded pull requests through it (docs/tasks.md#merged-outside-guildhall).
// forge.GitHub and forgetest.Fake implement it; a forge without it never closes a task by the rule.
type PullRequestReader interface {
	PullRequest(ctx context.Context, v forge.View) (forge.PullRequest, error)
}

var _ PullRequestReader = forge.GitHub{}

// outsidePrefix starts the id of a merge made outside Guildhall, outside-<number>, whose pull request the rule records
// as outside-<number>-merge. A step's ids are always <step>-<round>, and no workflow step is named outside.
const outsidePrefix = "outside-"

// outsideID is the id of the outside merge of pull request number.
func outsideID(number int) string { return outsidePrefix + strconv.Itoa(number) }

// Reconciled is what Engine.Reconcile found.
type Reconciled struct {
	// Done reports that a recorded pull request was merged outside Guildhall, and the task is now done.
	Done bool
	// URL is the merged pull request's.
	URL string
}

// Reconcile reads the forge now for each pull request open task repository/task recorded and applies the
// outside-merge rule, as an advance does before its steps: a pull request the forge reports merged, at any head,
// records the task done. It is `guildhall task reconcile`'s and `task cancel`'s entry point; both stop the task's
// running attempts first, and a task with a running attempt is refused. A task none of whose pull requests merged is
// left unchanged. A closed task is ErrTaskClosed, and a forge that cannot read pull requests ErrUnsupported.
func (e *Engine) Reconcile(ctx context.Context, repository, task string) (Reconciled, error) {
	r, err := e.begin(ctx, Event{Kind: TaskCreated, Repository: repository, Task: task})
	if err != nil {
		return Reconciled{}, err
	}
	if _, ok := e.Forge.(PullRequestReader); !ok {
		return Reconciled{}, fmt.Errorf("interp: %w: the forge does not read pull requests", ErrUnsupported)
	}
	if i := slices.IndexFunc(r.attempts, func(a store.StageAttempt) bool { return a.Outcome == "" }); i >= 0 {
		return Reconciled{}, fmt.Errorf("interp: %s/%s: attempt %d of step %s is running", repository, task, r.attempts[i].Number, r.attempts[i].Step)
	}
	merged, _, err := r.mergedOutside()
	if err != nil || !merged {
		return Reconciled{}, err
	}
	doc, err := r.outsideMerge()
	if err != nil {
		return Reconciled{}, err
	}
	return Reconciled{Done: true, URL: doc.URL}, nil
}

// running reports whether one of the task's attempts is still running, which defers the outside-merge rule to the
// first advance after it finishes.
func (r *run) running() bool {
	return slices.ContainsFunc(r.attempts, func(a store.StageAttempt) bool { return a.Outcome == "" })
}

// outsideMerge returns the pull request an earlier run of the outside-merge rule recorded, or nil.
func (r *run) outsideMerge() (*pullRequestDoc, error) {
	for _, d := range r.documents {
		if !strings.HasPrefix(d.Name, outsidePrefix) || !strings.HasSuffix(d.Name, "-merge") {
			continue
		}
		var pr pullRequestDoc
		if _, err := r.readJSONDoc(d.Name, &pr); err != nil {
			return nil, err
		}
		return &pr, nil
	}
	return nil, nil
}

// mergedOutside is the outside-merge rule (docs/tasks.md#merged-outside-guildhall). A task deliver has not merged, with
// no -merge document, one of whose recorded pull requests the forge now reports merged, at any head, landed outside
// Guildhall: the rule records that pull request as outside-<number>-merge, voids the task's open decisions, sends the
// Director a merged message, retires the worktree at the forge's head, opens the sampled audit, takes the task out of
// the merge queue, releasing its front and clearing its merge hold, and closes it done. A pull request only closed
// never counts. Every write's id is keyed on the pull request, so a run that fails part-way is finished by the next,
// from the recorded pull request alone. Without a reader on the forge, or with no pull request merged, it does nothing.
func (r *run) mergedOutside() (bool, Next, error) {
	pr, err := r.outsideMerge()
	if err != nil {
		return false, Next{}, err
	}
	if pr == nil {
		if pr, err = r.readMergedOutside(); err != nil || pr == nil {
			return false, Next{}, err
		}
	}
	next, err := r.closeMergedOutside(pr)
	return err == nil, next, err
}

// readMergedOutside reads each pull request the task's -pr documents record from the forge, when deliver recorded no
// merge, and records the first the forge reports merged as outside-<number>-merge; it returns nil when none merged.
func (r *run) readMergedOutside() (*pullRequestDoc, error) {
	reader, ok := r.e.Forge.(PullRequestReader)
	if !ok {
		return nil, nil
	}
	prs, merging, err := TaskPullRequests(r.documents, r.e.DocumentDir)
	if err != nil || merging || len(prs) == 0 {
		return nil, err
	}
	repo, err := r.repository()
	if err != nil {
		return nil, err
	}
	for _, rec := range prs {
		got, err := reader.PullRequest(r.ctx, forge.View{Repository: repo.Path, Remote: rec.Remote, Number: rec.Number})
		if err != nil {
			return nil, err
		}
		if got.State != forge.Merged {
			continue
		}
		if own, err := r.authorizedMerge(rec.Remote, got); err != nil || own {
			if err != nil {
				return nil, err
			}
			continue
		}
		id := outsideID(rec.Number)
		pr := pullRequestDoc{Schema: pullRequestSchema, Remote: rec.Remote, PullRequest: got}
		if err := r.putPullRequest(id+"-merge", path.Join("tasks", r.task.Repository, r.task.ID, id, "merge.json"), pr); err != nil {
			return nil, err
		}
		return &pr, nil
	}
	return nil, nil
}

// authorizedMerge reports whether pull request pr of remote merged at the head of a deliver step's latest merge, a
// merge decision answered merge or a recorded authorization, on a pull request recorded at that head, when that merge
// was not rebased at the front. That is deliver's own merge whose record a crash lost, and deliver's replay records
// it, so the rule leaves it alone. A pull request merged at a head deliver no longer merges is not deliver's.
func (r *run) authorizedMerge(remote string, pr forge.PullRequest) (bool, error) {
	// A held merge is never deliver's: it merges nothing until the hold lifts.
	if mq, ok := r.e.Records.(MergeQueue); ok {
		q, err := mq.MergeQueue(r.task.Repository)
		if err != nil || q.Holds[r.task.ID] {
			return false, err
		}
	}
	for _, s := range r.def.Steps {
		if s.Type != "deliver" {
			continue
		}
		round, h := 0, ""
		for i, d := range r.decisions {
			p := r.decisionPlaces[i]
			if p.ok && p.step == s.ID && !p.exit && p.round > round && d.Kind == store.DecisionMerge && d.Answered && d.Choice == "merge" && d.Subject != nil {
				round, h = p.round, d.Subject.Digest
			}
		}
		for i, p := range r.docPlaces {
			if !p.ok || p.step != s.ID || p.exit || p.part != "authorized" || p.index != 0 || p.round <= round {
				continue
			}
			var a authorizationDoc
			if _, err := r.readJSONDoc(r.documents[i].Name, &a); err != nil {
				return false, err
			}
			round, h = p.round, a.Head
		}
		// A merge rebased at the front merges its rebased head, under a later authorization, never h.
		rebased := slices.ContainsFunc(r.documents, func(d store.DocumentRef) bool {
			return strings.HasPrefix(d.Name, decisionID(s.ID, round, false)+"-rebase-")
		})
		if h != "" && h == pr.Head && !rebased {
			return r.recordedAt(remote, pr.Number, h)
		}
	}
	return false, nil
}

// recordedAt reports whether a -pr document recorded pull request number of remote at head h.
func (r *run) recordedAt(remote string, number int, h string) (bool, error) {
	for _, d := range r.documents {
		if !strings.HasSuffix(d.Name, "-pr") {
			continue
		}
		var doc pullRequestDoc
		if _, err := r.readJSONDoc(d.Name, &doc); err != nil {
			return false, err
		}
		if doc.Remote == remote && doc.Number == number && doc.Head == h {
			return true, nil
		}
	}
	return false, nil
}

// closeMergedOutside closes the task whose pull request pr merged outside Guildhall, after its record.
func (r *run) closeMergedOutside(pr *pullRequestDoc) (Next, error) {
	id := outsideID(pr.Number)
	// The writes' mutation ids are keyed on the pull request's remote, as a digest, and number.
	key := id + ":" + digest.Of([]byte(pr.Remote)).Hex()[:12]
	repo, err := r.repository()
	if err != nil {
		return Next{}, err
	}
	// The audit is opened below, and a replay after it must not void it.
	for _, d := range r.decisions {
		if !d.Open() || d.ID == auditDecision {
			continue
		}
		reason := fmt.Sprintf("Task done: pull request %s merged outside Guildhall", pr.URL)
		if _, err := r.e.Records.VoidDecision(r.mutation("void", key, d.ID), r.task.Repository, r.task.ID, d.ID, reason); err != nil {
			return Next{}, err
		}
	}
	recorded := "none"
	prs, _, err := TaskPullRequests(r.documents, r.e.DocumentDir)
	if err != nil {
		return Next{}, err
	}
	if i := slices.IndexFunc(prs, func(p RecordedPullRequest) bool { return p.Remote == pr.Remote && p.Number == pr.Number }); i >= 0 {
		recorded = prs[i].Head
	}
	text := fmt.Sprintf("Pull request %s was merged outside Guildhall at head %s, merge commit %s; Guildhall last recorded head %s for it. The task is done.",
		pr.URL, pr.Head, pr.MergeCommit, recorded)
	if err := r.notify("merged.outside."+strconv.Itoa(pr.Number), store.MessageMerged, text); err != nil {
		return Next{}, err
	}
	if err := r.retire(id, repo, pr.Head); err != nil {
		return Next{}, err
	}
	s := r.closingStep()
	if err := r.audit(s, pr.Head, pr); err != nil {
		return Next{}, err
	}
	if err := r.leave("merged:"+id, false); err != nil {
		return Next{}, err
	}
	if mq, ok := r.e.Records.(MergeQueue); ok {
		q, err := mq.MergeQueue(r.task.Repository)
		if err != nil {
			return Next{}, err
		}
		if q.Holds[r.task.ID] {
			if _, err := mq.SetMergeHold(r.mutation("merge-hold", key), r.task.Repository, r.task.ID, false); err != nil {
				return Next{}, err
			}
		}
	}
	if _, err := r.e.Records.SetTaskState(r.mutation("task-state", string(store.TaskDone)), r.task.Repository, r.task.ID, store.TaskDone); err != nil {
		return Next{}, err
	}
	return Next{Wait: WaitDone, Step: s.ID}, nil
}

// closingStep is the step a task merged outside Guildhall closes at: its deliver step, else its last.
func (r *run) closingStep() workflow.Step {
	if i := slices.IndexFunc(r.def.Steps, func(s workflow.Step) bool { return s.Type == "deliver" }); i >= 0 {
		return r.def.Steps[i]
	}
	return r.def.Steps[len(r.def.Steps)-1]
}
