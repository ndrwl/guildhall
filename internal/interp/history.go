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

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// place is where a record sits in its step's history, as its name says (docs/interpreter.md#history).
type place struct {
	step string
	// round is an attempt number or decision round, or for an exit, k: the exit follows the step's kth blocking result.
	round int
	// exit marks a loop-exit decision, <step>-x<k>, and the documents recorded from its answer.
	exit bool
	// part is what a document is: an output port, "inputs", "revisions" or "answer"; empty for a decision.
	part string
	// index numbers a later output of a port, from 2; 0 for the first.
	index int
}

// parseDecision reads a decision id, <step>-<round> or <step>-x<k>. It is the one parser of decision ids.
func parseDecision(id string) (place, bool) {
	i := strings.LastIndex(id, "-")
	if i < 0 || !workflow.ValidID(id[:i]) {
		return place{}, false
	}
	n, exit, ok := parseRound(id[i+1:])
	return place{step: id[:i], round: n, exit: exit}, ok
}

// parseDocument reads a document name, <decision>-<part> or <decision>-<part>-<index>, from the right, so a
// step id ending in -<digits> is never read as a round. It is the one parser of document names.
func parseDocument(name string) (place, bool) {
	segs := strings.Split(name, "-")
	index := 0
	if n, err := strconv.Atoi(segs[len(segs)-1]); err == nil && len(segs) > 3 {
		if n < 2 || strconv.Itoa(n) != segs[len(segs)-1] {
			return place{}, false
		}
		index, segs = n, segs[:len(segs)-1]
	}
	if len(segs) < 3 {
		return place{}, false
	}
	part := segs[len(segs)-1]
	if _, _, round := parseRound(part); round || !workflow.ValidID(part) || strings.Trim(part, "0123456789") == "" {
		return place{}, false
	}
	p, ok := parseDecision(strings.Join(segs[:len(segs)-1], "-"))
	p.part, p.index = part, index
	return p, ok
}

// parseRound reads <n> or x<n>, with n a positive number without leading zeros.
func parseRound(s string) (int, bool, bool) {
	number, exit := strings.CutPrefix(s, "x")
	n, err := strconv.Atoi(number)
	return n, exit, err == nil && n > 0 && strconv.Itoa(n) == number
}

// decisionID names a step's round decision, <step>-<round>, or its loop exit, <step>-x<k>; it is also the
// prefix of every document recorded from that attempt, round or exit.
func decisionID(step string, round int, exit bool) string {
	if exit {
		return fmt.Sprintf("%s-x%d", step, round)
	}
	return fmt.Sprintf("%s-%d", step, round)
}

// documentName names the document at p.
func documentName(p place) string {
	name := decisionID(p.step, p.round, p.exit) + "-" + p.part
	if p.index > 0 {
		name += "-" + strconv.Itoa(p.index)
	}
	return name
}

// errNoOrigin reports a loop exit with no blocking result it could follow.
var errNoOrigin = errors.New("no originating firing")

// history is the private view of one task's records an advance reads: which step, round and port each
// record belongs to, read from its name by parseDecision and parseDocument alone, so no other code reads
// structure out of names. It is built from the records at the start of an advance and every write the advance makes adds
// its record here, so a read after a write sees it; it caches nothing else. read verifies a document's digest.
type history struct {
	repository, task string
	def              *workflow.Definition
	read             func(store.DocumentRef) ([]byte, error)

	attempts  []store.StageAttempt
	documents []store.DocumentRef
	decisions []store.Decision
	// rulings are the Director's rulings, each on a finding of one outcome document, named by Document.
	rulings []store.Ruling
	// fullReviews are the Director's full-review requests, by number.
	fullReviews []store.FullReview
	// docPlaces and decisionPlaces parallel documents and decisions; ok is false for a name of another form.
	docPlaces      []parsed
	decisionPlaces []parsed
}

type parsed struct {
	place
	ok bool
}

func newHistory(repository, task string, def *workflow.Definition, read func(store.DocumentRef) ([]byte, error),
	attempts []store.StageAttempt, documents []store.DocumentRef, decisions []store.Decision) *history {
	h := &history{repository: repository, task: task, def: def, read: read, attempts: attempts}
	for _, d := range documents {
		h.addDocument(d)
	}
	for _, d := range decisions {
		h.addDecision(d)
	}
	return h
}

// addAttempt adds a recorded attempt and returns it as held by the view.
func (h *history) addAttempt(a store.StageAttempt) *store.StageAttempt {
	h.attempts = append(h.attempts, a)
	return &h.attempts[len(h.attempts)-1]
}

func (h *history) addDocument(d store.DocumentRef) {
	p, ok := parseDocument(d.Name)
	h.documents = append(h.documents, d)
	h.docPlaces = append(h.docPlaces, parsed{p, ok})
}

// addDecision adds an opened decision and returns it as held by the view.
func (h *history) addDecision(d store.Decision) *store.Decision {
	p, ok := parseDecision(d.ID)
	h.decisions = append(h.decisions, d)
	h.decisionPlaces = append(h.decisionPlaces, parsed{p, ok})
	return &h.decisions[len(h.decisions)-1]
}

