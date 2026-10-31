package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// The brief and designs these tests' design rounds use.
var (
	designBrief  = "Show the sort options as a mock-up, so the Guildmaster can choose one."
	designBrief2 = "Show the table of sort options with its column headings."
	designOne    = []byte("# Design\n\nOption A: a drop-down of sort keys. Option B: a table. Recommend A.\n")
	designTwo    = []byte("# Design\n\nA table of sort options: key, order, default.\n")
	researchSort = []byte("# Research\n\nThe list is sorted in the view.\n")
)

func designRequested(brief string) interp.Event {
	return interp.Event{Kind: interp.DesignRequested, Reason: brief}
}

// designerExpectation is designer attempt n of a round with brief, answering with design.
func designerExpectation(step string, n int, brief string, design []byte) interptest.Expectation {
	return interptest.Expectation{Step: step, Attempt: n, Profile: "designer",
		Inputs:    map[string]string{"brief": "sha256:" + sum([]byte(brief)), "context": interptest.AnyDigest},
		Standards: []string{"standards/go.md"}, Respond: interptest.Succeed("designed", interptest.File{Path: "design.md", Data: design})}
}

// gplanExpectation is plan attempt n of the director-grill workflow, given design when it is not nil, answering spec.
func gplanExpectation(n int, design, spec []byte) interptest.Expectation {
	inputs := map[string]string{"context": interptest.AnyDigest}
	if design != nil {
		inputs["design"] = "sha256:" + sum(design)
	}
	return interptest.Expectation{Step: "plan", Attempt: n, Profile: "planner", Inputs: inputs, Standards: []string{"standards/go.md"},
		Respond: interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: spec})}
}

// buildStarts is the director-grill workflow's first build of spec, left running.
func buildStarts(spec []byte) interptest.Expectation {
	return interptest.Expectation{Step: "build", Attempt: 1, Profile: "builder", Inputs: map[string]string{"spec": "sha256:" + sum(spec)},
		Standards: []string{"standards/go.md"}, Skills: []string{"go-tdd", "guildhall-contributing"}, Respond: interptest.Hang()}
}

func specV2(t *testing.T) []byte {
	data, err := os.ReadFile(filepath.Join(interptest.Testdata(), "docs", "spec-v2.md"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// bound returns attempt n of step's binding as "<port> <sha256>" lines, in binding order.
func (e *env) bound(t *testing.T, step string, n int) []string {
	t.Helper()
	d, ok := e.documents(t)[step+"-"+strconv.Itoa(n)+"-inputs"]
	if !ok {
		t.Fatalf("attempt %d of %s has no binding", n, step)
	}
	data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(d.Path)))
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Inputs []struct{ Port, SHA256 string }
	}
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, in := range b.Inputs {
		out = append(out, in.Port+" "+in.SHA256)
	}
	return out
}

// rounds is interp.DesignRounds of app/t7's documents.
func (e *env) rounds(t *testing.T) int {
	t.Helper()
	docs, err := e.h.Records.Documents("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	return interp.DesignRounds(docs)
}

// counts is how many attempts, documents and decisions app/t7 has recorded.
func (e *env) counts(t *testing.T) [3]int {
	t.Helper()
	ds, err := e.h.Records.Decisions("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	return [3]int{len(e.attempts(t)), len(e.documents(t)), len(ds)}
}

// grillHeld grills the director-grill task, then turns the admission hold on, so research waits paused.
func (e *env) grillHeld(t *testing.T) {
	t.Helper()
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1})
	e.eng.Hold = func() (bool, error) { return true, nil }
	finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitPaused, Step: "research"})
}

func (e *env) release() { e.eng.Hold = nil }

// TestNoDesignRoundRunsAsBefore: a feature task with no design round records no design document and passes no design
// input to plan.
func TestNoDesignRoundRunsAsBefore(t *testing.T) {
	e := newEnvDefinition(t, backends[0], loadWorkflow(t, "director-grill"), researchExpectation(researchSort), gplanExpectation(1, nil, specV1(t)))
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
	for name := range e.documents(t) {
		if strings.HasPrefix(name, "design") {
			t.Errorf("document %s recorded with no design round", name)
		}
	}
	if got := interp.DesignRounds(nil); got != 0 {
		t.Errorf("DesignRounds = %d", got)
	}
}

