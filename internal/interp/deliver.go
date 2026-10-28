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

// TaskBranch is the branch of task id: the branch its worktree is on and publish pushes to.
func TaskBranch(id string) string { return branchPrefix + id }

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

// auditDecision is the id of a delivered task's audit decision; a step's decisions are always <step>-<round>, so
// it is never one of them. auditOptions are the answers to its first question.
const auditDecision = "audit"

var auditOptions = []string{"yes", "no"}

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
	body := fmt.Sprintf("Guildhall task %s/%s, published by step %s at %s.\n", r.task.Repository, r.task.ID, s.ID, h)
	if f := r.task.Fixes; f != nil {
		// The fix line that rework (C5) and change failure (E2) count from (docs/quality.md).
		body += fmt.Sprintf("\nFixes a defect introduced by task `%s/%s`.\n", f.Repository, f.ID)
	}
	pr, err := r.e.Forge.Publish(r.ctx, forge.Change{
		Repository: repo.Path, Remote: repo.Remote, Branch: branch, Head: h, Title: title, Body: body,
	})
	if err != nil {
		return false, Next{}, err
	}
	id := decisionID(s.ID, k, false)
	if err := r.putPullRequest(id+"-pr", path.Join("tasks", r.task.Repository, r.task.ID, id, "pull-request.json"), pullRequestDoc{Schema: pullRequestSchema, Remote: repo.Remote, PullRequest: pr}); err != nil {
		return false, Next{}, err
	}
	if err := r.notify("published."+id, store.MessagePublished, fmt.Sprintf("Step %s pushed head %s to branch %s: pull request %s.", s.ID, h, branch, pr.URL)); err != nil {
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

// deliver checks the repository's merge requirements (docs/configuration.md#merge-requirements) and merges the
// head. A required artisan_approval or ci_green that does not hold is a hard error, and a behind or conflicting pull
// request waits for the Director, before anything is asked. With guildmaster_approval required, or when nothing_open
// fails, it opens the merge decision bound to the head, whose question lists every requirement; a merge answer
// squash-merges the published pull request at that head and closes the task, and a hold answer waits for the
// Director. Otherwise it records the merge's authorization, <step>-<k>-authorized, and merges without a decision.
// A merge the forge refuses waits for the Director.
func (r *run) deliver(s workflow.Step) (bool, Next, error) {
	repo, err := r.repository()
	if err != nil {
		return false, Next{}, err
	}
	requires := repo.Merge.Requires
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
	outcomes, err := r.reviewOutcomes(s)
	if err != nil {
		return false, Next{}, err
	}
	artisan, err := gateCondition("artisan_approval", requires.Artisan(), ErrReviewGate, r.gate(s, h))
	if err != nil {
		return false, Next{}, err
	}
	for _, o := range outcomes {
		artisan.Evidence = append(artisan.Evidence, o.name)
	}
	ci, err := gateCondition("ci_green", requires.CI(), ErrValidationGate, r.validated(s, h))
	if err != nil {
		return false, Next{}, err
	}
	if refs := s.Inputs["validation"]; len(refs) > 0 {
		step, port, _ := strings.Cut(refs[0], ".")
		if v, _, err := r.output(step, port); err == nil {
			ci.Evidence = []string{v.Name}
		}
	}
	pr, err := r.published(s, h)
	if err != nil {
		return false, Next{}, err
	}
	state, blocked, err := r.mergeBlocked(pr, h)
	if err != nil || blocked {
		return false, Next{Wait: WaitDirector, Step: s.ID}, err
	}
	guildmaster := condition{Requirement: "guildmaster_approval", Required: requires.Guildmaster()}
	if guildmaster.Required {
		guildmaster.Reason = "this decision is not answered yet"
	}
	conditions := []condition{guildmaster, artisan, ci, r.nothingOpen(s, requires.Open(), outcomes)}
	if id, err := r.authorization(s, h); err != nil || id != "" {
		if err != nil {
			return false, Next{}, err
		}
		return r.merge(s, id, repo, h, pr)
	}
	d := r.latestDecision(s.ID)
	switch {
	case d == nil || d.Subject == nil || d.Subject.Digest != h:
		k := r.deliverRound(s)
		var failed []string
		for _, c := range conditions[1:] {
			if c.Required && !c.Holds {
				failed = append(failed, fmt.Sprintf("%s (%s)", c.Requirement, c.Reason))
			}
		}
		if !requires.Guildmaster() && len(failed) == 0 {
			if pr == nil {
				return false, Next{}, fmt.Errorf("%w: merging head %s, which no publish step pushed", ErrUnsupported, h)
			}
			id := decisionID(s.ID, k, false)
			if err := r.authorize(id, authorizationDoc{Schema: authorizationSchema, Head: h, PullRequest: pr, Requires: requires.Effective(),
				Mergeability: state, Conditions: conditions}); err != nil {
				return false, Next{}, err
			}
			return r.merge(s, id, repo, h, pr)
		}
		question := fmt.Sprintf("Merge needed for %s/%s, step %s, attempt %d: head %s.\n", r.task.Repository, r.task.ID, s.ID, k, h)
		if !requires.Guildmaster() {
			question = fmt.Sprintf("Guildhall did not merge by itself: %s.\n", strings.Join(failed, "; ")) + question
		}
		if pr != nil {
			question += fmt.Sprintf("Pull request: %s\n", pr.URL)
		}
		question += "\nMerge requirements of repository " + r.task.Repository + ":\n"
		for _, c := range conditions {
			question += "- " + c.Requirement + ": " + c.line() + "\n"
		}
		question += "A merge answer overrides a failing nothing_open, and nothing else."
		ask := render.Ask{Question: boundedQuestion(question, mergeOptions), Options: mergeOptions}
		next, err := r.open(s, k, inputs, ask)
		return false, next, err
	case !d.Answered:
		return false, Next{Wait: WaitDecision, Step: s.ID, Decision: d.ID}, r.notifyDecision(*d, s.ID)
	case d.Choice == "hold":
		return false, Next{Wait: WaitDirector, Step: s.ID, Decision: d.ID},
			r.notify("merge-held."+d.ID, store.MessageMergeHeld, fmt.Sprintf("The Guildmaster held the merge of head %s (decision %s); the Director decides what happens next.", h, d.ID))
	case d.Choice != "merge":
		return false, Next{}, fmt.Errorf("decision %s: %w: outcome %s", d.ID, ErrUnsupported, d.Choice)
	}
	return r.merge(s, d.ID, repo, h, pr)
}

// authorizationSchema versions the authorization deliver records before a merge Guildhall makes by itself.
const authorizationSchema = "guildhall.merge-authorization/1"

// condition is one merge requirement as deliver found it, with the names of the evidence it read.
type condition struct {
	Requirement string `json:"requirement"`
	Required    bool   `json:"required"`
	Holds       bool   `json:"holds"`
	// Reason says why the requirement does not hold; empty when it holds.
	Reason   string   `json:"reason,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

// line is the condition as the merge question lists it.
func (c condition) line() string {
	switch {
	case !c.Required:
		return "not required"
	case c.Holds:
		return "holds"
	}
	return "fails: " + c.Reason
}

// authorizationDoc is <step>-<k>-authorized: what allowed a merge Guildhall made without a decision. Deliver reads
// the requirements from the current configuration at every advance, so this is the only record of the ones in effect.
type authorizationDoc struct {
	Schema      string          `json:"schema"`
	Head        string          `json:"head"`
	PullRequest *pullRequestDoc `json:"pull_request"`
	// Requires are the effective requirements; Mergeability is the pull request's observed state.
	Requires     config.MergeRequires `json:"requires"`
	Mergeability string               `json:"mergeability"`
	Conditions   []condition          `json:"conditions"`
}

// gateCondition is the condition of a gate requirement from the gate's error: a required gate that fails returns
// its error, so no answer merges over it, and an error that is not the gate's own is returned whatever is required.
func gateCondition(requirement string, required bool, gate, err error) (condition, error) {
	c := condition{Requirement: requirement, Required: required, Holds: err == nil}
	switch {
	case err == nil:
	case !errors.Is(err, gate) || required:
		return condition{}, err
	default:
		c.Reason = strings.TrimPrefix(err.Error(), gate.Error()+": ")
	}
	return c, nil
}

// reviewOutcome is the latest outcome of a review step before deliver, by document name.
type reviewOutcome struct {
	name    string
	outcome *workflow.ReviewOutcome
}

// reviewOutcomes returns the latest outcome of every review and document-review step before deliver that has one.
func (r *run) reviewOutcomes(deliver workflow.Step) ([]reviewOutcome, error) {
	var out []reviewOutcome
	for _, s := range r.def.Steps {
		if s.ID == deliver.ID {
			break
		}
		if !isReview(s) {
			continue
		}
		d, _, err := r.output(s.ID, "outcome")
		if err != nil {
			continue
		}
		data, err := r.read(d)
		if err != nil {
			return nil, err
		}
		o, err := workflow.ParseReviewOutcome(data)
		if err != nil {
			return nil, err
		}
		out = append(out, reviewOutcome{name: d.Name, outcome: o})
	}
	return out, nil
}

// nothingOpen is the nothing_open condition: the review outcomes list no findings, advisory ones included, and the
// task has no unanswered decision but deliver step s's own.
func (r *run) nothingOpen(s workflow.Step, required bool, outcomes []reviewOutcome) condition {
	c := condition{Requirement: "nothing_open", Required: required}
	var open []string
	for _, o := range outcomes {
		c.Evidence = append(c.Evidence, o.name)
		if n := len(o.outcome.Findings); n > 0 {
			open = append(open, fmt.Sprintf("%s lists %d finding(s)", o.name, n))
		}
	}
	for i, d := range r.decisions {
		if p := r.decisionPlaces[i]; d.Answered || (p.ok && p.step == s.ID && !p.exit) {
			continue
		}
		c.Evidence = append(c.Evidence, d.ID)
		open = append(open, fmt.Sprintf("decision %s is unanswered", d.ID))
	}
	c.Holds, c.Reason = len(open) == 0, strings.Join(open, ", ")
	return c
}

// deliverRound is the round deliver step s's next merge opens: after its latest decision and its latest authorization.
func (r *run) deliverRound(s workflow.Step) int {
	k := r.nextRound(s.ID)
	for _, p := range r.docPlaces {
		if p.ok && p.step == s.ID && p.part == "authorized" && !p.exit && p.index == 0 && p.round >= k {
			k = p.round + 1
		}
	}
	return k
}

// authorization returns the id of deliver step s's latest merge Guildhall authorized at head h, or "".
func (r *run) authorization(s workflow.Step, h string) (string, error) {
	id := ""
	for i, p := range r.docPlaces {
		if !p.ok || p.step != s.ID || p.part != "authorized" || p.exit || p.index != 0 {
			continue
		}
		data, err := r.read(r.documents[i])
		if err != nil {
			return "", err
		}
		var a authorizationDoc
		if err := json.Unmarshal(data, &a); err != nil {
			return "", fmt.Errorf("document %s: %w", r.documents[i].Name, err)
		}
		if a.Schema != authorizationSchema {
			return "", fmt.Errorf("document %s is not a %s authorization", r.documents[i].Name, authorizationSchema)
		}
		if a.Head == h {
			id = decisionID(p.step, p.round, false)
		}
	}
	return id, nil
}

// authorize records a, the authorization of merge id, as <id>-authorized.
func (r *run) authorize(id string, a authorizationDoc) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: id + "-authorized",
		Path: path.Join("tasks", r.task.Repository, r.task.ID, id, "authorized.json"), SHA256: digest.Of(data).Hex()}, data)
}

// mergedBy says what allowed merge id: the Guildmaster's merge decision, or Guildhall's recorded authorization.
func (r *run) mergedBy(id string) string {
	if r.decision(id) != nil {
		return "decision " + id
	}
	return "merged by Guildhall, " + id + "-authorized"
}

// merge merges the pull request merge id allows, records it as <id>-merge and closes the task. The id is the merge
// decision's, or <step>-<k> for a merge Guildhall authorized. A merge the forge refuses is recorded and waits for
// the Director.
func (r *run) merge(s workflow.Step, id string, repo config.Repository, h string, pr *pullRequestDoc) (bool, Next, error) {
	decision := ""
	if r.decision(id) != nil {
		decision = id
	}
	name := id + "-merge"
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
			if err := r.recordRefusal(id, refusalDoc{Schema: refusalSchema, Remote: pr.Remote, Number: pr.Number, URL: pr.URL, Head: h, Reason: reason}); err != nil {
				return false, Next{}, err
			}
			return false, Next{Wait: WaitDirector, Step: s.ID, Decision: decision},
				r.notify(fmt.Sprintf("merge-refused.%s.%s", id, digest.Of([]byte(reason)).Hex()[:12]), store.MessageMergeRefused,
					fmt.Sprintf("The forge refused to merge pull request %s at head %s: %s\nThe Director decides how to recover; advancing the task again retries the merge.", pr.URL, h, reason))
		}
		if err != nil {
			return false, Next{}, err
		}
		if err := r.putPullRequest(name, path.Join("tasks", r.task.Repository, r.task.ID, id, "merge.json"), pullRequestDoc{Schema: pullRequestSchema, Remote: pr.Remote, PullRequest: merged}); err != nil {
			return false, Next{}, err
		}
	}
	if err := r.notify("merged."+id, store.MessageMerged, fmt.Sprintf("Merged head %s (%s); the task is done.", h, r.mergedBy(id))); err != nil {
		return false, Next{}, err
	}
	if err := r.retire(id, repo, h); err != nil {
		return false, Next{}, err
	}
	if err := r.audit(s, h, pr); err != nil {
		return false, Next{}, err
	}
	if _, err := r.e.Records.SetTaskState(r.mutation("task-state", string(store.TaskDone)), r.task.Repository, r.task.ID, store.TaskDone); err != nil {
		return false, Next{}, err
	}
	return false, Next{Wait: WaitDone, Step: s.ID}, nil
}

// audit opens the Guildmaster's three-question audit of the task merged at head h, bound to h, when the quality
// settings sample the task (docs/quality.md, G4), and tells the Director; an audit already opened is not opened again.
// It opens before the task closes, so a close that fails replays to the same audit.
func (r *run) audit(s workflow.Step, h string, pr *pullRequestDoc) error {
	q, err := r.e.Config.Quality()
	if err != nil {
		return err
	}
	if !q.Audits(r.task.Repository, r.task.ID) {
		return nil
	}
	d := r.decision(auditDecision)
	if d == nil {
		question := fmt.Sprintf("Audit of delivered task %s/%s, merged at head %s.\n", r.task.Repository, r.task.ID, h)
		if pr != nil {
			question += fmt.Sprintf("Pull request: %s\n", pr.URL)
		}
		question += "\nThree questions, under two minutes:\n" +
			"1. Would you have merged this as is?\n" +
			"2. Was anything asked that should not have been?\n" +
			"3. Was anything decided that should have been asked?\n" +
			fmt.Sprintf("\nAnswer the first with one of: %s; then the other two in your own words after a blank line.\n", strings.Join(auditOptions, ", "))
		nd := store.Decision{Repository: r.task.Repository, TaskID: r.task.ID, ID: auditDecision, Kind: store.DecisionAudit,
			Subject: &store.Subject{Kind: store.SubjectHead, Digest: h}, Options: auditOptions, Question: question}
		if _, err := r.e.Records.OpenDecision(r.mutation("decision", auditDecision), nd); err != nil {
			return err
		}
		d = r.addDecision(nd)
	}
	return r.notifyDecision(*d, s.ID)
}

// recordRefusal records a refusal of merge id as its next refusal document, <id>-refused[-n], unless one of its refusals
// already holds the same content, so a refusal repeated or replayed for the same reason is recorded once.
func (r *run) recordRefusal(id string, doc refusalDoc) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	sum := digest.Of(data).Hex()
	p, _ := parseDecision(id)
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
		Path: path.Join("tasks", r.task.Repository, r.task.ID, id, "refused-"+sum[:12]+".json"), SHA256: sum,
	}, data)
}
