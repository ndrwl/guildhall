package interp

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Ports the engine adds to an Artisan attempt besides the step's wired inputs.
const (
	// FeedbackPort carries each blocking outcome, or Guildmaster answer, that sent the task back to this step.
	FeedbackPort = "feedback"
	// PreviousPort carries a review step's own previous outcome, whose findings the new outcome dispositions.
	PreviousPort = "previous"
)

// changesRequested is the only outcome whose loop rules the engine follows.
const changesRequested = string(workflow.VerdictChangesRequested)

// exitOptions are the answers a loop-exit decision offers.
var exitOptions = []string{"continue", "accept", "reject"}

// binding is what an Artisan attempt was given, recorded as document <step>-<n>-inputs before the attempt
// itself, so the engine can tell later whether the attempt is still current and which loops it served.
type binding struct {
	Inputs []boundInput `json:"inputs"`
	// Revisions is what a review attempt's outcome must be bound to.
	Revisions *workflow.Revisions `json:"revisions,omitempty"`
}

type boundInput struct {
	Port   string `json:"port"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func isReview(s workflow.Step) bool { return s.Type == "review" || s.Type == "document-review" }

func bindingName(step string, n int) string { return fmt.Sprintf("%s-%d-inputs", step, n) }

// bind records attempt n's binding unless it is already recorded.
func (r *run) bind(s workflow.Step, n int, inputs []render.Input, revisions *workflow.Revisions) error {
	name := bindingName(s.ID, n)
	if r.document(name) != nil {
		return nil
	}
	b := binding{Inputs: []boundInput{}, Revisions: revisions}
	for _, in := range inputs {
		b.Inputs = append(b.Inputs, boundInput{Port: in.Port, Path: in.Name, SHA256: in.SHA256})
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name,
		Path: path.Join("tasks", r.task.Repository, r.task.ID, fmt.Sprintf("%s-%d.inputs.json", s.ID, n)), SHA256: digest(data)}, data)
}

func (r *run) binding(step string, n int) (binding, error) {
	var b binding
	d := r.document(bindingName(step, n))
	if d == nil {
		return b, fmt.Errorf("attempt %d of %s has no recorded inputs", n, step)
	}
	data, err := r.read(*d)
	if err != nil {
		return b, err
	}
	return b, json.Unmarshal(data, &b)
}

// boundInputs returns the inputs an attempt was given, as render inputs.
func (r *run) boundInputs(b binding) []render.Input {
	var out []render.Input
	for _, in := range b.Inputs {
		out = append(out, render.Input{Port: in.Port, Name: in.Path, SHA256: in.SHA256, Path: filepath.Join(r.e.DocumentDir, filepath.FromSlash(in.Path))})
	}
	return out
}

// extras returns the inputs the engine adds to a new attempt of s: its previous outcome for a review
// step, then every loop it has not yet served.
func (r *run) extras(s workflow.Step) ([]render.Input, error) {
	var out []render.Input
	if isReview(s) {
		if doc, _, err := r.output(s.ID, "outcome"); err == nil {
			out = append(out, render.Input{Port: PreviousPort, Name: doc.Path, SHA256: doc.SHA256, Path: filepath.Join(r.e.DocumentDir, filepath.FromSlash(doc.Path))})
		}
	}
	pending, err := r.pending(s)
	return append(out, pending...), err
}

// want is what an outcome of review step s must be bound to, given its current inputs.
func (r *run) want(s workflow.Step, inputs []render.Input) (workflow.Revisions, error) {
	want := workflow.Revisions{StandardsRevision: r.e.StandardsRevision}
	for _, in := range inputs {
		switch in.Port {
		case "subject":
			want.Subject.DocumentDigest = "sha256:" + in.SHA256
		case "spec":
			want.SpecDigest = "sha256:" + in.SHA256
		case "head":
			h, err := r.head(in)
			if err != nil {
				return want, err
			}
			want.Subject.CodeHead = h
		}
	}
	return want, nil
}

// stale reports whether a finished attempt of s was given other inputs, or for a review other
// revisions, than s would be given now.
func (r *run) stale(s workflow.Step, n int) (bool, error) {
	b, err := r.binding(s.ID, n)
	if err != nil {
		return false, err
	}
	inputs, err := r.inputs(s)
	if err != nil {
		return false, err
	}
	var given []render.Input
	for _, in := range r.boundInputs(b) {
		if in.Port != FeedbackPort && in.Port != PreviousPort {
			given = append(given, in)
		}
	}
	if !slices.Equal(given, inputs) {
		return true, nil
	}
	if !isReview(s) {
		return false, nil
	}
	want, err := r.want(s, inputs)
	return err == nil && (b.Revisions == nil || *b.Revisions != want), err
}

// outcome returns the recorded outcome of attempt n of a review step, or nil when it has none.
func (r *run) outcome(step string, n int) (*workflow.ReviewOutcome, error) {
	d := r.document(fmt.Sprintf("%s-%d-outcome", step, n))
	if d == nil {
		return nil, nil
	}
	data, err := r.read(*d)
	if err != nil {
		return nil, err
	}
	return workflow.ParseReviewOutcome(data)
}

// firing is one blocking result of a looping step: an Artisan outcome or a Guildmaster's answer.
type firing struct {
	path, sha256 string
	answer       *store.Decision
	round        int
}

// firings returns the blocking results of step s in round order: its recorded outcomes that are
// Blocking, or its decisions answered changes_requested.
func (r *run) firings(s workflow.Step) ([]firing, error) {
	var out []firing
	if s.Actor() == workflow.GuildmasterActor {
		for _, d := range r.rounds(s.ID) {
			if d.Answered && d.Choice == changesRequested {
				p := path.Join("tasks", r.task.Repository, r.task.ID, d.ID, "answer.md")
				out = append(out, firing{path: p, sha256: digest([]byte(d.Answer)), answer: d, round: round(d.ID)})
			}
		}
		return out, nil
	}
	attempts := slices.Clone(r.attempts)
	slices.SortFunc(attempts, func(a, b store.StageAttempt) int { return a.Number - b.Number })
	for _, a := range attempts {
		if a.Step != s.ID {
			continue
		}
		o, err := r.outcome(s.ID, a.Number)
		if err != nil {
			return nil, err
		}
		if o != nil && o.Blocking() {
			d := r.document(fmt.Sprintf("%s-%d-outcome", s.ID, a.Number))
			out = append(out, firing{path: d.Path, sha256: d.SHA256, round: a.Number})
		}
	}
	return out, nil
}

// allowed is how many times step s may loop: its rule's max plus one for every loop-exit answered continue.
func (r *run) allowed(s workflow.Step) int {
	n := s.On[changesRequested].Max
	for _, d := range r.decisions {
		if rest, ok := strings.CutPrefix(d.ID, s.ID+"-x"); ok && isNumber(rest) && d.Answered && d.Choice == "continue" {
			n++
		}
	}
	return n
}

// served reports whether an attempt of step t was given the firing as feedback.
func (r *run) served(t string, f firing) (bool, error) {
	for _, a := range r.attempts {
		if a.Step != t {
			continue
		}
		b, err := r.binding(t, a.Number)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(b.Inputs, func(in boundInput) bool { return in.Port == FeedbackPort && in.Path == f.path }) {
			return true, nil
		}
	}
	return false, nil
}

// pending returns, as feedback inputs, every allowed firing of a loop back to step t that no attempt of t
// has served yet, recording a Guildmaster's answer as a document first.
func (r *run) pending(t workflow.Step) ([]render.Input, error) {
	var out []render.Input
	for _, s := range r.def.Steps {
		if rule, ok := s.On[changesRequested]; !ok || rule.Goto != t.ID {
			continue
		}
		fs, err := r.firings(s)
		if err != nil {
			return nil, err
		}
		for _, f := range fs[:min(len(fs), r.allowed(s))] {
			if done, err := r.served(t.ID, f); err != nil || done {
				if err != nil {
					return nil, err
				}
				continue
			}
			if f.answer != nil && r.document(f.answer.ID+"-answer") == nil {
				doc := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: f.answer.ID + "-answer", Path: f.path, SHA256: f.sha256}
				if err := r.put(doc, []byte(f.answer.Answer)); err != nil {
					return nil, err
				}
			}
			out = append(out, render.Input{Port: FeedbackPort, Name: f.path, SHA256: f.sha256, Path: filepath.Join(r.e.DocumentDir, filepath.FromSlash(f.path))})
		}
	}
	return out, nil
}

// reviewed completes an Artisan review step whose latest attempt n succeeded, unless its outcome blocks.
func (r *run) reviewed(s workflow.Step, n int) (bool, Next, error) {
	o, err := r.outcome(s.ID, n)
	if err != nil {
		return false, Next{}, err
	}
	switch {
	case o == nil:
		return false, Next{}, fmt.Errorf("attempt %d recorded no outcome", n)
	case o.Verdict == workflow.VerdictRejected:
		return false, Next{}, fmt.Errorf("%w: outcome %s", ErrUnsupported, o.Verdict)
	case !o.Blocking():
		return true, Next{}, nil
	}
	subject := &store.Subject{Kind: store.SubjectHead, Digest: o.Revisions.Subject.CodeHead}
	if s.Type == "document-review" {
		subject = &store.Subject{Kind: store.SubjectDocument, Digest: o.Revisions.Subject.DocumentDigest}
	}
	return r.changes(s, Next{Wait: WaitDirector, Step: s.ID, Attempt: n}, subject)
}

// changes follows a blocking result of step s: without a loop rule the Director decides; within the rule's
// max the step runs again once the loop's target has served it; past it the loop-exit decision is asked.
func (r *run) changes(s workflow.Step, here Next, subject *store.Subject) (bool, Next, error) {
	rule, ok := s.On[changesRequested]
	if !ok {
		what := fmt.Sprintf("%s-%d", s.ID, here.Attempt)
		if here.Decision != "" {
			what = here.Decision
		}
		return false, here, r.notify("changes-requested."+what, "changes-requested",
			fmt.Sprintf("Step %s requested changes and has no loop rule; the Director decides what happens next.", s.ID))
	}
	fs, err := r.firings(s)
	if err != nil {
		return false, Next{}, err
	}
	k := len(fs)
	if k > r.allowed(s) {
		return r.exit(s, k, subject, fs[k-1])
	}
	i := slices.IndexFunc(r.def.Steps, func(t workflow.Step) bool { return t.ID == rule.Goto })
	if actor := r.def.Steps[i].Actor(); actor != workflow.ArtisanActor {
		return false, Next{}, fmt.Errorf("%w: loops back to steps run by the %s", ErrUnsupported, actor)
	}
	done, err := r.served(rule.Goto, fs[k-1])
	if err != nil || !done {
		return false, Next{}, fmt.Errorf("loop to %s was not taken: %v", rule.Goto, err)
	}
	// The target ran again and the step's inputs did not change: ask the step again on the same subject.
	if s.Actor() == workflow.GuildmasterActor {
		next, err := r.ask(s)
		return false, next, err
	}
	next, err := r.start(s)
	return false, next, err
}

// exit asks the Guildmaster how to leave a loop whose max is exhausted by firing k, decision <step>-x<k>.
func (r *run) exit(s workflow.Step, k int, subject *store.Subject, origin firing) (bool, Next, error) {
	id := fmt.Sprintf("%s-x%d", s.ID, k)
	d := r.decision(id)
	if d == nil {
		nd := store.Decision{
			Repository: r.task.Repository, TaskID: r.task.ID, ID: id, Kind: store.DecisionLoopExit, Subject: subject, Options: exitOptions,
			Question: fmt.Sprintf("Step %s of %s/%s still requests changes after %d rounds, its max of %d.\n\n"+
				"Answer with one of: continue (one more round), accept (record your approval as the step's outcome), reject.\n",
				s.ID, r.task.Repository, r.task.ID, k, s.On[changesRequested].Max),
		}
		if _, err := r.e.Records.OpenDecision(r.mutation("decision", id), nd); err != nil {
			return false, Next{}, err
		}
		r.decisions = append(r.decisions, nd)
		d = &r.decisions[len(r.decisions)-1]
	}
	next := Next{Wait: WaitDecision, Step: s.ID, Decision: id}
	switch {
	case !d.Answered:
		return false, next, r.notifyDecision(*d, s.ID)
	case d.Choice == "reject":
		return false, Next{Wait: WaitDirector, Step: s.ID, Decision: id}, r.notify("loop-exit-rejected."+id, "loop-exit-rejected",
			fmt.Sprintf("The Guildmaster rejected step %s at loop exit %s; the Director decides what happens next.", s.ID, id))
	case d.Choice != "accept":
		return false, Next{}, fmt.Errorf("loop exit %s answered %q but the loop was not taken", id, d.Choice)
	case s.Type == "guildmaster-approval":
		return true, Next{}, r.accept(s, *d)
	case !isReview(s):
		return false, Next{}, fmt.Errorf("%w: accepting at the loop exit of a %s step", ErrUnsupported, s.Type)
	}
	return true, Next{}, r.acceptOutcome(s, *d, origin)
}

// acceptOutcome records the Guildmaster's accept at a loop exit as the review step's outcome, <id>-outcome:
// an approval bound to the originating review's recorded revisions, retaining its findings as still-open.
func (r *run) acceptOutcome(s workflow.Step, d store.Decision, origin firing) error {
	name := d.ID + "-outcome"
	if r.document(name) != nil {
		return nil
	}
	o := workflow.ReviewOutcome{Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.HumanReviewer, Verdict: workflow.VerdictApproved}
	if origin.answer != nil {
		binding := r.document(origin.answer.ID + "-revisions")
		if binding == nil {
			return fmt.Errorf("decision %s has no review revisions", origin.answer.ID)
		}
		data, err := r.read(*binding)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &o.Revisions); err != nil {
			return err
		}
	} else {
		previous, err := r.outcome(s.ID, origin.round)
		if err != nil {
			return err
		}
		if previous == nil {
			return fmt.Errorf("attempt %d of %s has no review outcome", origin.round, s.ID)
		}
		o.Revisions, o.Findings = previous.Revisions, previous.Findings
		for _, f := range o.Findings {
			o.Dispositions = append(o.Dispositions, workflow.Disposition{Finding: f.ID, State: workflow.StillOpen})
		}
	}
	if err := o.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: path.Join("tasks", r.task.Repository, r.task.ID, d.ID, "outcome.json"), SHA256: digest(data)}, data)
}

// checkOutcome refuses a review attempt's outcome that is not bound to the revisions the attempt was given,
// or that leaves a finding of the previous outcome it was given without a disposition.
func (r *run) checkOutcome(s workflow.Step, n int, data []byte) error {
	o, err := workflow.ParseReviewOutcome(data)
	if err != nil {
		return err
	}
	b, err := r.binding(s.ID, n)
	if err != nil {
		return err
	}
	if b.Revisions == nil {
		return fmt.Errorf("%w: attempt %d recorded no revisions to bind its outcome to", ErrOutcome, n)
	}
	if err := o.BoundTo(*b.Revisions); err != nil {
		return fmt.Errorf("%w: %v", ErrOutcome, err)
	}
	prev, err := r.previous(b)
	if err != nil || prev == nil {
		return err
	}
	for _, f := range prev.Findings {
		if !slices.ContainsFunc(o.Dispositions, func(d workflow.Disposition) bool { return d.Finding == f.ID }) {
			return fmt.Errorf("%w: previous finding %q has no disposition", ErrOutcome, f.ID)
		}
	}
	return nil
}

// previous returns the previous outcome an attempt was given, or nil.
func (r *run) previous(b binding) (*workflow.ReviewOutcome, error) {
	i := slices.IndexFunc(b.Inputs, func(in boundInput) bool { return in.Port == PreviousPort })
	if i < 0 {
		return nil, nil
	}
	data, err := r.read(store.DocumentRef{Path: b.Inputs[i].Path, SHA256: b.Inputs[i].SHA256})
	if err != nil {
		return nil, err
	}
	return workflow.ParseReviewOutcome(data)
}

// stall tells the Director when attempt n of a looping review step requests changes again within its max
// and shows no progress over its previous outcome (report.md §5.3). It is information only: the loop goes on.
func (r *run) stall(s workflow.Step, n int) error {
	o, err := r.outcome(s.ID, n)
	if _, loops := s.On[changesRequested]; err != nil || o == nil || !o.Blocking() || !loops {
		return err
	}
	b, err := r.binding(s.ID, n)
	if err != nil {
		return err
	}
	prev, err := r.previous(b)
	if err != nil || prev == nil {
		return err
	}
	if fs, err := r.firings(s); err != nil || len(fs) > r.allowed(s) {
		return err
	}
	var signals []string
	if prev.Revisions.Subject == o.Revisions.Subject {
		what := "document"
		if s.Type == "review" {
			what = "head"
		}
		signals = append(signals, what+" unchanged")
	}
	if !slices.ContainsFunc(o.Dispositions, func(d workflow.Disposition) bool { return d.State != workflow.StillOpen }) {
		signals = append(signals, "no finding resolved or withdrawn")
	}
	for _, d := range o.Dispositions {
		if d.State == workflow.StillOpen && slices.Contains(prev.Dispositions, workflow.Disposition{Finding: d.Finding, State: workflow.StillOpen}) {
			signals = append(signals, fmt.Sprintf("finding %s still open for two rounds", d.Finding))
		}
	}
	if len(signals) == 0 {
		return nil
	}
	return r.notify(fmt.Sprintf("stall.%s-%d", s.ID, n), "stall", fmt.Sprintf("Round %d of step %s requested changes again: %s. The loop continues; the Director may add context, rule on a finding or escalate.",
		n, s.ID, strings.Join(signals, "; ")))
}

// gate checks, before deliver, that every review step before it has an outcome that does not block,
// bound to its document or the delivered head, its applicable spec and the current standards revision.
func (r *run) gate(deliver workflow.Step, h string) error {
	for _, s := range r.def.Steps {
		if s.ID == deliver.ID {
			return nil
		}
		if !isReview(s) {
			continue
		}
		inputs, err := r.inputs(s)
		if err != nil {
			return err
		}
		want, err := r.want(s, inputs)
		if err != nil {
			return err
		}
		if s.Type == "review" {
			want.Subject.CodeHead = h
		}
		ok, err := r.approved(s.ID, want)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: step %s has no outcome approving subject %+v for spec %s and standards %s", ErrReviewGate, s.ID, want.Subject, want.SpecDigest, want.StandardsRevision)
		}
	}
	return nil
}

// approved reports whether the latest output is bound to want and neither blocks nor rejects.
func (r *run) approved(step string, want workflow.Revisions) (bool, error) {
	d, _, err := r.output(step, "outcome")
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrReviewGate, err)
	}
	data, err := r.read(d)
	if err != nil {
		return false, err
	}
	o, err := workflow.ParseReviewOutcome(data)
	if err != nil {
		return false, err
	}
	return o.BoundTo(want) == nil && !o.Blocking() && o.Verdict != workflow.VerdictRejected, nil
}

// rounds returns the step's round decisions, <step>-<n>, in round order.
func (r *run) rounds(step string) []*store.Decision {
	var out []*store.Decision
	for i, d := range r.decisions {
		if rest, ok := strings.CutPrefix(d.ID, step+"-"); ok && isNumber(rest) {
			out = append(out, &r.decisions[i])
		}
	}
	slices.SortFunc(out, func(a, b *store.Decision) int { return round(a.ID) - round(b.ID) })
	return out
}

func round(id string) int {
	n, _ := strconv.Atoi(id[strings.LastIndex(id, "-")+1:])
	return n
}

func isNumber(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && strconv.Itoa(n) == s
}
