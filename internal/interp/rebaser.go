package interp

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Tier 2 of the tiered rebase (docs/interpreter.md#tiered-rebase): a minor_conflicts replay is resolved by a rebaser
// Artisan, squashed onto the base tip, and approved by a narrowed review of the resolved hunks before its head is
// pushed. Both attempts run under synthetic steps of the deliver or validate step that rebases, <step>-rebase and
// <step>-rebase-review; they are not workflow steps, but they count against the attempt caps.

// rebaserProfile is the profile a rebaser attempt runs under, the rebase role profile (workflow.RoleProfile). Every
// parsed Director config has it, shipped or declared; a config without it, such as one built in memory, cannot
// resolve conflicts, so minor conflicts escalate.
const rebaserProfile = "rebaser"

// rebaseKindType is the step kind of a rebaser attempt.
const rebaseKindType = "rebase"

// The schemas of tier 2's records.
const (
	resolutionSchema = "guildhall.rebase-resolution/1"
	coverageSchema   = "guildhall.rebase-coverage/1"
)

// rebaseReport is rebase.json, the rebaser's account of its resolution.
type rebaseReport struct {
	BehaviourChange *bool  `json:"behaviour_change"`
	Reason          string `json:"reason"`
}

// resolvedDoc is <link>-resolved: the head a minor_conflicts rebase resolved to, one commit on the base tip. Rebaser
// is the commit the rebaser left, empty when the replay had no conflicts and only its patch id differed.
type resolvedDoc struct {
	Schema   string `json:"schema"`
	Marked   string `json:"marked,omitempty"`
	Rebaser  string `json:"rebaser,omitempty"`
	Resolved string `json:"resolved"`
	Reason   string `json:"reason,omitempty"`
}

// coverageDoc is <link>-covered: the narrowed review outcome that approved a resolution, with its count of findings
// and suggestions; any, a suggestion included, sends the merge to the Guildmaster.
type coverageDoc struct {
	Schema   string `json:"schema"`
	Head     string `json:"head"`
	Outcome  string `json:"outcome"`
	Findings int    `json:"findings"`
}

// rebaserStep is the synthetic rebaser step of step owner.
func rebaserStep(owner string) workflow.Step {
	return workflow.Step{ID: owner + "-rebase", Type: rebaseKindType, Version: 1,
		Settings: map[string]json.RawMessage{"profile": json.RawMessage(`"` + rebaserProfile + `"`)}}
}

// narrowStep is the synthetic narrowed review step of step owner: a code review with the settings, so the profile,
// of the workflow's first review step; with no profile set it runs under that step's effective profile, the
// reviewer role profile (workflow.EffectiveProfile).
func (r *run) narrowStep(owner string) (workflow.Step, error) {
	i := slices.IndexFunc(r.def.Steps, func(s workflow.Step) bool { return s.Type == "review" })
	if i < 0 {
		return workflow.Step{}, fmt.Errorf("%w: a narrowed review needs a review step in the workflow", ErrUnsupported)
	}
	rv := r.def.Steps[i]
	return workflow.Step{ID: owner + "-rebase-review", Type: rv.Type, Version: rv.Version, Settings: rv.Settings}, nil
}

// syntheticStep returns the synthetic step named id of a deliver or validate step of the workflow, or of the task's
// design rounds.
func (r *run) syntheticStep(id string) (workflow.Step, bool) {
	if _, designer := DesignSteps(r.def); id == designer {
		return r.designerStep(), true
	}
	for _, s := range r.def.Steps {
		if s.Type != "deliver" && s.Type != "validate" {
			continue
		}
		switch id {
		case s.ID + "-rebase":
			return rebaserStep(s.ID), true
		case s.ID + "-rebase-review":
			ns, err := r.narrowStep(s.ID)
			return ns, err == nil
		}
	}
	return workflow.Step{}, false
}

// resolveMinor sets a minor_conflicts link's head and coverage once a narrowed review approved its resolution.
func (r *run) resolveMinor(l *rebaseLink) error {
	var c coverageDoc
	ok, err := r.readJSONDoc(l.name+"-covered", &c)
	if err != nil || !ok {
		return err
	}
	if c.Schema != coverageSchema || !digest.ValidHead(c.Head) {
		return fmt.Errorf("document %s-covered is not a %s record", l.name, coverageSchema)
	}
	l.head, l.covered, l.findings = c.Head, true, c.Findings
	return nil
}

// resolveRebase brings the unresolved minor_conflicts link l of step owner to a covered head: the rebaser resolves its
// conflicts, or, for a clean replay whose patch id differs, the replay stands as it is; the resolution is squashed
// onto the base tip and the worktree moved to it; and a narrowed review reads the resolution. It returns the wait
// while an attempt runs, the escalation when the link cannot stand, or neither once the link is covered.
func (r *run) resolveRebase(owner workflow.Step, repo config.Repository, l rebaseLink) (Next, *escalationDoc, error) {
	files := []string{}
	for _, c := range l.doc.Conflicts {
		files = append(files, c.Path)
	}
	fail := func(carry bool, format string, args ...any) *escalationDoc {
		return &escalationDoc{From: l.doc.From, Onto: l.doc.Onto, Kind: config.RebaseMajorConflicts, Files: files, Carry: carry,
			Reason: fmt.Sprintf(format, args...)}
	}
	spec, err := r.rebaseSpec(owner)
	if err != nil {
		return Next{}, nil, err
	}
	var res resolvedDoc
	ok, err := r.readJSONDoc(l.name+"-resolved", &res)
	if err != nil {
		return Next{}, nil, err
	}
	if !ok {
		res = resolvedDoc{Schema: resolutionSchema, Marked: l.doc.Marked, Resolved: l.doc.Rebased, Reason: l.doc.Reason}
		from := l.doc.From
		if l.doc.Marked != "" {
			if _, err := r.e.Config.Profile(rebaserProfile); err != nil {
				return Next{}, fail(true, "%s; minor conflicts need a rebaser Artisan, and this copy declares no %q profile", l.doc.Reason, rebaserProfile), nil
			}
			conflicts, err := r.conflictsInput(repo, l)
			if err != nil {
				return Next{}, nil, err
			}
			rs := rebaserStep(owner.ID)
			// The rebaser resolves the conflicts in the task's worktree, moved to the marked replay first.
			before := func() error { return r.adopt(repo, l.doc.From, l.doc.Marked) }
			a, next, err := r.synthetic(rs, rebaserProfile, conflicts.Name, []render.Input{spec, conflicts}, binding{}, before)
			if err != nil || a == nil {
				return next, nil, err
			}
			if a.Outcome != store.AttemptSucceeded {
				return Next{}, fail(true, "%s; rebaser attempt %d of %s %s", l.doc.Reason, a.Number, rs.ID, a.Outcome), nil
			}
			var report rebaseReport
			name := documentName(place{step: rs.ID, round: a.Number, part: "rebase"})
			ok, err := r.readJSONDoc(name, &report)
			switch {
			case err != nil:
				return Next{}, nil, err
			case !ok || report.BehaviourChange == nil:
				return Next{}, nil, fmt.Errorf("rebaser attempt %d of %s recorded no %s saying whether its resolution changes behaviour", a.Number, rs.ID, name)
			}
			if *report.BehaviourChange {
				return Next{}, fail(false, "the rebaser reports that resolving %s changes behaviour: %s", strings.Join(files, ", "), report.Reason), nil
			}
			hd := r.document(documentName(place{step: rs.ID, round: a.Number, part: "head"}))
			if hd == nil {
				return Next{}, nil, fmt.Errorf("rebaser attempt %d of %s recorded no head", a.Number, rs.ID)
			}
			h, err := r.head(render.Input{Name: hd.Path, SHA256: hd.SHA256})
			if err != nil {
				return Next{}, nil, err
			}
			if err := r.refuseRunning(); err != nil {
				return Next{}, nil, err
			}
			squashed, err := r.e.Workspaces.Squash(r.ctx, repo.Path, l.doc.Onto, h)
			if err != nil {
				return Next{}, nil, err
			}
			res.Rebaser, res.Resolved, res.Reason, from = h, squashed, report.Reason, h
		}
		// The narrowed review reads the resolution in the worktree, so it moves there before the resolution is
		// recorded; a replay finds it already there.
		if err := r.adopt(repo, from, res.Resolved); err != nil {
			return Next{}, nil, err
		}
		if err := r.putJSONDoc(l.name+"-resolved", l.name, "resolved.json", res); err != nil {
			return Next{}, nil, err
		}
	}
	// The narrowed review reads exactly the resolved hunks: the marked replay to its resolution, or, for a clean
	// replay whose patch id differs, the whole replayed change.
	base := res.Marked
	if base == "" {
		base = l.doc.Onto
	}
	diff, err := r.e.Workspaces.Diff(r.ctx, repo.Path, base, res.Resolved)
	if err != nil {
		return Next{}, nil, err
	}
	resolution, err := r.inputDoc(l.name+"-resolution", l.name, "resolution.diff", ResolutionPort, diff)
	if err != nil {
		return Next{}, nil, err
	}
	head, err := r.inputDoc(l.name+"-head", l.name, "head", "head", []byte(res.Resolved+"\n"))
	if err != nil {
		return Next{}, nil, err
	}
	ns, err := r.narrowStep(owner.ID)
	if err != nil {
		return Next{}, nil, err
	}
	inputs := []render.Input{spec, head, resolution}
	want, err := r.want(ns, inputs)
	if err != nil {
		return Next{}, nil, err
	}
	b := binding{Revisions: &want, Range: &git.Range{Base: l.doc.Onto, Head: res.Resolved}, Scope: git.ScopeResolution}
	a, next, err := r.synthetic(ns, workflow.EffectiveProfile(ns), resolution.Name, inputs, b, nil)
	if err != nil || a == nil {
		return next, nil, err
	}
	if a.Outcome != store.AttemptSucceeded {
		return Next{}, fail(false, "the narrowed review of the resolution, attempt %d of %s, %s", a.Number, ns.ID, a.Outcome), nil
	}
	od := r.document(documentName(place{step: ns.ID, round: a.Number, part: "outcome"}))
	if od == nil {
		return Next{}, nil, fmt.Errorf("narrowed review attempt %d of %s recorded no outcome", a.Number, ns.ID)
	}
	data, err := r.read(*od)
	if err != nil {
		return Next{}, nil, err
	}
	o, err := workflow.ParseReviewOutcome(data)
	if err != nil {
		return Next{}, nil, err
	}
	if o.Verdict != workflow.VerdictApproved {
		var why []string
		for _, f := range o.Findings {
			why = append(why, f.Summary)
		}
		return Next{}, fail(false, "the narrowed review of the resolution is %s: %s", o.Verdict, strings.Join(why, "; ")), nil
	}
	c := coverageDoc{Schema: coverageSchema, Head: res.Resolved, Outcome: od.Name, Findings: len(o.Findings) + len(o.Suggestions)}
	return Next{}, nil, r.putJSONDoc(l.name+"-covered", l.name, "covered.json", c)
}

// rebaseSpec is the accepted spec the build that produced step owner's head was given, the rebaser's and the narrowed
// review's spec input.
func (r *run) rebaseSpec(owner workflow.Step) (render.Input, error) {
	build, err := r.buildStep(owner)
	if err != nil {
		return render.Input{}, err
	}
	return r.input(build, "spec")
}

// conflictsInput records, once, link l's conflicts input <link>-conflicts: the marked replay, each conflicting file
// with its line count, and git log -p of the commits on both sides that touch them.
func (r *run) conflictsInput(repo config.Repository, l rebaseLink) (render.Input, error) {
	if d := r.document(l.name + "-conflicts"); d != nil {
		return docInput("conflicts", *d, r.e.DocumentDir), nil
	}
	var paths []string
	var b strings.Builder
	fmt.Fprintf(&b, "# Conflicts to resolve\n\nGuildhall replayed task head %s onto base tip %s (merge base %s) as one commit, %s, with conflict markers written into these files:\n\n", l.doc.From, l.doc.Onto, l.doc.MergeBase, l.doc.Marked)
	for _, c := range l.doc.Conflicts {
		paths = append(paths, c.Path)
		fmt.Fprintf(&b, "- `%s`: %d conflicting line(s)\n", c.Path, c.Lines)
	}
	theirs, err := r.e.Workspaces.Log(r.ctx, repo.Path, l.doc.MergeBase, l.doc.Onto, paths...)
	if err != nil {
		return render.Input{}, err
	}
	ours, err := r.e.Workspaces.Log(r.ctx, repo.Path, l.doc.MergeBase, l.doc.From, paths...)
	if err != nil {
		return render.Input{}, err
	}
	fmt.Fprintf(&b, "\n## Commits on the base branch that touch these files\n\n```diff\n%s```\n\n## Commits of the task that touch these files\n\n```diff\n%s```\n", theirs, ours)
	return r.inputDoc(l.name+"-conflicts", l.name, "conflicts.md", "conflicts", []byte(b.String()))
}

// inputDoc records data, once, as document name at <tasks>/<repo>/<task>/<dir>/<file> and returns it as an input on
// port. A document already recorded stands, so a replay gives the same input.
func (r *run) inputDoc(name, dir, file, port string, data []byte) (render.Input, error) {
	if d := r.document(name); d != nil {
		return docInput(port, *d, r.e.DocumentDir), nil
	}
	ref := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
		Path: path.Join("tasks", r.task.Repository, r.task.ID, dir, file), SHA256: digest.Of(data).Hex()}
	if err := r.put(ref, data); err != nil {
		return render.Input{}, err
	}
	return docInput(port, ref, r.e.DocumentDir), nil
}