// rulingsOn returns the rulings on findings of the named outcome document.
func (h *history) rulingsOn(document string) []store.Ruling {
	var out []store.Ruling
	for _, r := range h.rulings {
		if r.Document == document {
			out = append(out, r)
		}
	}
	return out
}

// markContested marks the ruling on a finding of document as contested by attempt n of step.
func (h *history) markContested(document, finding, step string, n int) {
	for i, r := range h.rulings {
		if r.Document == document && r.Finding == finding {
			h.rulings[i].ContestedBy = decisionID(step, n, false)
		}
	}
}

// contestedBy returns the rulings attempt n of step contested.
func (h *history) contestedBy(step string, n int) []store.Ruling {
	var out []store.Ruling
	for _, r := range h.rulings {
		if r.ContestedBy == decisionID(step, n, false) {
			out = append(out, r)
		}
	}
	return out
}

// fullReview returns the full-review request attempt by consumed, or else the pending one, or nil.
func (h *history) fullReview(by string) *store.FullReview {
	var pending *store.FullReview
	for i, f := range h.fullReviews {
		switch f.ConsumedBy {
		case by:
			return &h.fullReviews[i]
		case "":
			pending = &h.fullReviews[i]
		}
	}
	return pending
}

func (h *history) attempt(step string, n int) *store.StageAttempt {
	for i, a := range h.attempts {
		if a.Step == step && a.Number == n {
			return &h.attempts[i]
		}
	}
	return nil
}

func (h *history) latestAttempt(step string) *store.StageAttempt {
	var last *store.StageAttempt
	for i, a := range h.attempts {
		if a.Step == step && (last == nil || a.Number > last.Number) {
			last = &h.attempts[i]
		}
	}
	return last
}

func (h *history) document(name string) *store.DocumentRef {
	for i, d := range h.documents {
		if d.Name == name {
			return &h.documents[i]
		}
	}
	return nil
}

func (h *history) decision(id string) *store.Decision {
	for i, d := range h.decisions {
		if d.ID == id {
			return &h.decisions[i]
		}
	}
	return nil
}

// rounds returns the step's round decisions, <step>-<n>, in round order; loop exits are not rounds.
func (h *history) rounds(step string) []*store.Decision {
	var out []*store.Decision
	for i, p := range h.decisionPlaces {
		if p.ok && p.step == step && !p.exit {
			out = append(out, &h.decisions[i])
		}
	}
	slices.SortFunc(out, func(a, b *store.Decision) int { return h.round(a) - h.round(b) })
	return out
}

func (h *history) round(d *store.Decision) int {
	p, _ := parseDecision(d.ID)
	return p.round
}

// latestDecision returns the step's round decision of the highest round, or nil.
func (h *history) latestDecision(step string) *store.Decision {
	rs := h.rounds(step)
	if len(rs) == 0 {
		return nil
	}
	return rs[len(rs)-1]
}

// nextRound is the round the step's next decision opens.
func (h *history) nextRound(step string) int {
	if d := h.latestDecision(step); d != nil {
		return h.round(d) + 1
	}
	return 1
}

// continued counts the step's loop exits answered continue: each allows one more round.
func (h *history) continued(step string) int {
	n := 0
	for i, p := range h.decisionPlaces {
		if d := h.decisions[i]; p.ok && p.step == step && p.exit && d.Answered && d.Choice == "continue" {
			n++
		}
	}
	return n
}

// firing is one blocking result of a looping step: an Artisan outcome or a Guildmaster's answer.
type firing struct {
	path, sha256 string
	answer       *store.Decision
	round        int
}

// firings returns the blocking results of step s in round order: its recorded outcomes that are
// Blocking, its decisions answered changes_requested, or for validate its failed results. An outcome or
// result that cannot be read is an error.
func (h *history) firings(s workflow.Step) ([]firing, error) {
	var out []firing
	if s.Actor() == workflow.GuildmasterActor {
		for _, d := range h.rounds(s.ID) {
			if d.Answered && d.Choice == changesRequested {
				p := path.Join("tasks", h.repository, h.task, d.ID, "answer.md")
				out = append(out, firing{path: p, sha256: digest.Of([]byte(d.Answer)).Hex(), answer: d, round: h.round(d)})
			}
		}
		return out, nil
	}
	if s.Type == "validate" {
		return h.failedValidations(s)
	}
	attempts := slices.Clone(h.attempts)
	slices.SortFunc(attempts, func(a, b store.StageAttempt) int { return a.Number - b.Number })
	for _, a := range attempts {
		if a.Step != s.ID {
			continue
		}
		o, err := h.outcome(s.ID, a.Number)
		if err != nil {
			return nil, err
		}
		if o != nil && o.Blocking() {
			d := h.document(documentName(place{step: s.ID, round: a.Number, part: "outcome"}))
			out = append(out, firing{path: d.Path, sha256: d.SHA256, round: a.Number})
		}
	}
	return out, nil
}