// TestDesignRoundAfterTheGrill: a round requested after the grill, while a dependency is open, holds its designer at
// the dependency gate, recording nothing for it; once the dependency is done the designer runs under the designer
// profile at effort high, given the brief and the grill notes, and its design is asked as decision design-1, bound to
// the design's digest. approved lets research run, which the designer did not let past the gate, then plan runs with
// the design and build follows.
func TestDesignRoundAfterTheGrill(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := loadWorkflow(t, "director-grill")
			e := newDependentEnv(t, b, def, nil, designerExpectation("designer", 1, designBrief, designOne), researchExpectation(researchSort),
				gplanExpectation(1, designOne, specV1(t)), buildStarts(specV1(t)))
			want(t, e.grilled(t), heldAt)

			designerHeld := interp.Next{Wait: interp.WaitDependencies, Step: "designer"}
			want(t, e.advance(t, designRequested(designBrief)), designerHeld)
			if got := e.stepsAttempted(t); !slices.Equal(got, []string{"grill"}) {
				t.Fatalf("attempts = %v, want the designer held", got)
			}
			if _, ok := e.documents(t)["design-1-brief"]; !ok {
				t.Fatal("the request recorded no brief")
			}
			before := e.counts(t)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), designerHeld)
			if after := e.counts(t); after != before {
				t.Errorf("a held round recorded %v, had %v", after, before)
			}

			e.closeTask(t, "app", "t3", store.TaskDone)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
			if l := e.script.Launched(); len(l) != 1 || l[0].Profile != "designer" || l[0].Settings.Effort != "high" {
				t.Fatalf("launched %+v, want the designer at effort high", l)
			}
			wantBound := []string{"brief " + sum([]byte(designBrief)), "context " + sum(grillNotes)}
			if got := e.bound(t, "designer", 1); !slices.Equal(got, wantBound) {
				t.Errorf("designer 1 binding = %q, want %q", got, wantBound)
			}
			if q, err := e.h.Records.StartQueue(); err != nil || len(q) != 0 {
				t.Errorf("start queue = %+v, %v", q, err)
			}

			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
			d := e.decision(t, "design-1")
			if d.Kind != store.DecisionApproval || d.Subject == nil || d.Subject.Kind != store.SubjectDocument || d.Subject.Digest != "sha256:"+sum(designOne) ||
				!slices.Equal(d.Options, []string{"approved", "changes_requested"}) {
				t.Errorf("design-1 = %+v, want an approval of the design", d)
			}
			if _, ok := e.messages(t)["app.t7.decision.design-1"]; !ok {
				t.Errorf("messages = %v, want the design-1 decision message", e.messages(t))
			}

			e.answer(t, "design-1", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "design-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			if d := e.documents(t)["design-1-design"]; d.SHA256 != sum(designOne) {
				t.Errorf("design-1-design = %+v, want the approved design", d)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			wantPlan := []string{"context " + sum(grillNotes), "context " + sum(researchSort), "design " + sum(designOne)}
			if got := e.bound(t, "plan", 1); !slices.Equal(got, wantPlan) {
				t.Errorf("plan 1 binding = %q, want %q", got, wantPlan)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			e.answer(t, "accept-1", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			if got := e.rounds(t); got != 1 {
				t.Errorf("DesignRounds = %d, want 1", got)
			}
		})
	}
}

// TestDesignRoundAfterAnAcceptedPlan: a round requested once accept-1 is approved but before build has started runs
// the designer with the grill notes, research notes and spec as context. approved makes plan stale, so plan runs again
// with the design, accept is asked again on the new spec's digest and build follows it; research and the grill are
// not run again.
func TestDesignRoundAfterAnAcceptedPlan(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), researchExpectation(researchSort), gplanExpectation(1, nil, specV1(t)),
				designerExpectation("designer", 1, designBrief, designOne), gplanExpectation(2, designOne, specV2(t)), buildStarts(specV2(t)))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			e.answer(t, "accept-1", "approved")

			// The Director asks for a design instead of starting build.
			want(t, e.advance(t, designRequested(designBrief)), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
			wantBound := []string{"brief " + sum([]byte(designBrief)), "context " + sum(grillNotes), "context " + sum(researchSort), "context " + sum(specV1(t))}
			if got := e.bound(t, "designer", 1); !slices.Equal(got, wantBound) {
				t.Errorf("designer 1 binding = %q, want %q", got, wantBound)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
			e.answer(t, "design-1", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "design-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-2"})
			if d := e.decision(t, "accept-2"); d.Subject == nil || d.Subject.Digest != "sha256:"+sum(specV2(t)) {
				t.Errorf("accept-2 bound to %+v, want the new spec", d.Subject)
			}
			e.answer(t, "accept-2", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-2"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			if got := slices.Sorted(slices.Values(e.stepsAttempted(t))); !slices.Equal(got, []string{"build", "designer", "grill", "plan", "plan", "research"}) {
				t.Errorf("attempts = %v, want research and the grill once", got)
			}
		})
	}
}