func docInput(port string, d store.DocumentRef, dir string) render.Input {
	return render.Input{Port: port, Name: d.Path, SHA256: d.SHA256, Path: filepath.Join(dir, filepath.FromSlash(d.Path))}
}

// adopt moves the task's worktree from commit from to commit to, keeping untracked stage outputs, and refuses while
// any attempt of the task runs in it; without Workspaces there is no worktree to move.
func (r *run) adopt(repo config.Repository, from, to string) error {
	if r.e.Workspaces == nil || from == to {
		return nil
	}
	if err := r.refuseRunning(); err != nil {
		return err
	}
	w := git.Workspace{Repository: repo.Path, Remote: repo.Remote, Branch: r.branch(), Dir: r.workPath()}
	if err := r.e.Workspaces.Adopt(r.ctx, w, from, to); err != nil {
		return fmt.Errorf("move %s from %s to %s: %w", w.Branch, from, to, err)
	}
	return nil
}

// synthetic runs the attempt of synthetic step s, under profile, that serves one purpose, told apart by key, the
// path of an input only its binding holds. With none bound to key it runs before, then binds inputs and b and starts
// one; while it runs it waits, relaunching one recorded but never launched; once it finished it is taken and returned.
func (r *run) synthetic(s workflow.Step, profile, key string, inputs []render.Input, b binding, before func() error) (*store.StageAttempt, Next, error) {
	var a *store.StageAttempt
	for i := range r.attempts {
		x := &r.attempts[i]
		if x.Step != s.ID {
			continue
		}
		bd, err := r.binding(s.ID, x.Number)
		if err != nil {
			return nil, Next{}, err
		}
		if slices.ContainsFunc(bd.Inputs, func(in boundInput) bool { return in.Path == key }) {
			a = x
		}
	}
	rt, err := r.runtime(profile)
	if err != nil {
		return nil, Next{}, err
	}
	if a == nil {
		if held, err := r.admissionHeld(); err != nil || held {
			return nil, Next{Wait: WaitPaused, Step: s.ID}, err
		}
		if before != nil {
			if err := before(); err != nil {
				return nil, Next{}, err
			}
		}
		n := 1
		if l := r.latestAttempt(s.ID); l != nil {
			n = l.Number + 1
		}
		if err := r.bind(s, n, inputs, b); err != nil {
			return nil, Next{}, err
		}
		next, err := r.startBound(s, n, rt)
		return nil, next, err
	}
	next := Next{Wait: WaitAttempt, Step: s.ID, Attempt: a.Number}
	switch {
	case a.Outcome == "" && exists(r.resultFile(s.ID, a.Number)):
		return nil, next, nil
	case a.Outcome == "" && exists(filepath.Join(r.attemptDir(s.ID, a.Number), launchedFile)):
		return nil, next, r.workerStalled(s.ID, a.Number)
	case a.Outcome == "":
		if held, err := r.admissionHeld(); err != nil || held {
			return nil, Next{Wait: WaitPaused, Step: s.ID}, err
		}
		bd, err := r.binding(s.ID, a.Number)
		if err != nil {
			return nil, Next{}, err
		}
		return nil, next, r.launch(s, a.Number, rt, r.boundInputs(bd))
	case a.Outcome == store.AttemptSucceeded && !r.taken(s, a.Number):
		if err := r.takeResult(s, a); err != nil {
			return nil, Next{}, err
		}
		if a.Outcome != store.AttemptSucceeded {
			return a, Next{}, nil
		}
	}
	return a, Next{}, nil
}