// failedValidations returns the failed results of validate step s, <step>-<n>-result, in round order.
func (h *history) failedValidations(s workflow.Step) ([]firing, error) {
	var out []firing
	for i, p := range h.docPlaces {
		if !p.ok || p.step != s.ID || p.exit || p.part != "result" || p.index != 0 {
			continue
		}
		d := h.documents[i]
		data, err := h.read(d)
		if err != nil {
			return nil, err
		}
		v, err := parseValidation(d.Name, data)
		if err != nil {
			return nil, err
		}
		if v.Verdict == validationFailed {
			out = append(out, firing{path: d.Path, sha256: d.SHA256, round: p.round})
		}
	}
	slices.SortFunc(out, func(a, b firing) int { return a.round - b.round })
	return out, nil
}

// origin returns the blocking result loop exit k of step s follows: its kth firing.
func (h *history) origin(s workflow.Step, k int) (firing, error) {
	fs, err := h.firings(s)
	if err != nil {
		return firing{}, err
	}
	if k < 1 || k > len(fs) {
		return firing{}, fmt.Errorf("%w for exit %s: step %s has %d", errNoOrigin, decisionID(s.ID, k, true), s.ID, len(fs))
	}
	return fs[k-1], nil
}

// outcome returns the recorded outcome of attempt n of a review step, or nil when it has none.
func (h *history) outcome(step string, n int) (*workflow.ReviewOutcome, error) {
	d := h.document(documentName(place{step: step, round: n, part: "outcome"}))
	if d == nil {
		return nil, nil
	}
	data, err := h.read(*d)
	if err != nil {
		return nil, err
	}
	return workflow.ParseReviewOutcome(data)
}

// binding returns what attempt n of a step was given, <step>-<n>-inputs.
func (h *history) binding(step string, n int) (binding, error) {
	var b binding
	d := h.document(bindingName(step, n))
	if d == nil {
		return b, fmt.Errorf("attempt %d of %s has no recorded inputs", n, step)
	}
	data, err := h.read(*d)
	if err != nil {
		return b, err
	}
	return b, json.Unmarshal(data, &b)
}

func bindingName(step string, n int) string {
	return documentName(place{step: step, round: n, part: "inputs"})
}

// served reports whether an attempt of step t was given the firing as feedback.
func (h *history) served(t string, f firing) (bool, error) {
	for _, a := range h.attempts {
		// A failed or stopped attempt did not act on its feedback, so the retry that follows it is given it again.
		if a.Step != t || a.Outcome == store.AttemptFailed || a.Outcome == store.AttemptStopped {
			continue
		}
		b, err := h.binding(t, a.Number)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(b.Inputs, func(in boundInput) bool { return in.Port == FeedbackPort && in.Path == f.path }) {
			return true, nil
		}
	}
	return false, nil
}

// output returns the latest first document of one step's output port and the id of the attempt, round or
// exit that recorded it. Rounds order by number; an exit orders immediately after the round it originates
// in, before later rounds.
func (h *history) output(step, port string) (store.DocumentRef, string, error) {
	best, err := h.latest(step, port)
	if err != nil {
		return store.DocumentRef{}, "", err
	}
	p := h.docPlaces[best]
	return h.documents[best], decisionID(p.step, p.round, p.exit), nil
}

// reviewedRange returns the range the code review step's latest outcome reviewed, from the binding of the attempt that
// recorded it. Reuse requires matching accepted-spec and standards revisions; otherwise review must remain full.
// It returns nil when no eligible range is available.
func (h *history) reviewedRange(step string, revisions *workflow.Revisions) (*git.Range, error) {
	i, err := h.latest(step, "outcome")
	if err != nil || h.docPlaces[i].exit {
		return nil, nil
	}
	if h.document(bindingName(step, h.docPlaces[i].round)) == nil {
		return nil, nil
	}
	b, err := h.binding(step, h.docPlaces[i].round)
	if err != nil {
		return nil, err
	}
	if b.Revisions == nil || revisions == nil || b.Revisions.SpecDigest != revisions.SpecDigest || b.Revisions.StandardsRevision != revisions.StandardsRevision {
		return nil, nil
	}
	return b.Range, nil
}

// latest returns the index of the latest first document of one step's output port. Rounds order by number; an exit
// orders immediately after the round it originates in, before later rounds.
func (h *history) latest(step, port string) (int, error) {
	best, bestRound, bestExit := -1, 0, false
	for i, p := range h.docPlaces {
		if !p.ok || p.step != step || p.part != port || p.index != 0 {
			continue
		}
		n := p.round
		if p.exit {
			j := slices.IndexFunc(h.def.Steps, func(s workflow.Step) bool { return s.ID == step })
			if j < 0 {
				return -1, fmt.Errorf("output %s: step %s is not in the workflow", h.documents[i].Name, step)
			}
			o, err := h.origin(h.def.Steps[j], n)
			if err != nil {
				return -1, fmt.Errorf("output %s: %w", h.documents[i].Name, err)
			}
			n = o.round
		}
		if c := cmp.Compare(n, bestRound); best < 0 || c > 0 || c == 0 && p.exit && !bestExit {
			best, bestRound, bestExit = i, n, p.exit
		}
	}
	if best < 0 {
		return -1, fmt.Errorf("%s.%s has no recorded document", step, port)
	}
	return best, nil
}
