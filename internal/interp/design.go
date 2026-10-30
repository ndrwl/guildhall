package interp

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Design rounds (docs/interpreter.md#design-rounds): the Director's `task design` asks for a design of a feature task
// after its grill has succeeded and before its first build. Each request is one round, n, recorded as the immutable
// brief <decision>-<n>-brief; while the round is open the walk starts no workflow step. The round runs one designer
// Artisan, attempts <attempt>-<k> of a synthetic step that is not a workflow step but counts against the attempt
// caps, then asks the Guildmaster decision <decision>-<n>, bound to the design's digest. approved records the design
// as <decision>-<n>-design, which plan is given on its design port; changes_requested reopens the grill with the
// answer as feedback. Everything is derived from those records.

const (
	// designerProfile is the profile a designer attempt runs under, the design role profile (workflow.RoleProfile).
	designerProfile = "designer"
	// designKindType is the step kind of a designer attempt.
	designKindType = "design"
	// DesignPort carries to a plan step the design the Guildmaster approved in the task's latest approved design
	// round; it is the designer's output port too.
	DesignPort = "design"
	// BriefPort carries a designer attempt its round's brief.
	BriefPort = "brief"
	// roundPrefix prefixes the design round's step ids while a workflow step has one of them (DesignSteps).
	roundPrefix = "round-"
)

// ErrDesignRound reports a `task design` request the task's records refuse; nothing is recorded.
var ErrDesignRound = errors.New("design round refused")

// DesignSteps returns the step ids design rounds of a task bound to def run under: decision, whose round n decision is
// <decision>-<n>, and attempt, whose attempt k is <attempt>-<k>. They are design and designer, both prefixed with
// round- as many times as it takes for neither to be a step id of def, so they depend on the bound workflow alone and
// stay the same for the life of the task.
func DesignSteps(def *workflow.Definition) (decision, attempt string) {
	taken := func(id string) bool {
		return slices.ContainsFunc(def.Steps, func(s workflow.Step) bool { return s.ID == id })
	}
	decision, attempt = "design", "designer"
	for taken(decision) || taken(attempt) {
		decision, attempt = roundPrefix+decision, roundPrefix+attempt
	}
	return decision, attempt
}

// DesignRounds counts a task's design rounds used, from its documents: one per brief a `task design` request recorded,
// whatever the round's outcome. It is zero for a task that has had none.
func DesignRounds(documents []store.DocumentRef) int {
	n := 0
	for _, d := range documents {
		if p, ok := parseDocument(d.Name); ok && isDesignDecision(p.step) && p.part == BriefPort && !p.exit && p.index == 0 {
			n++
		}
	}
	return n
}

// isDesignDecision reports whether step is a design round's decision step id under any number of round- prefixes.
func isDesignDecision(step string) bool {
	for {
		rest, ok := strings.CutPrefix(step, roundPrefix)
		if !ok {
			return step == "design"
		}
		step = rest
	}
}

func briefName(decision string, n int) string {
	return documentName(place{step: decision, round: n, part: BriefPort})
}

// designerStep is the synthetic designer step of the task's design rounds.
func (r *run) designerStep() workflow.Step {
	_, id := DesignSteps(r.def)
	return workflow.Step{ID: id, Type: designKindType, Version: 1,
		Settings: map[string]json.RawMessage{"profile": json.RawMessage(`"` + designerProfile + `"`)}}
}

// grillStep is the workflow's Director grill, which a design round follows and its changes_requested reopens.
func (r *run) grillStep() (workflow.Step, bool) {
	i := slices.IndexFunc(r.def.Steps, func(s workflow.Step) bool { return s.Type == "grill" && s.Actor() == workflow.DirectorActor })
	if i < 0 {
		return workflow.Step{}, false
	}
	return r.def.Steps[i], true
}

// designRound is the task's latest design round, 0 when it has had none.
func (r *run) designRound() int {
	decision, _ := DesignSteps(r.def)
	n := 0
	for _, p := range r.docPlaces {
		if p.ok && p.step == decision && p.part == BriefPort && !p.exit && p.index == 0 {
			n = max(n, p.round)
		}
	}
	return n
}

// designRequested validates a `task design` request and records its brief as round n+1's, before anything launches.
// A request whose brief is the latest round's is a replay and records nothing while that round is open; once its
// decision is answered, the same brief is a new request for round n+1. It is refused, recording nothing, for a
// task whose workflow has no Director grill and build step, once build has started, while a round is open or any
// attempt runs, and until the grill has succeeded and served every design answer that reopened it.
func (r *run) designRequested(ev Event) error {
	if strings.TrimSpace(ev.Reason) == "" {
		return fmt.Errorf("%w: a design round needs a nonempty brief", ErrEvent)
	}
	decision, _ := DesignSteps(r.def)
	n := r.designRound()
	open := false
	if n > 0 {
		d := r.decision(decisionID(decision, n, false))
		open = d == nil || !d.Answered
	}
	if open && r.document(briefName(decision, n)).SHA256 == digest.Of([]byte(ev.Reason)).Hex() {
		return nil
	}
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s/%s: %s", ErrDesignRound, r.task.Repository, r.task.ID, fmt.Sprintf(format, args...))
	}
	grill, ok := r.grillStep()
	builds := func(step string) bool {
		return slices.ContainsFunc(r.def.Steps, func(s workflow.Step) bool { return s.ID == step && s.Type == "build" })
	}
	if !ok || !slices.ContainsFunc(r.def.Steps, func(s workflow.Step) bool { return s.Type == "build" }) {
		return refuse("bugs have no design round: workflow %s has no Director grill step and build step", r.def.Name)
	}
	if i := slices.IndexFunc(r.attempts, func(a store.StageAttempt) bool { return builds(a.Step) }); i >= 0 {
		return refuse("build has started: step %s has attempt %d", r.attempts[i].Step, r.attempts[i].Number)
	}
	if open {
		return refuse("design round %d is already open, until decision %s is answered", n, decisionID(decision, n, false))
	}
	for _, a := range r.attempts {
		if a.Outcome == "" {
			return refuse("step %s attempt %d is still running", a.Step, a.Number)
		}
	}
	if a := r.latestAttempt(grill.ID); a == nil || a.Outcome != store.AttemptSucceeded {
		return refuse("the grill has not succeeded")
	}
	if pending, _, err := r.designFeedback(grill); err != nil {
		return err
	} else if len(pending) > 0 {
		return refuse("the grill has not succeeded since the design answer that reopened it")
	}
	data := []byte(ev.Reason)
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: briefName(decision, n+1),
		Path: path.Join("tasks", r.task.Repository, r.task.ID, decisionID(decision, n+1, false), "brief.md"), SHA256: digest.Of(data).Hex()}, data)
}

