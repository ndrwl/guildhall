package interp

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// pullRequestSchema versions the pull-request documents publish and deliver record.
const pullRequestSchema = "guildhall.pull-request/1"

// refusalSchema versions the merge refusals deliver records.
const refusalSchema = "guildhall.merge-refusal/1"

// branchPrefix starts every task branch publish pushes: a task's branch is branchPrefix + its id.
const branchPrefix = "guildhall/"

// pullRequestDoc is a recorded pull request: publish's <step>-<round>-pr and deliver's <decision>-merge.
type pullRequestDoc struct {
	Schema string `json:"schema"`
	// Remote is the remote the branch was pushed to; deliver merges there whatever the configuration says later.
	Remote string `json:"remote"`
	forge.PullRequest
}

// refusalDoc is a merge the forge refused at a merge decision: <decision>-refused, then -refused-2 and on.
type refusalDoc struct {
	Schema string `json:"schema"`
	Remote string `json:"remote"`
	Number int    `json:"number"`
	URL    string `json:"url"`
	Head   string `json:"head"`
	// Reason is the forge's reason, as the error it returned.
	Reason string `json:"reason"`
}

// mergeOptions are the answers the merge decision offers.
var mergeOptions = []string{"merge", "hold"}

// repository returns the task's repository configuration.
func (r *run) repository() (config.Repository, error) {
	i := slices.IndexFunc(r.e.Config.Repositories, func(c config.Repository) bool { return c.ID == r.task.Repository })
	if i < 0 {
		return config.Repository{}, fmt.Errorf("repository %s is not configured", r.task.Repository)
	}
	return r.e.Config.Repositories[i], nil
}

// publish pushes the step's head to the task branch through the forge and records its pull request, then the
// head as the step's output. A head already published by the step's latest round completes it unchanged.
func (r *run) publish(s workflow.Step) (bool, Next, error) {
	in, err := r.input(s, "head")
	if err != nil {
		return false, Next{}, err
	}
	h, err := r.head(in)
	if err != nil {
		return false, Next{}, err
	}
	k := 1
	if doc, source, err := r.output(s.ID, "head"); err == nil {
		published, err := r.head(render.Input{Name: doc.Path, SHA256: doc.SHA256})
		if err != nil {
			return false, Next{}, err
		}
		if published == h {
			return true, Next{}, nil
		}
		p, _ := parseDecision(source)
		k = p.round + 1
	}
	repo, err := r.repository()
	if err != nil {
		return false, Next{}, err
	}
	if r.e.Forge == nil {
		return false, Next{}, fmt.Errorf("%w: publishing without a forge", ErrUnsupported)
	}
	title := r.task.Title
	if strings.TrimSpace(title) == "" {
		title = r.task.ID
	}
	branch := branchPrefix + r.task.ID
	pr, err := r.e.Forge.Publish(r.ctx, forge.Change{
		Repository: repo.Path, Remote: repo.Remote, Branch: branch, Head: h, Title: title,
		Body: fmt.Sprintf("Guildhall task %s/%s, published by step %s at %s.\n", r.task.Repository, r.task.ID, s.ID, h),
	})
	if err != nil {
		return false, Next{}, err
	}
	id := decisionID(s.ID, k, false)
	if err := r.putPullRequest(id+"-pr", path.Join("tasks", r.task.Repository, r.task.ID, id, "pull-request.json"), pullRequestDoc{Schema: pullRequestSchema, Remote: repo.Remote, PullRequest: pr}); err != nil {
		return false, Next{}, err
	}
	if err := r.notify("published."+id, "published", fmt.Sprintf("Step %s pushed head %s to branch %s: pull request %s.", s.ID, h, branch, pr.URL)); err != nil {
		return false, Next{}, err
	}
	data := []byte(h + "\n")
	doc := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: id + "-head", Path: path.Join("tasks", r.task.Repository, r.task.ID, id, "head"), SHA256: digest.Of(data).Hex()}
	return true, Next{}, r.put(doc, data)
}

func (r *run) putPullRequest(name, p string, d pullRequestDoc) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: p, SHA256: digest.Of(data).Hex()}, data)
}

// published returns the pull request of the publish step deliver's head comes from, or nil when it comes from
// another kind of step.
func (r *run) published(s workflow.Step, h string) (*pullRequestDoc, error) {
	from, _, _ := strings.Cut(s.Inputs["head"][0], ".")
	i := slices.IndexFunc(r.def.Steps, func(p workflow.Step) bool { return p.ID == from })
	if i < 0 || r.def.Steps[i].Type != "publish" {
		return nil, nil
	}
	_, source, err := r.output(from, "head")
	if err != nil {
		return nil, err
	}
	ref := r.document(source + "-pr")
	if ref == nil {
		return nil, fmt.Errorf("%s has no recorded pull request", source)
	}
	data, err := r.read(*ref)
	if err != nil {
		return nil, err
	}
	var pr pullRequestDoc
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, fmt.Errorf("document %s: %w", ref.Name, err)
	}
	if pr.Schema != pullRequestSchema || pr.Head != h || pr.Number < 1 {
		return nil, fmt.Errorf("document %s is not a %s pull request at head %s", ref.Name, pullRequestSchema, h)
	}
	return &pr, nil
}