// recordRebaseOutputs records rebaser attempt n's head and its rebase.json, which it must list and which must say
// whether its resolution changes behaviour. The head is the final commit of its worktree, or, as for a build, the
// commit an explicit head file it lists first names.
func (r *run) recordRebaseOutputs(s workflow.Step, n int, port string, outputs []adapter.Output) error {
	i := slices.IndexFunc(outputs, func(o adapter.Output) bool { return o.Path == render.RebaseReportFile })
	if i < 0 || len(outputs) > 2 || len(outputs) == 2 && i != 1 {
		return rejectOutput(fmt.Errorf("a succeeded rebase result lists %s, after a head file if it names one", render.RebaseReportFile))
	}
	data, err := os.ReadFile(filepath.Join(r.workPath(), render.RebaseReportFile))
	if err != nil {
		return rejectOutput(fmt.Errorf("read output %s: %w", render.RebaseReportFile, err))
	}
	var report rebaseReport
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil || report.BehaviourChange == nil || strings.TrimSpace(report.Reason) == "" {
		return rejectOutput(fmt.Errorf("%s must be one JSON object with behaviour_change and a reason: %v", render.RebaseReportFile, err))
	}
	if err := r.recordRebaseHead(s, n, port, outputs[:i]); err != nil {
		return err
	}
	name := documentName(place{step: s.ID, round: n, part: "rebase"})
	if r.document(name) != nil {
		return nil
	}
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
		Path: path.Join("tasks", r.task.Repository, r.task.ID, r.key(s.ID, n).AttemptID(), render.RebaseReportFile), SHA256: digest.Of(data).Hex()}, data)
}