// designRun moves the task's design rounds on before the walk and reports whether the walk may go on: it records
// the design of every round answered approved, and while the latest round is open it runs the round's designer and
// then asks its decision, and waits.
func (r *run) designRun() (bool, Next, error) {
	decision, _ := DesignSteps(r.def)
	n := r.designRound()
	for k := 1; k <= n; k++ {
		d := r.decision(decisionID(decision, k, false))
		if d == nil || !d.Answered {
			continue
		}
		switch d.Choice {
		case string(workflow.VerdictApproved):
			if err := r.approveDesign(*d); err != nil {
				return false, Next{}, err
			}
		case changesRequested:
			// The grill's pending feedback reopens it (designFeedback).
		default:
			return false, Next{}, fmt.Errorf("decision %s: %w: outcome %s", d.ID, ErrUnsupported, d.Choice)
		}
	}
	if n == 0 {
		return true, Next{}, nil
	}
	id := decisionID(decision, n, false)
	if d := r.decision(id); d != nil {
		if d.Answered {
			return true, Next{}, nil
		}
		return false, Next{Wait: WaitDecision, Step: decision, Decision: id}, r.notifyDecision(*d, decision)
	}
	brief := *r.document(briefName(decision, n))
	a, next, err := r.designer(brief)
	if err != nil || a == nil {
		return false, next, err
	}
	next, err = r.askDesign(n, brief, a)
	return false, next, err
}