// TestDesignRoundWhileAcceptanceOpen: a round requested while accept-1 is still open, then approved, makes plan run
// again; when the walk next reaches accept, the open accept-1 bound to the old spec is voided as superseded and
// accept-2 is asked on the new spec's digest. A late answer to accept-1 is refused, and a repeated advance writes
// nothing again.
func TestDesignRoundWhileAcceptanceOpen(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), researchExpectation(researchSort), gplanExpectation(1, nil, specV1(t)),
				designerExpectation("designer", 1, designBrief, designOne), gplanExpectation(2, designOne, specV2(t)), buildStarts(specV2(t)))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})

			// accept-1 is left open; the Director asks for a design.
			want(t, e.advance(t, designRequested(designBrief)), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
			e.answer(t, "design-1", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "design-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-2"})

			v2 := "sha256:" + sum(specV2(t))
			if d := e.decision(t, "accept-2"); d.Subject == nil || d.Subject.Digest != v2 || !d.Open() {
				t.Errorf("accept-2 = %+v bound to %+v, want open on the new spec", d, d.Subject)
			}
			stale := e.decision(t, "accept-1")
			if !stale.Void || stale.Open() || !strings.HasPrefix(stale.VoidReason, "Superseded: the spec changed to "+v2) || !strings.Contains(stale.VoidReason, "accept-2") {
				t.Errorf("accept-1 = %+v, want void as superseded by accept-2 on %s", stale, v2)
			}
			if _, err := e.h.AnswerDecision("answer:late", "app", "t7", "accept-1", "approved"); err == nil {
				t.Error("answering the superseded accept-1 succeeded")
			}
			if _, err := e.eng.Advance(context.Background(), interp.Event{Kind: interp.DecisionAnswered, Repository: "app", Task: "t7", Decision: "accept-1"}); err == nil {
				t.Error("DecisionAnswered for the superseded accept-1 was accepted")
			}

			// Replay: advancing again finds the void and accept-2 recorded, and writes nothing.
			decisions, err := e.h.Records.Decisions("app", "t7")
			if err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-2"})
			again, err := e.h.Records.Decisions("app", "t7")
			if err != nil {
				t.Fatal(err)
			}
			if len(again) != len(decisions) {
				t.Errorf("decisions after replay = %+v, want %+v", again, decisions)
			}

			e.answer(t, "accept-2", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-2"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
		})
	}
}

// TestDesignRoundAfterResearch: a round requested while plan's first start is held runs before plan, given the
// grill and research notes; once approved, plan's first attempt is given the design.
func TestDesignRoundAfterResearch(t *testing.T) {
	e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"), researchExpectation(researchSort),
		designerExpectation("designer", 1, designBrief, designOne), gplanExpectation(1, designOne, specV1(t)))
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1})
	e.eng.Hold = func() (bool, error) { return true, nil }
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitPaused, Step: "plan"})
	want(t, e.advance(t, designRequested(designBrief)), interp.Next{Wait: interp.WaitPaused, Step: "designer"})
	e.release()
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
	if got, w := e.bound(t, "designer", 1), []string{"brief " + sum([]byte(designBrief)), "context " + sum(grillNotes), "context " + sum(researchSort)}; !slices.Equal(got, w) {
		t.Errorf("designer 1 binding = %q, want %q", got, w)
	}
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1})
	e.answer(t, "design-1", "approved")
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "design-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
}

