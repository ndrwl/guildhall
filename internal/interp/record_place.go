package interp

import (
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Place is where a record sits, as its name says: the exported face of the parser behind the history view
// (docs/interpreter.md#history), for readers outside interp such as the task page.
type Place struct {
	// Step is the step id with its slice qualifier, --s<k>, removed; Slice is k, or 1 when the id has none.
	Step  string
	Slice int
	// Round is an attempt number or decision round, or for an exit, Exit true, k of <step>-x<k>.
	Round int
	Exit  bool
	// Part is empty for a decision; otherwise what the document is: an output port, "inputs", "revisions",
	// "answer", "send-back", or a sequence part, "rebase", "escalated" or "checks".
	Part string
	// Index numbers a later output of a port, from 2, or a sequence part, from 1; 0 otherwise.
	Index int
}

// rebaseFollowOns are the documents the rebaser records after a rebase <decision>-rebase-<n>, each named
// <decision>-rebase-<n>-<sub>.
var rebaseFollowOns = []string{"resolved", "covered", "conflicts", "head", "resolution", "pr"}

// RecordPlace reads any document or decision name the engine writes, and is ok false for a name of another form,
// such as follow-ups, finished or q<n>. It is pure and reads, first match wins:
//
//  1. send-back-<decision>, Part "send-back";
//  2. <decision>-rebase-<n>[-<sub>], <decision>-escalated-<n> and <decision>-checks-<head>, the outer decision's
//     place with Part the sequence part and Index n (0 for checks);
//  3. a document, by parseDocument;
//  4. a decision, by parseDecision, unless its step itself reads as a document (build-2-head-1).
func RecordPlace(name string) (Place, bool) {
	if rest, ok := strings.CutPrefix(name, "send-back-"); ok {
		if p, ok := parseDecision(rest); ok {
			p.part = "send-back"
			return exported(p), true
		}
	}
	if p, ok := parseSequencePart(name); ok {
		return exported(p), true
	}
	if p, ok := parseDocument(name); ok {
		return exported(p), true
	}
	if p, ok := parseDecision(name); ok && !isDocument(p.step) {
		return exported(p), true
	}
	return Place{}, false
}

// isDocument reports whether a decision's step reads as a document name, as the step of build-2-head-1 does: such a
// name is a later output with an index the engine never writes, not a decision.
func isDocument(step string) bool {
	_, ok := parseDocument(step)
	return ok
}

// parseSequencePart reads a name of a delivery sequence, <decision>-rebase-<n>, a rebase's follow-on
// <decision>-rebase-<n>-<sub>, <decision>-escalated-<n> or <decision>-checks-<head>, as its decision's place.
func parseSequencePart(name string) (place, bool) {
	segs := strings.Split(name, "-")
	if len(segs) < 4 {
		return place{}, false
	}
	if i := len(segs) - 2; segs[i] == "checks" && digest.ValidHead(segs[i+1]) {
		p, ok := parseDecision(strings.Join(segs[:i], "-"))
		p.part = "checks"
		return p, ok
	}
	if len(segs) >= 5 && segs[len(segs)-3] == "rebase" && slices.Contains(rebaseFollowOns, segs[len(segs)-1]) {
		segs = segs[:len(segs)-1]
	}
	i := len(segs) - 2
	if segs[i] != "rebase" && segs[i] != "escalated" {
		return place{}, false
	}
	n, err := strconv.Atoi(segs[i+1])
	if err != nil || n < 1 || strconv.Itoa(n) != segs[i+1] {
		return place{}, false
	}
	p, ok := parseDecision(strings.Join(segs[:i], "-"))
	p.part, p.index = segs[i], n
	return p, ok
}

// exported is p as a Place, its step's slice qualifier read off.
func exported(p place) Place {
	step, k := workflow.ParseSliceStep(p.step)
	return Place{Step: step, Slice: k, Round: p.round, Exit: p.exit, Part: p.part, Index: p.index}
}