// designer runs the designer attempt of the round whose brief is brief, told apart by the brief its binding holds.
// With none, it passes the dependency gate and the hold, then binds and starts one; while one runs it waits,
// relaunching one recorded but never launched; a failed or stopped one waits for the Director, whose retry starts
// the next within the same round. Once one has succeeded it is taken and returned.
func (r *run) designer(brief store.DocumentRef) (*store.StageAttempt, Next, error) {
	s := r.designerStep()
	var a *store.StageAttempt
	for i := range r.attempts {
		x := &r.attempts[i]
		if x.Step != s.ID || a != nil && x.Number < a.Number {
			continue
		}
		b, err := r.binding(s.ID, x.Number)
		if err != nil {
			return nil, Next{}, err
		}
		if slices.ContainsFunc(b.Inputs, func(in boundInput) bool { return in.Port == BriefPort && in.Path == brief.Path }) {
			a = x
		}
	}
	rt, err := r.runtime(designerProfile)
	if err != nil {
		return nil, Next{}, err
	}
	failed := a != nil && (a.Outcome == store.AttemptFailed || a.Outcome == store.AttemptStopped)
	if failed && r.retry == nil && r.queuedStart(s.ID, a.Number+1) >= 0 {
		// The retry that queued the next attempt is still owed, so any advance starts it as the retry would.
		r.retry = &Event{Kind: AttemptRetried, Repository: r.task.Repository, Task: r.task.ID, Step: s.ID, Attempt: a.Number}
	}
	retrying := failed && r.retry != nil && r.retry.Step == s.ID && r.retry.Attempt == a.Number
	switch {
	case a == nil || retrying:
		if a == nil {
			// The designer is held by the dependency gate as any Artisan attempt is, but never passes it for research.
			if held, next, err := r.dependencyGate(s); err != nil || held {
				return nil, next, err
			}
		}
		if held, err := r.admissionHeld(); err != nil || held {
			return nil, Next{Wait: WaitPaused, Step: s.ID}, err
		}
		k := 1
		if l := r.latestAttempt(s.ID); l != nil {
			k = l.Number + 1
		}
		if r.document(bindingName(s.ID, k)) == nil {
			inputs, err := r.designInputs(brief)
			if err != nil {
				return nil, Next{}, err
			}
			if err := r.bind(s, k, inputs, binding{Retry: r.retryContext(s.ID, k)}); err != nil {
				return nil, Next{}, err
			}
		}
		next, err := r.startBound(s, k, rt)
		if next.Wait == WaitQueued {
			return nil, next, err
		}
		return nil, next, r.retryLaunched(s.ID, k, err)
	case a.Outcome == "":
		next := Next{Wait: WaitAttempt, Step: s.ID, Attempt: a.Number}
		switch {
		case exists(r.resultFile(s.ID, a.Number)):
			return nil, next, nil
		case exists(filepath.Join(r.attemptDir(s.ID, a.Number), launchedFile)):
			return nil, next, r.workerStalled(s.ID, a.Number)
		}
		if held, err := r.admissionHeld(); err != nil || held {
			return nil, Next{Wait: WaitPaused, Step: s.ID}, err
		}
		b, err := r.binding(s.ID, a.Number)
		if err != nil {
			return nil, Next{}, err
		}
		return nil, next, r.launch(s, a.Number, rt, r.boundInputs(b))
	case failed:
		text := fmt.Sprintf("Attempt %d of step %s %s; the Director decides what happens next.", a.Number, s.ID, a.Outcome)
		if a.IntakeRejection != "" {
			text += " Result rejected: " + a.IntakeRejection
		}
		err := r.notify(fmt.Sprintf("attempt-failed.%s-%d", s.ID, a.Number), store.MessageAttemptFailed, text)
		return nil, Next{Wait: WaitDirector, Step: s.ID, Attempt: a.Number}, err
	case !r.taken(s, a.Number):
		if err := r.takeResult(s, a); err != nil {
			return nil, Next{}, err
		}
		if a.Outcome != store.AttemptSucceeded {
			return r.designer(brief)
		}
	}
	return a, Next{}, nil
}

// designInputs are what a designer attempt is given: the brief, then as context the latest grill notes, research
// notes and spec, in workflow order, when they exist, the task's context and its linked documents.
func (r *run) designInputs(brief store.DocumentRef) ([]render.Input, error) {
	inputs := []render.Input{docInput(BriefPort, brief, r.e.DocumentDir)}
	for _, s := range r.def.Steps {
		port := map[string]string{"grill": "notes", "research": "notes", "plan": "spec"}[s.Type]
		if port == "" {
			continue
		}
		if doc, _, err := r.output(s.ID, port); err == nil {
			inputs = append(inputs, docInput("context", doc, r.e.DocumentDir))
		}
	}
	c, err := r.contextInput()
	if err != nil {
		return nil, err
	}
	if c != nil {
		inputs = append(inputs, *c)
	}
	linked, err := r.linkInputs()
	if err != nil {
		return nil, err
	}
	return append(inputs, linked...), nil
}