// TestDesignChangesRequestedReopensTheGrill: changes_requested hands grill attempt 2 to the Director with the answer
// as feedback, its notes as previous and the design as reviewed. A second round, requested once the grill has
// finished again, becomes design-2 under designer attempt 2 and asks again on the new design's digest.
func TestDesignChangesRequestedReopensTheGrill(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"),
				designerExpectation("designer", 1, designBrief, designOne), designerExpectation("designer", 2, designBrief2, designTwo))
			e.grillHeld(t)
			want(t, e.advance(t, designRequested(designBrief)), interp.Next{Wait: interp.WaitPaused, Step: "designer"})
			e.release()
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1})

			words := "changes_requested\nShow the sort options as a table."
			e.answer(t, "design-1", words)
			grill2 := interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 2}
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "design-1"}), grill2)
			wantInputs := []string{"feedback sha256:" + sum([]byte(words)), "previous sha256:" + sum(grillNotes), "reviewed sha256:" + sum(designOne)}
			var bound []string
			for _, in := range e.stageManifest(t, "grill", 2).Inputs {
				bound = append(bound, in.Port+" "+in.Digest)
			}
			if !slices.Equal(bound, wantInputs) {
				t.Errorf("grill 2 inputs = %q, want %q", bound, wantInputs)
			}
			if _, ok := e.documents(t)["design-1-design"]; ok {
				t.Error("a design asked changes to was recorded as approved")
			}
			err := advanceErr(t, e, designRequested(designBrief2))
			if !errors.Is(err, interp.ErrDesignRound) || !strings.Contains(err.Error(), "still running") {
				t.Errorf("a round while the grill runs = %v, want it refused", err)
			}

			notes2 := []byte("# Grill notes\n\n1. Sort by due date, shown as a table.\n")
			e.eng.Hold = func() (bool, error) { return true, nil }
			finishStage(t, e, "grill", 2, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: notes2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 2}), interp.Next{Wait: interp.WaitPaused, Step: "research"})
			want(t, e.advance(t, designRequested(designBrief2)), interp.Next{Wait: interp.WaitPaused, Step: "designer"})
			e.release()
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 2})
			if got, w := e.bound(t, "designer", 2), []string{"brief " + sum([]byte(designBrief2)), "context " + sum(notes2)}; !slices.Equal(got, w) {
				t.Errorf("designer 2 binding = %q, want %q", got, w)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-2"})
			if d := e.decision(t, "design-2"); d.Subject == nil || d.Subject.Digest != "sha256:"+sum(designTwo) {
				t.Errorf("design-2 bound to %+v, want the new design", d.Subject)
			}
			if got := e.rounds(t); got != 2 {
				t.Errorf("DesignRounds = %d, want 2", got)
			}
		})
	}
}

// TestDesignSameBriefAfterChangesRequested: a request repeating the brief of a round already answered is not a
// replay: once design-1 is answered changes_requested and grill 2 has succeeded, the same brief records design-2's
// brief, runs designer attempt 2 and asks design-2.
func TestDesignSameBriefAfterChangesRequested(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"),
				designerExpectation("designer", 1, designBrief, designOne), designerExpectation("designer", 2, designBrief, designTwo))
			e.grillHeld(t)
			e.release()
			want(t, e.advance(t, designRequested(designBrief)), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1})
			e.answer(t, "design-1", "changes_requested\nShow the sort options as a table.")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "design-1"}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 2})

			notes2 := []byte("# Grill notes\n\n1. Sort by due date, shown as a table.\n")
			e.eng.Hold = func() (bool, error) { return true, nil }
			finishStage(t, e, "grill", 2, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: notes2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 2}), interp.Next{Wait: interp.WaitPaused, Step: "research"})
			e.release()
			want(t, e.advance(t, designRequested(designBrief)), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 2})
			if d, ok := e.documents(t)["design-2-brief"]; !ok || d.SHA256 != sum([]byte(designBrief)) {
				t.Errorf("design-2-brief = %+v, %v; want the repeated brief recorded as round 2's", d, ok)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-2"})
			if d := e.decision(t, "design-2"); d.Subject == nil || d.Subject.Digest != "sha256:"+sum(designTwo) {
				t.Errorf("design-2 bound to %+v, want the new design", d.Subject)
			}
			if got := e.rounds(t); got != 2 {
				t.Errorf("DesignRounds = %d, want 2", got)
			}
		})
	}
}

// TestDesignRetryStaysInTheRound: a failed designer attempt waits for the Director; its step retry starts designer
// attempt 2 within round 1, whose decision is still design-1.
func TestDesignRetryStaysInTheRound(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			failed := designerExpectation("designer", 1, designBrief, nil)
			failed.Respond = interptest.Fail("no design")
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), failed, designerExpectation("designer", 2, designBrief, designOne))
			e.grillHeld(t)
			e.release()
			want(t, e.advance(t, designRequested(designBrief)), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "designer", Attempt: 1})
			if _, ok := e.messages(t)["app.t7.attempt-failed.designer-1"]; !ok {
				t.Errorf("messages = %v, want designer 1's failure", e.messages(t))
			}
			want(t, e.advance(t, retried("designer", 1, "try again")), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 2})
			if got := e.bound(t, "designer", 2); len(got) == 0 || got[0] != "brief "+sum([]byte(designBrief)) {
				t.Errorf("designer 2 binding = %q, want round 1's brief", got)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
			if got := e.rounds(t); got != 1 {
				t.Errorf("DesignRounds = %d, want 1", got)
			}
		})
	}
}