// recordRebaseHead records rebaser attempt n's head: the commit the head file listed names, checked as a build's,
// or the worktree's final commit when it lists none.
func (r *run) recordRebaseHead(s workflow.Step, n int, port string, listed []adapter.Output) error {
	if len(listed) == 0 {
		return r.recordWorkHead(s, n, port)
	}
	name := documentName(place{step: s.ID, round: n, part: port})
	if r.document(name) != nil {
		return nil
	}
	o := listed[0]
	if !fs.ValidPath(o.Path) || o.Path == "." || strings.ContainsAny(o.Path, `\:`) {
		return rejectOutput(fmt.Errorf("output path %q is not a clean relative slash-separated path", o.Path))
	}
	data, err := os.ReadFile(filepath.Join(r.workPath(), filepath.FromSlash(o.Path)))
	if err != nil {
		return rejectOutput(fmt.Errorf("read output %q: %w", o.Path, err))
	}
	if err := checkPort(workflow.CodeHead, data); err != nil {
		return rejectOutput(err)
	}
	if err := r.checkHead(s, n, strings.TrimSpace(string(data))); err != nil {
		return err
	}
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
		Path: path.Join("tasks", r.task.Repository, r.task.ID, r.key(s.ID, n).AttemptID(), o.Path), SHA256: digest.Of(data).Hex()}, data)
}