// askDesign opens round n's decision once its designer attempt a succeeded: an approval bound to the design's digest,
// asking about the brief and the design.
func (r *run) askDesign(n int, brief store.DocumentRef, a *store.StageAttempt) (Next, error) {
	decision, _ := DesignSteps(r.def)
	doc := r.document(documentName(place{step: a.Step, round: a.Number, part: DesignPort}))
	if doc == nil {
		return Next{}, fmt.Errorf("designer attempt %d of %s recorded no design", a.Number, a.Step)
	}
	dg, err := digest.ParseHex(doc.SHA256)
	if err != nil {
		return Next{}, err
	}
	id := decisionID(decision, n, false)
	ask := render.DesignQuestion(r.task.Repository, r.task.ID, id, n, []render.Input{docInput(BriefPort, brief, r.e.DocumentDir), docInput(DesignPort, *doc, r.e.DocumentDir)})
	d := store.Decision{Repository: r.task.Repository, TaskID: r.task.ID, ID: id, Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: string(dg)}, Options: ask.Options, Question: ask.Question}
	if _, err := r.e.Records.OpenDecision(r.mutation("decision", id), d); err != nil {
		return Next{}, err
	}
	r.addDecision(d)
	return Next{Wait: WaitDecision, Step: decision, Decision: id}, r.notifyDecision(d, decision)
}

// designDocument returns the designer's design document the decision of a design round is bound to.
func (r *run) designDocument(d store.Decision) (store.DocumentRef, error) {
	_, attempt := DesignSteps(r.def)
	for i, p := range r.docPlaces {
		if !p.ok || p.step != attempt || p.part != DesignPort || p.exit || p.index != 0 {
			continue
		}
		if dg, err := digest.ParseHex(r.documents[i].SHA256); err == nil && d.Subject != nil && string(dg) == d.Subject.Digest {
			return r.documents[i], nil
		}
	}
	return store.DocumentRef{}, fmt.Errorf("decision %s was bound to a design that is not recorded", d.ID)
}

// approveDesign records the design an approved round's decision d was bound to as <decision>-<n>-design, once.
func (r *run) approveDesign(d store.Decision) error {
	name := d.ID + "-" + DesignPort
	if r.document(name) != nil {
		return nil
	}
	design, err := r.designDocument(d)
	if err != nil {
		return err
	}
	doc := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: design.Path, SHA256: design.SHA256}
	if _, err := r.e.Records.PutDocument(r.mutation("document", name), doc); err != nil {
		return err
	}
	r.addDocument(doc)
	return nil
}

// designInput is the design input of plan step s: the design of the task's latest approved design round, or nil for
// any other step or a task with no approved design.
func (r *run) designInput(s workflow.Step) *render.Input {
	if s.Type != "plan" {
		return nil
	}
	decision, _ := DesignSteps(r.def)
	doc, _, err := r.output(decision, DesignPort)
	if err != nil {
		return nil
	}
	in := docInput(DesignPort, doc, r.e.DocumentDir)
	return &in
}

// designFeedback returns, when t is the grill, each design round's changes_requested answer that no attempt of the
// grill has served, as feedback recorded first as document <decision>-<n>-answer, and the design each was bound to,
// as reviewed inputs, once per document. A failed or stopped grill attempt does not serve it (history.served).
func (r *run) designFeedback(t workflow.Step) (feedback, reviewed []render.Input, err error) {
	if g, ok := r.grillStep(); !ok || g.ID != t.ID {
		return nil, nil, nil
	}
	decision, _ := DesignSteps(r.def)
	for _, d := range r.rounds(decision) {
		if !d.Answered || d.Choice != changesRequested {
			continue
		}
		f := firing{path: path.Join("tasks", r.task.Repository, r.task.ID, d.ID, "answer.md"), sha256: digest.Of([]byte(d.Answer)).Hex(), answer: d, round: r.round(d)}
		if done, err := r.served(t.ID, f); err != nil || done {
			if err != nil {
				return nil, nil, err
			}
			continue
		}
		if r.document(d.ID+"-answer") == nil {
			doc := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: d.ID + "-answer", Path: f.path, SHA256: f.sha256}
			if err := r.put(doc, []byte(d.Answer)); err != nil {
				return nil, nil, err
			}
		}
		feedback = append(feedback, render.Input{Port: FeedbackPort, Name: f.path, SHA256: f.sha256, Path: filepath.Join(r.e.DocumentDir, filepath.FromSlash(f.path))})
		design, err := r.designDocument(*d)
		if err != nil {
			return nil, nil, err
		}
		if in := docInput(ReviewedPort, design, r.e.DocumentDir); !slices.Contains(reviewed, in) {
			reviewed = append(reviewed, in)
		}
	}
	return feedback, reviewed, nil
}