// TestDesignRoundStepIDsAvoidTheWorkflow: a workflow with steps named design and designer still validates, and its
// rounds run under round-design and round-designer, whose documents and decisions parse back to them.
func TestDesignRoundStepIDsAvoidTheWorkflow(t *testing.T) {
	def := loadWorkflow(t, "director-grill")
	def.Steps[1].ID = "design"
	def.Steps[2].Inputs["context"] = []string{"grill.notes", "design.notes"}
	def.Steps[5].ID = "designer"
	def.Steps[7].Inputs["review"] = []string{"designer.outcome", "sign-off.outcome"}
	if err := workflow.Validate(def); err != nil {
		t.Fatalf("a workflow with design and designer steps = %v, want it valid", err)
	}
	if d, a := interp.DesignSteps(def); d != "round-design" || a != "round-designer" {
		t.Fatalf("DesignSteps = %s, %s", d, a)
	}
	research := researchExpectation(researchSort)
	research.Step = "design"
	e := newEnvDefinition(t, backends[1], def, designerExpectation("round-designer", 1, designBrief, designOne), research)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.eng.Hold = func() (bool, error) { return true, nil }
	finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitPaused, Step: "design"})
	e.release()
	want(t, e.advance(t, designRequested(designBrief)), interp.Next{Wait: interp.WaitAttempt, Step: "round-designer", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "round-designer", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "round-design", Decision: "round-design-1"})
	e.answer(t, "round-design-1", "approved")
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "round-design-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "design", Attempt: 1})
	docs := e.documents(t)
	for _, name := range []string{"round-design-1-brief", "round-designer-1-inputs", "round-designer-1-design", "round-design-1-design"} {
		if _, ok := docs[name]; !ok {
			t.Errorf("no document %s", name)
		}
	}
	if docs["round-design-1-design"].SHA256 != sum(designOne) {
		t.Errorf("round-design-1-design = %+v", docs["round-design-1-design"])
	}
	if got := e.rounds(t); got != 1 {
		t.Errorf("DesignRounds = %d, want 1", got)
	}
}

// TestDesignRequestRefusals: each refusal records nothing.
func TestDesignRequestRefusals(t *testing.T) {
	refused := func(t *testing.T, e *env, brief, reason string, target error) {
		t.Helper()
		before := e.counts(t)
		err := advanceErr(t, e, designRequested(brief))
		if !errors.Is(err, target) || !strings.Contains(err.Error(), reason) {
			t.Errorf("request = %v, want %v naming %q", err, target, reason)
		}
		if after := e.counts(t); after != before {
			t.Errorf("a refused request recorded %v, had %v", after, before)
		}
	}
	t.Run("bug", func(t *testing.T) {
		e := newEnvDefinition(t, backends[1], loadWorkflow(t, "no-acceptance"))
		refused(t, e, designBrief, "bugs have no design round", interp.ErrDesignRound)
	})
	t.Run("running", func(t *testing.T) {
		e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"))
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		refused(t, e, designBrief, "grill attempt 1 is still running", interp.ErrDesignRound)
	})
	t.Run("grill not succeeded", func(t *testing.T) {
		e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"))
		refused(t, e, designBrief, "the grill has not succeeded", interp.ErrDesignRound)
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		finishStage(t, e, "grill", 1, adapter.StatusFailed)
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1})
		refused(t, e, designBrief, "the grill has not succeeded", interp.ErrDesignRound)
	})
	t.Run("empty brief", func(t *testing.T) {
		e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"))
		e.grillHeld(t)
		refused(t, e, " \n", "nonempty brief", interp.ErrEvent)
	})
	t.Run("open round", func(t *testing.T) {
		e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"), designerExpectation("designer", 1, designBrief, designOne))
		e.grillHeld(t)
		e.release()
		e.advance(t, designRequested(designBrief))
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1})
		refused(t, e, designBrief2, "design round 1 is already open", interp.ErrDesignRound)
		// The same brief is a replay: it records nothing and the round still waits for its decision.
		before := e.counts(t)
		want(t, e.advance(t, designRequested(designBrief)), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
		if after := e.counts(t); after != before {
			t.Errorf("a replayed request recorded %v, had %v", after, before)
		}
	})
	t.Run("build started", func(t *testing.T) {
		e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"), researchExpectation(researchSort), gplanExpectation(1, nil, specV1(t)), buildStarts(specV1(t)))
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
		e.answer(t, "accept-1", "approved")
		want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
		if _, err := e.h.Records.FinishAttempt("finish:build", "app", "t7", "build", 1, store.AttemptStopped); err != nil {
			t.Fatal(err)
		}
		refused(t, e, designBrief, "build has started", interp.ErrDesignRound)
	})
	t.Run("cancelled", func(t *testing.T) {
		e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"))
		e.grillHeld(t)
		e.closeTask(t, "app", "t7", store.TaskCancelled)
		refused(t, e, designBrief, "cancelled", interp.ErrTaskClosed)
	})
	t.Run("done", func(t *testing.T) {
		e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"))
		e.grillHeld(t)
		e.closeTask(t, "app", "t7", store.TaskDone)
		refused(t, e, designBrief, "done", interp.ErrTaskClosed)
	})
}

