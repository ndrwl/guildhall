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

// deliver checks the repository's merge requirements (docs/configuration.md#merge-requirements), authorizes the
// merge, queues it and merges the head at the front of the repository's merge queue (docs/interpreter.md#the-merge-queue).
// A required artisan_approval or ci_green that does not hold is a hard error. With guildmaster_approval required, or
// when nothing_open fails, it opens the merge decision bound to the head, whose question lists every requirement,
// whether or not the pull request is behind or conflicting; a hold answer waits for the Director. Otherwise it
// records the merge's authorization, <step>-<k>-authorized; so it does too when, without guildmaster_approval, every
// requirement comes to hold while the merge decision it opened is unanswered, such as after the Director rules on a
// disputed finding, and the authorization records the decision it supersedes. An authorized merge joins the queue,
// and only the front is brought up to date and merged; a merge the forge refuses waits for the Director.
func (r *run) deliver(s workflow.Step) (bool, Next, error) {
	repo, err := r.repository()
	if err != nil {
		return false, Next{}, err
	}
	requires := repo.Merge.Requires
	// The inputs are resolved for the spec gate on any accepted spec deliver takes.
	if _, err := r.inputs(s); err != nil {
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
	pr, err := r.published(s, h)
	if err != nil {
		return false, Next{}, err
	}
	// The delivered head d is the published head, or the head validate rebased it to (docs/interpreter.md#validate).
	vlinks, err := r.chain(h, r.validateSteps(s)...)
	if err != nil {
		return false, Next{}, err
	}
	d := chainHead(h, vlinks)
	if d != h {
		if pr, err = r.linkPR(vlinks, d); err != nil {
			return false, Next{}, err
		}
	}
	dl := deliverHead{reviewed: h, head: d, pr: pr, links: vlinks}
	conditions, err := r.conditions(s, requires, outcomes, dl, nil)
	if err != nil {
		return false, Next{}, err
	}
	id, record, err := r.authorizedAs(s, d)
	if err != nil {
		return false, Next{}, err
	}
	if id == "" {
		var next Next
		id, record, next, err = r.authorizeHead(s, repo, dl, conditions, outcomes)
		if err != nil || id == "" {
			return false, next, err
		}
	}
	return r.front(s, repo, dl, id, record, outcomes)
}

// deliverHead is a head deliver may merge: the reviewed head, the head itself, its pull request and the rebases
// from the reviewed head to it.
type deliverHead struct {
	reviewed, head string
	pr             *pullRequestDoc
	links          []rebaseLink
	// checks is the recorded read of a rebased head's checks; nil for the delivered head, whose validation is the
	// validation input.
	checks *validationDoc
}

// conditions are the merge requirements as they stand for x: guildmaster_approval, artisan_approval, ci_green and
// nothing_open. A head rebased from the reviewed one is approved by the reviewed head's approval and the coverage of
// its rebases, and is green by the read of its own checks. A required gate that fails is the gate's error.
func (r *run) conditions(s workflow.Step, requires config.MergeRequires, outcomes []reviewOutcome, x deliverHead, links []rebaseLink) ([]condition, error) {
	review := r.gate(s, x.reviewed)
	if review == nil {
		review = covered(append(slices.Clone(x.links), links...), x.head)
	}
	artisan, err := gateCondition("artisan_approval", requires.Artisan(), ErrReviewGate, review)
	if err != nil {
		return nil, err
	}
	for _, o := range outcomes {
		artisan.Evidence = append(artisan.Evidence, o.name)
	}
	for _, l := range append(slices.Clone(x.links), links...) {
		artisan.Evidence = append(artisan.Evidence, l.name)
	}
	var green error
	if x.checks != nil {
		if x.checks.Verdict != validationPassed || x.checks.Head != x.head {
			green = fmt.Errorf("%w: the checks of rebased head %s are %s", ErrValidationGate, x.head, x.checks.Verdict)
		}
	} else {
		green = r.validated(s, x.head)
	}
	ci, err := gateCondition("ci_green", requires.CI(), ErrValidationGate, green)
	if err != nil {
		return nil, err
	}
	if x.checks != nil {
		ci.Evidence = []string{fmt.Sprintf("checks of %s", x.head)}
	} else if refs := s.Inputs["validation"]; len(refs) > 0 {
		step, port, _ := strings.Cut(refs[0], ".")
		if v, _, err := r.output(step, port); err == nil {
			ci.Evidence = []string{v.Name}
		}
	}
	guildmaster := condition{Requirement: "guildmaster_approval", Required: requires.Guildmaster()}
	if guildmaster.Required {
		guildmaster.Reason = "this decision is not answered yet"
	}
	return []condition{guildmaster, artisan, ci, r.nothingOpen(s, requires.Open(), outcomes)}, nil
}

// carryRefusals lists every condition after guildmaster_approval that does not hold, required or not: a carried-over
// approval needs them all, nothing_open with no new findings included.
func carryRefusals(conditions []condition) []string {
	var out []string
	for _, c := range conditions[1:] {
		if !c.Holds {
			out = append(out, fmt.Sprintf("%s does not hold (%s)", c.Requirement, c.Reason))
		}
	}
	return out
}

// failing lists the required conditions after guildmaster_approval that do not hold.
func failing(conditions []condition) []string {
	var out []string
	for _, c := range conditions[1:] {
		if c.Required && !c.Holds {
			out = append(out, fmt.Sprintf("%s (%s)", c.Requirement, c.Reason))
		}
	}
	return out
}

// authorizedAs returns the latest authorization of deliver step s's merge of head x, the id of an answered merge
// decision bound to x or of a recorded authorization of x, with the record of the receipt that made it.
func (r *run) authorizedAs(s workflow.Step, x string) (id, record string, err error) {
	if id, err = r.authorization(s, x); err != nil || id != "" {
		return id, "document/" + id + "-authorized", err
	}
	if d := r.decisionOn(s, x); d != nil && d.Answered && d.Choice == "merge" {
		return d.ID, "decision/" + d.ID, nil
	}
	return "", "", nil
}

// authorizeHead authorizes the merge of the delivered head, or waits: with guildmaster_approval required, or a
// requirement failing, the merge decision is asked; otherwise Guildhall records the authorization itself, also while
// a merge decision it asked when a requirement failed is unanswered, which the authorization then supersedes. Under
// reapprove_after_rebase = never, a head rebuilt after a build send-back of an authorized head carries that
// authorization over. It returns the authorization's id and receipt record, or "" and the wait.
func (r *run) authorizeHead(s workflow.Step, repo config.Repository, x deliverHead, conditions []condition, outcomes []reviewOutcome) (string, string, Next, error) {
	requires := repo.Merge.Requires
	failed := failing(conditions)
	unattended := !requires.Guildmaster() && len(failed) == 0
	dec := r.decisionOn(s, x.head)
	switch {
	case dec == nil || !dec.Answered && unattended:
		k := r.deliverRound(s)
		id := decisionID(s.ID, k, false)
		lead := ""
		if requires.Guildmaster() && repo.Merge.Reapprove() == config.RebaseNever {
			e, ok, err := r.carryFrom(s)
			if err != nil {
				return "", "", Next{}, err
			}
			if ok {
				// The carry-over holds only as reauthorize's does: every condition holds, nothing_open included even
				// when it is not required, and the accepted spec is the one the approval was given under.
				why := carryRefusals(conditions)
				if spec := r.specDigest(s); e.SpecDigest == "" || spec != e.SpecDigest {
					why = append(why, fmt.Sprintf("the accepted spec %s is not %s, the one %s was approved under", cmp.Or(spec, "(none)"), cmp.Or(e.SpecDigest, "(none recorded)"), e.Authorization))
				}
				if len(why) == 0 {
					if err := r.carry(s, id, x, conditions, e.Authorization, nil, config.RebaseMajorConflicts); err != nil {
						return "", "", Next{}, err
					}
					return id, "document/" + id + "-authorized", Next{}, nil
				}
				lead = fmt.Sprintf("Head %s was rebuilt after a major_conflicts rebase of the head merge %s authorized; under reapprove_after_rebase = never the authorization would carry over, but it does not: %s.\n",
					x.head, e.Authorization, strings.Join(why, "; "))
			}
		}
		if unattended {
			if x.pr == nil {
				return "", "", Next{}, fmt.Errorf("%w: merging head %s, which no publish step pushed", ErrUnsupported, x.head)
			}
			m, err := r.observe(repo, x.pr, x.head)
			if err != nil {
				return "", "", Next{}, err
			}
			a := authorizationDoc{Schema: authorizationSchema, Head: x.head, PullRequest: x.pr, Requires: requires.Effective(),
				Mergeability: m.State, Conditions: conditions, SpecDigest: r.specDigest(s)}
			if dec != nil {
				a.Supersedes = dec.ID
			}
			if err := r.authorize(id, a); err != nil {
				return "", "", Next{}, err
			}
			return id, "document/" + id + "-authorized", Next{}, nil
		}
		if !requires.Guildmaster() {
			lead = fmt.Sprintf("Guildhall did not merge by itself: %s.\n", strings.Join(failed, "; "))
		}
		next, err := r.openMerge(s, k, x.head, r.mergeQuestion(s, k, x.head, x.pr, conditions, outcomes, lead))
		return "", "", next, err
	case !dec.Answered:
		return "", "", Next{Wait: WaitDecision, Step: s.ID, Decision: dec.ID}, r.notifyDecision(*dec, s.ID)
	case dec.Choice == "hold":
		return "", "", Next{Wait: WaitDirector, Step: s.ID, Decision: dec.ID},
			r.notify("merge-held."+dec.ID, store.MessageMergeHeld, fmt.Sprintf("The Guildmaster held the merge of head %s (decision %s); the Director decides what happens next.", x.head, dec.ID))
	case dec.Choice != "merge":
		return "", "", Next{}, fmt.Errorf("decision %s: %w: outcome %s", dec.ID, ErrUnsupported, dec.Choice)
	}
	return dec.ID, "decision/" + dec.ID, Next{}, nil
}

// front queues the merge authorized as id and, at the front of the queue, brings the head up to date and merges it:
// a clear pull request merges; a behind or conflicting one is rebased onto the base tip mergeability measured, its
// rebased head pushed to the same pull request and its checks read, and then merged under the authorization carried
// over, or a new decision when reapprove_after_rebase asks for one. A rebase with major conflicts, or red checks on
// the rebased head, sends the build back; three rebases in one stay at the front wait for the Director.
func (r *run) front(s workflow.Step, repo config.Repository, dl deliverHead, id, record string, outcomes []reviewOutcome) (bool, Next, error) {
	q, err := r.enqueue(id, record, dl.pr)
	if err != nil {
		return false, Next{}, err
	}
	if q.view.Holds[r.task.ID] {
		if q.front {
			if _, err := q.q.ReleaseMergeFront(r.mutation("merge-release", strconv.Itoa(q.stay)), r.task.Repository, r.task.ID); err != nil {
				return false, Next{}, err
			}
		}
		next, err := r.queued(s, q, true)
		return false, next, err
	}
	if err := r.claim(&q); err != nil {
		return false, Next{}, err
	}
	if !q.front {
		next, err := r.queued(s, q, false)
		return false, next, err
	}
	p, _ := parseDecision(id)
	k := p.round
	for {
		links, err := r.chain(dl.head, s.ID)
		if err != nil {
			return false, Next{}, err
		}
		cur := dl
		cur.head, cur.links = chainHead(dl.head, links), slices.Concat(dl.links, links)
		if e, ok, err := r.pendingEscalation(s, id, cur.head); err != nil || ok {
			if err != nil {
				return false, Next{}, err
			}
			next, err := r.escalate(s, k, e)
			return false, next, err
		}
		if n := len(links); n > 0 && links[n-1].head == "" {
			next, fail, err := r.resolveRebase(s, repo, links[n-1])
			switch {
			case err != nil:
				return false, Next{}, err
			case fail != nil:
				fail.Authorization = id
				next, err := r.escalate(s, k, *fail)
				return false, next, err
			case next != (Next{}):
				return false, next, nil
			}
			continue
		}
		if cur.head != dl.head {
			last := links[len(links)-1]
			if cur.pr, err = r.publishLink(s, repo, last, dl.pr); err != nil {
				return false, Next{}, err
			}
			v, next, err := r.headChecks(s, k, repo, cur.head, cur.pr)
			if err != nil || v == nil {
				return false, next, err
			}
			if v.Verdict != validationPassed {
				next, err := r.escalate(s, k, escalationDoc{Authorization: id, From: cur.head, Onto: last.doc.Onto, Kind: config.RebaseMajorConflicts, Files: []string{},
					Reason: "the checks of the rebased head failed: " + strings.Join(v.Failing, ", ")})
				return false, next, err
			}
			cur.checks = v
		}
		m, err := r.observe(repo, cur.pr, cur.head)
		if err != nil {
			return false, Next{}, err
		}
		if !obstructed(m) {
			if cur.head == dl.head {
				return r.merge(s, id, repo, cur.head, cur.pr)
			}
			return r.reauthorize(s, repo, id, cur, links, outcomes)
		}
		if err := r.rebaseFront(s, k, repo, id, q.stay, cur.head, m, links); err != nil {
			if errors.Is(err, errRebaseStuck) || errors.Is(err, ErrUnsupported) {
				return false, Next{Wait: WaitDirector, Step: s.ID}, r.stuck(s, cur.head, m, err)
			}
			return false, Next{}, err
		}
	}
}

// errRebaseStuck reports a front that already rebased maxRebaseRounds times in its stay.
var errRebaseStuck = errors.New("rebased three times in one stay at the front")

// rebaseFront records the next rebase of head cur onto the base tip m measured, as <step>-<k>-rebase-<n>, unless the
// stay already made maxRebaseRounds; a major_conflicts rebase is recorded and then escalated by the loop.
func (r *run) rebaseFront(s workflow.Step, k int, repo config.Repository, id string, stay int, cur string, m forge.Mergeability, links []rebaseLink) error {
	all, err := r.rebases(s.ID)
	if err != nil {
		return err
	}
	rounds := 0
	for _, l := range all {
		if l.doc.Authorization == id && l.doc.Stay == stay {
			rounds++
		}
	}
	if rounds >= maxRebaseRounds {
		return errRebaseStuck
	}
	doc, err := r.replay(repo, cur, m.Base)
	if err != nil {
		return err
	}
	doc.Authorization, doc.Stay = id, stay
	n := 1
	for _, l := range all {
		if l.k == k && l.n >= n {
			n = l.n + 1
		}
	}
	name := rebaseName(s.ID, k, n)
	return r.putJSONDoc(name, decisionID(s.ID, k, false), fmt.Sprintf("rebase-%d.json", n), doc)
}

// pendingEscalation returns the escalation of head cur under authorization id: a recorded one, or the one a recorded
// major_conflicts rebase of cur asks for.
func (r *run) pendingEscalation(s workflow.Step, id, cur string) (escalationDoc, bool, error) {
	es, _, err := r.escalations(s.ID)
	if err != nil {
		return escalationDoc{}, false, err
	}
	if i := slices.IndexFunc(es, func(e escalationDoc) bool { return e.From == cur && e.Authorization == id }); i >= 0 {
		return es[i], true, nil
	}
	all, err := r.rebases(s.ID)
	if err != nil {
		return escalationDoc{}, false, err
	}
	i := slices.IndexFunc(all, func(l rebaseLink) bool {
		return l.doc.From == cur && l.doc.Authorization == id && l.doc.Kind == config.RebaseMajorConflicts
	})
	if i < 0 {
		return escalationDoc{}, false, nil
	}
	d := all[i].doc
	files := []string{}
	for _, c := range d.Conflicts {
		files = append(files, c.Path)
	}
	return escalationDoc{Authorization: id, From: cur, Onto: d.Onto, Kind: d.Kind, Files: files, Reason: d.Reason, Carry: true}, true, nil
}

// stuck tells the Director that the front waits for them: it rebased three times in one stay, or this copy cannot
// rebase. The observation is recorded once per head and state as pr-blocked-<head>-<state>.
func (r *run) stuck(s workflow.Step, head string, m forge.Mergeability, why error) error {
	id, m, err := r.recordBlocked(head, m)
	if err != nil {
		return err
	}
	if !errors.Is(why, errRebaseStuck) {
		return r.notifyMergeBlocked(id, m)
	}
	text := fmt.Sprintf("Pull request: %s\nHead: %s\nBase: %s\nState: %s\nCommits behind: %d\nConflicting files: %s\nStep %s rebased this task %d times in one stay at the front of the merge queue and the pull request is still %s, so Guildhall stops rebasing it. The task keeps the front and waits for the Director: guildhall task hold-merge --repo %s %s holds it and its next advance releases the front, and unholding it later starts a new stay with %d more rebases; guildhall step send-back sends the build back to rebase it.",
		m.URL, m.Head, m.Base, m.State, m.Behind, strings.Join(m.Files, ", "), s.ID, maxRebaseRounds, m.State, r.task.Repository, r.task.ID, maxRebaseRounds)
	return r.notify("rebase-stuck."+id, store.MessageRebaseStuck, text)
}

// reauthorize authorizes the merge of rebased head x at the front: a decision already bound to x stands; below the
// repository's reapprove_after_rebase, with every gate holding for x, nothing_open holding and the accepted spec
// unchanged, the earlier authorization carries over as <step>-<k>-authorized; otherwise a new merge decision for x is
// asked.
func (r *run) reauthorize(s workflow.Step, repo config.Repository, from string, x deliverHead, links []rebaseLink, outcomes []reviewOutcome) (bool, Next, error) {
	requires := repo.Merge.Requires
	// An authorization already recorded for x stands, so a replay after a crash, or a retry after the forge refused
	// the merge, merges under it again and never records a second one.
	if id, err := r.authorization(s, x.head); err != nil || id != "" {
		if err != nil {
			return false, Next{}, err
		}
		return r.merge(s, id, repo, x.head, x.pr)
	}
	conditions, err := r.conditions(s, requires, outcomes, x, nil)
	if err != nil {
		return false, Next{}, err
	}
	if dec := r.decisionOn(s, x.head); dec != nil {
		switch {
		case !dec.Answered:
			return false, Next{Wait: WaitDecision, Step: s.ID, Decision: dec.ID}, r.notifyDecision(*dec, s.ID)
		case dec.Choice == "hold":
			return false, Next{Wait: WaitDirector, Step: s.ID, Decision: dec.ID},
				r.notify("merge-held."+dec.ID, store.MessageMergeHeld, fmt.Sprintf("The Guildmaster held the merge of rebased head %s (decision %s); the Director decides what happens next.", x.head, dec.ID))
		case dec.Choice == "merge":
			return r.merge(s, dec.ID, repo, x.head, x.pr)
		}
		return false, Next{}, fmt.Errorf("decision %s: %w: outcome %s", dec.ID, ErrUnsupported, dec.Choice)
	}
	kind := chainKind(links)
	k := r.deliverRound(s)
	id := decisionID(s.ID, k, false)
	holds := len(carryRefusals(conditions)) == 0
	spec, err := r.sameSpec(s, from)
	if err != nil {
		return false, Next{}, err
	}
	needs := requires.Guildmaster() && repo.Merge.NeedsReapproval(kind)
	// A narrowed review's findings and suggestions reach the Guildmaster whatever the threshold.
	findings := 0
	for _, l := range x.links {
		findings += l.findings
	}
	if !needs && holds && spec && findings == 0 {
		if err := r.carry(s, id, x, conditions, from, links, kind); err != nil {
			return false, Next{}, err
		}
		return r.merge(s, id, repo, x.head, x.pr)
	}
	var why []string
	switch {
	case needs:
		why = append(why, fmt.Sprintf("a %s rebase is at or above reapprove_after_rebase = %s", kind, repo.Merge.Reapprove()))
	case !spec:
		why = append(why, "the accepted spec changed since "+from)
	}
	if findings > 0 {
		why = append(why, fmt.Sprintf("the narrowed review of the resolution approved it with %d finding(s) or suggestion(s)", findings))
	}
	why = append(why, carryRefusals(conditions)...)
	lead := fmt.Sprintf("Head %s is a %s rebase of the head merge %s authorized; the authorization does not carry over: %s.\n", x.head, kind, from, strings.Join(why, "; "))
	next, err := r.openMerge(s, k, x.head, r.mergeQuestion(s, k, x.head, x.pr, conditions, outcomes, lead))
	return false, next, err
}

// carry records the authorization of merge id for head x carried over from authorization from after the rebases
// links of kind, schema guildhall.merge-authorization/2, and tells the Director.
func (r *run) carry(s workflow.Step, id string, x deliverHead, conditions []condition, from string, links []rebaseLink, kind config.RebaseKind) error {
	if r.document(id+"-authorized") != nil {
		return nil
	}
	repo, err := r.repository()
	if err != nil {
		return err
	}
	var names []string
	for _, l := range links {
		names = append(names, l.name)
	}
	if names == nil {
		names = []string{}
	}
	a := authorizationDoc{Schema: carriedAuthorizationSchema, Head: x.head, PullRequest: x.pr, Requires: repo.Merge.Requires.Effective(), Mergeability: "clear",
		Conditions: conditions, SpecDigest: r.specDigest(s), CarriedFrom: from, Rebase: names, Kind: kind}
	if err := r.authorize(id, a); err != nil {
		return err
	}
	return r.notify("approval-carried."+id, store.MessageApprovalCarried, fmt.Sprintf("The merge approval of %s carries over to head %s after a %s rebase (reapprove_after_rebase = %s): %s.",
		from, x.head, kind, repo.Merge.Reapprove(), id+"-authorized"))
}

// specDigest is the digest of the spec the build was given, accepted or plan's, that the latest code review outcome
// before deliver is bound to, or "".
func (r *run) specDigest(s workflow.Step) string {
	outcomes, err := r.reviewOutcomes(s)
	if err != nil {
		return ""
	}
	for _, o := range outcomes {
		if o.outcome.Revisions.SpecDigest != "" {
			return o.outcome.Revisions.SpecDigest
		}
	}
	return ""
}

// sameSpec reports whether the accepted spec is the one authorization from was given under, when it recorded one.
func (r *run) sameSpec(s workflow.Step, from string) (bool, error) {
	var a authorizationDoc
	ok, err := r.readJSONDoc(from+"-authorized", &a)
	if err != nil || !ok || a.SpecDigest == "" {
		return err == nil, err
	}
	return a.SpecDigest == r.specDigest(s), nil
}

// linkPR is the pull request recorded when link chain's rebase to head x was pushed.
func (r *run) linkPR(links []rebaseLink, x string) (*pullRequestDoc, error) {
	for _, l := range links {
		if l.head != x {
			continue
		}
		var pr pullRequestDoc
		ok, err := r.readJSONDoc(l.name+"-pr", &pr)
		if err != nil || !ok {
			return nil, cmp.Or(err, fmt.Errorf("rebase %s to head %s has no recorded pull request", l.name, x))
		}
		return &pr, nil
	}
	return nil, fmt.Errorf("no rebase ends at head %s", x)
}

// validateSteps are the validate steps before deliver step s whose rebases the delivered head may follow.
func (r *run) validateSteps(s workflow.Step) []string {
	var out []string
	for _, p := range r.def.Steps {
		if p.ID == s.ID {
			break
		}
		if p.Type == "validate" {
			out = append(out, p.ID)
		}
	}
	return out
}

// authorizationSchema versions the authorization deliver records before a merge Guildhall makes by itself;
// carriedAuthorizationSchema the one that carries an earlier approval over to a rebased head.
const (
	authorizationSchema        = "guildhall.merge-authorization/1"
	carriedAuthorizationSchema = "guildhall.merge-authorization/2"
)

// condition is one merge requirement as deliver found it, with the names of the evidence it read.
type condition struct {
	Requirement string `json:"requirement"`
	Required    bool   `json:"required"`
	Holds       bool   `json:"holds"`
	// Reason says why the requirement does not hold; empty when it holds, but for nothing_open naming the
	// suggestions it did not count.
	Reason   string   `json:"reason,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

// line is the condition as the merge question lists it.
func (c condition) line() string {
	switch {
	case !c.Required:
		return "not required"
	case c.Holds && c.Reason != "":
		return "holds (" + c.Reason + ")"
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
	// SpecDigest is the accepted spec the review outcomes were bound to; empty in a legacy authorization.
	SpecDigest string `json:"spec_digest,omitempty"`
	// CarriedFrom, Rebase and Kind are set in a carried-over authorization, schema guildhall.merge-authorization/2:
	// the decision or authorization id it carries, the rebase documents from that head to this one, and the hardest
	// rebase kind among them.
	CarriedFrom string            `json:"carried_from,omitempty"`
	Rebase      []string          `json:"rebase,omitempty"`
	Kind        config.RebaseKind `json:"kind,omitempty"`
	// Supersedes is the merge decision deliver left unanswered when the requirements it was opened for came to hold
	// before the Guildmaster answered it, such as a disputed finding the Director ruled on after the fallback.
	Supersedes string `json:"supersedes,omitempty"`
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

// nothingOpen is the nothing_open condition: no review outcome has an open finding, and the task has no unanswered
// decision but deliver step s's own. Every finding is open but a disputed one with a standing Director ruling on it;
// suggestions never count, though the reason names how many there are, whether the condition holds or not.
func (r *run) nothingOpen(s workflow.Step, required bool, outcomes []reviewOutcome) condition {
	c := condition{Requirement: "nothing_open", Required: required}
	var open []string
	suggestions := 0
	for _, o := range outcomes {
		c.Evidence = append(c.Evidence, o.name)
		unresolved, disputed := 0, 0
		for _, f := range o.outcome.Findings {
			switch state, _ := r.findingState(o, f.ID); state {
			case workflow.Disputed:
				disputed++
			case "":
				unresolved++
			}
		}
		if unresolved+disputed > 0 {
			open = append(open, fmt.Sprintf("%s has %d unresolved and %d disputed finding(s)", o.name, unresolved, disputed))
		}
		suggestions += len(o.outcome.Suggestions)
	}
	for i, d := range r.decisions {
		if p := r.decisionPlaces[i]; d.Answered || (p.ok && p.step == s.ID && !p.exit) {
			continue
		}
		c.Evidence = append(c.Evidence, d.ID)
		open = append(open, fmt.Sprintf("decision %s is unanswered", d.ID))
	}
	c.Holds = len(open) == 0
	if suggestions > 0 {
		open = append(open, fmt.Sprintf("%d suggestion(s), not counted", suggestions))
	}
	c.Reason = strings.Join(open, ", ")
	return c
}

// findingState is what keeps finding id of outcome o open for nothing_open: Disputed for a disputed finding with no
// standing ruling, "" for any other finding, an unresolved or newly raised one; and the move of the standing
// ruling that released a disputed finding, with state Resolved.
func (r *run) findingState(o reviewOutcome, id string) (workflow.DispositionState, store.RulingMove) {
	if !slices.ContainsFunc(o.outcome.Dispositions, func(d workflow.Disposition) bool { return d.Finding == id && d.State == workflow.Disputed }) {
		return "", ""
	}
	for _, g := range r.rulingsOn(o.name) {
		if g.Finding == id && g.ContestedBy == "" {
			return workflow.Resolved, g.Move
		}
	}
	return workflow.Disputed, ""
}

// openItems lists, for the merge question, each finding and suggestion of the latest review outcomes.
func (r *run) openItems(outcomes []reviewOutcome) string {
	var lines []string
	for _, o := range outcomes {
		for _, f := range o.outcome.Findings {
			state, move := r.findingState(o, f.ID)
			what := "unresolved"
			switch state {
			case workflow.Disputed:
				what = "disputed, no ruling"
			case workflow.Resolved:
				what = fmt.Sprintf("disputed, ruled %s", move)
			}
			lines = append(lines, fmt.Sprintf("- %s finding %s (%s, %s): %s", o.name, f.ID, shortText(f.Rule, 256), what, shortText(f.Summary, 512)))
		}
		for _, sg := range o.outcome.Suggestions {
			lines = append(lines, fmt.Sprintf("- %s suggestion %s: %s", o.name, sg.ID, shortText(sg.Summary, 512)))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\nFindings and suggestions of the latest review outcomes:\n" + strings.Join(lines, "\n") + "\n"
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
		if a.Schema != authorizationSchema && a.Schema != carriedAuthorizationSchema {
			return "", fmt.Errorf("document %s is not a %s or %s authorization", r.documents[i].Name, authorizationSchema, carriedAuthorizationSchema)
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

// mergedBy says what allowed merge id: the Guildmaster's merge decision, Guildhall's recorded authorization, or an
// approval carried over to a rebased head.
func (r *run) mergedBy(id string) string {
	if r.decision(id) != nil {
		return "decision " + id
	}
	var a authorizationDoc
	if ok, err := r.readJSONDoc(id+"-authorized", &a); err == nil && ok && a.CarriedFrom != "" {
		return fmt.Sprintf("merged by carried-over approval of %s after %s rebase, %s-authorized", a.CarriedFrom, a.Kind, id)
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
	msg := fmt.Sprintf("Merged head %s (%s); the task is done.", h, r.mergedBy(id))
	var a authorizationDoc
	if _, err := r.readJSONDoc(id+"-authorized", &a); err != nil {
		return false, Next{}, err
	}
	if a.Supersedes != "" {
		msg += fmt.Sprintf(" Merge decision %s, opened while the requirements did not all hold, is no longer needed.", a.Supersedes)
	}
	if err := r.notify("merged."+id, store.MessageMerged, msg); err != nil {
		return false, Next{}, err
	}
	if err := r.retire(id, repo, h); err != nil {
		return false, Next{}, err
	}
	if err := r.audit(s, h, pr); err != nil {
		return false, Next{}, err
	}
	if err := r.leave("merged:"+id, false); err != nil {
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