// deliver checks the review and validation gates and, under the approval policy, opens the merge decision bound to the head.
// A merge answer squash-merges the published pull request at that head and closes the task; a hold answer, or a
// merge the forge refuses, waits for the Director. The auto policy is not supported.
func (r *run) deliver(s workflow.Step) (bool, Next, error) {
	repo, err := r.repository()
	if err != nil {
		return false, Next{}, err
	}
	if repo.MergePolicy != config.MergeApproval {
		return false, Next{}, fmt.Errorf("%w: merge policy %q", ErrUnsupported, repo.MergePolicy)
	}
	inputs, err := r.inputs(s)
	if err != nil {
		return false, Next{}, err
	}
	in, err := r.input(s, "head")
	if err != nil {
		return false, Next{}, err
	}
	h, err := r.head(in)
	if err != nil {
		return false, Next{}, err
	}
	if err := r.gate(s, h); err != nil {
		return false, Next{}, err
	}
	if err := r.validated(s, h); err != nil {
		return false, Next{}, err
	}
	pr, err := r.published(s, h)
	if err != nil {
		return false, Next{}, err
	}
	d := r.latestDecision(s.ID)
	switch {
	case d == nil || d.Subject == nil || d.Subject.Digest != h:
		k := r.nextRound(s.ID)
		question := fmt.Sprintf("Merge needed for %s/%s, step %s, attempt %d: head %s.\n", r.task.Repository, r.task.ID, s.ID, k, h)
		if pr != nil {
			question += fmt.Sprintf("Pull request: %s\n", pr.URL)
		}
		ask := render.Ask{Question: question + fmt.Sprintf("\nAnswer with one of: %s.\n", strings.Join(mergeOptions, ", ")), Options: mergeOptions}
		next, err := r.open(s, k, inputs, ask)
		return false, next, err
	case !d.Answered:
		return false, Next{Wait: WaitDecision, Step: s.ID, Decision: d.ID}, r.notifyDecision(*d, s.ID)
	case d.Choice == "hold":
		return false, Next{Wait: WaitDirector, Step: s.ID, Decision: d.ID},
			r.notify("merge-held."+d.ID, "merge-held", fmt.Sprintf("The Guildmaster held the merge of head %s (decision %s); the Director decides what happens next.", h, d.ID))
	case d.Choice != "merge":
		return false, Next{}, fmt.Errorf("decision %s: %w: outcome %s", d.ID, ErrUnsupported, d.Choice)
	}
	return r.merge(s, *d, repo, h, pr)
}

// merge merges the pull request the merge decision d approved, records it as <decision>-merge and closes the task.
// A merge the forge refuses is recorded and waits for the Director.
func (r *run) merge(s workflow.Step, d store.Decision, repo config.Repository, h string, pr *pullRequestDoc) (bool, Next, error) {
	name := d.ID + "-merge"
	if r.document(name) == nil {
		switch {
		case pr == nil:
			return false, Next{}, fmt.Errorf("%w: merging head %s, which no publish step pushed", ErrUnsupported, h)
		case r.e.Forge == nil:
			return false, Next{}, fmt.Errorf("%w: merging without a forge", ErrUnsupported)
		}
		merged, err := r.e.Forge.Merge(r.ctx, forge.Merge{Repository: repo.Path, Remote: pr.Remote, Number: pr.Number, Head: h})
		if errors.Is(err, forge.ErrRefused) {
			reason := err.Error()
			if err := r.recordRefusal(d, refusalDoc{Schema: refusalSchema, Remote: pr.Remote, Number: pr.Number, URL: pr.URL, Head: h, Reason: reason}); err != nil {
				return false, Next{}, err
			}
			return false, Next{Wait: WaitDirector, Step: s.ID, Decision: d.ID},
				r.notify(fmt.Sprintf("merge-refused.%s.%s", d.ID, digest.Of([]byte(reason)).Hex()[:12]), "merge-refused",
					fmt.Sprintf("The forge refused to merge pull request %s at head %s: %s\nThe Director decides how to recover; advancing the task again retries the merge.", pr.URL, h, reason))
		}
		if err != nil {
			return false, Next{}, err
		}
		if err := r.putPullRequest(name, path.Join("tasks", r.task.Repository, r.task.ID, d.ID, "merge.json"), pullRequestDoc{Schema: pullRequestSchema, Remote: pr.Remote, PullRequest: merged}); err != nil {
			return false, Next{}, err
		}
	}
	if err := r.notify("merged."+d.ID, "merged", fmt.Sprintf("Merged head %s (decision %s); the task is done.", h, d.ID)); err != nil {
		return false, Next{}, err
	}
	if err := r.retire(d, repo, h); err != nil {
		return false, Next{}, err
	}
	if _, err := r.e.Records.SetTaskState(r.mutation("task-state", string(store.TaskDone)), r.task.Repository, r.task.ID, store.TaskDone); err != nil {
		return false, Next{}, err
	}
	return false, Next{Wait: WaitDone, Step: s.ID}, nil
}

// recordRefusal records a refusal of the merge decision d as its next refusal document, unless one of its refusals
// already holds the same content, so a refusal repeated or replayed for the same reason is recorded once.
func (r *run) recordRefusal(d store.Decision, doc refusalDoc) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	sum := digest.Of(data).Hex()
	p, _ := parseDecision(d.ID)
	p.part = "refused"
	for ; ; p.index = max(2, p.index+1) {
		ref := r.document(documentName(p))
		if ref == nil {
			break
		}
		if ref.SHA256 == sum {
			return nil
		}
	}
	return r.put(store.DocumentRef{
		Repository: r.task.Repository, TaskID: r.task.ID, Name: documentName(p),
		Path: path.Join("tasks", r.task.Repository, r.task.ID, d.ID, "refused-"+sum[:12]+".json"), SHA256: sum,
	}, data)
}