// TestDesignRoundReplaysACrashedLaunch: a designer recorded but never launched is launched again with the same identity
// by the next advance, never recorded again, and its decision is opened once.
func TestDesignRoundReplaysACrashedLaunch(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), designerExpectation("designer", 1, designBrief, designOne))
			e.grillHeld(t)
			e.release()
			e.eng.Launcher = &crashingLauncher{Launcher: e.script}
			if err := advanceErr(t, e, designRequested(designBrief)); err == nil {
				t.Fatal("Advance succeeded through a crashed launch")
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
			if got := slices.Sorted(slices.Values(e.stepsAttempted(t))); !slices.Equal(got, []string{"designer", "grill"}) || len(e.script.Launched()) != 1 {
				t.Fatalf("attempts = %v, %d launches; want designer 1 launched once", got, len(e.script.Launched()))
			}
			open := interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), open)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), open)
			ds, err := e.h.Records.Decisions("app", "t7")
			if err != nil || len(ds) != 1 {
				t.Errorf("decisions = %+v, %v; want design-1 once", ds, err)
			}
		})
	}
}

// TestDesignRoundReplays: a replayed request records nothing new; repeated advances while the designer runs and while
// its decision is open launch, record and ask nothing again; and a designer the running cap does not admit waits in the
// start queue under a design request, then starts once a slot is free.
func TestDesignRoundReplays(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), designerExpectation("designer", 1, designBrief, designOne))
			e.grillHeld(t)
			e.release()
			e.eng.Caps = store.AttemptCaps{MaxRunning: 1, MaxPerDay: 40}
			occupy(t, e, "build")
			queued := interp.Next{Wait: interp.WaitQueued, Step: "designer", Attempt: 1, Position: 1}
			want(t, e.advance(t, designRequested(designBrief)), queued)
			q, err := e.h.Records.StartQueue()
			if err != nil || len(q) != 1 || !q[0].Is("app", "t7", "designer") || q[0].Kind != store.StartByDesign {
				t.Fatalf("StartQueue = %+v, %v; want the designer queued under a design request", q, err)
			}
			before := e.counts(t)
			want(t, e.advance(t, designRequested(designBrief)), queued)
			if after := e.counts(t); after != before {
				t.Errorf("a replayed request recorded %v, had %v", after, before)
			}
			if _, err := e.h.Records.FinishAttempt("finish:build", "app", "t8", "build", 1, store.AttemptFailed); err != nil {
				t.Fatal(err)
			}
			running := interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), running)
			before = e.counts(t)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), running)
			if after := e.counts(t); after != before || len(e.script.Launched()) != 1 {
				t.Errorf("replay while the designer runs: %v records, %d launches; want %v, 1", after, len(e.script.Launched()), before)
			}
			open := interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), open)
			before = e.counts(t)
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), open)
			want(t, e.advance(t, designRequested(designBrief)), open)
			if after := e.counts(t); after != before || len(e.script.Launched()) != 1 {
				t.Errorf("replay of an open round: %v records, %d launches; want %v, 1", after, len(e.script.Launched()), before)
			}
		})
	}
}
