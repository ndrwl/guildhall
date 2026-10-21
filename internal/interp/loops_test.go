package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Heads the scripted builds land in later rounds.
var (
	head2 = strings.Repeat("1", 40)
	head3 = strings.Repeat("2", 40)
)

func loadWorkflow(t *testing.T, name string) *workflow.Definition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(interptest.Testdata(), "workflows", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Validate(def); err != nil {
		t.Fatal(err)
	}
	return def
}

func headFile(h string) []byte { return []byte(h + "\n") }

func specDigest(t *testing.T) string { return "sha256:" + sum(specV1(t)) }

// bound is an automated code review outcome bound to head h, the accepted spec and the engine's standards.
func bound(t *testing.T, v workflow.Verdict, h string, findings []workflow.Finding, dispositions ...workflow.Disposition) workflow.ReviewOutcome {
	return workflow.ReviewOutcome{
		Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.AutomatedReviewer, Verdict: v,
		Findings: findings, Dispositions: dispositions,
		Revisions: workflow.Revisions{Subject: workflow.Subject{CodeHead: h}, SpecDigest: specDigest(t), StandardsRevision: "standards-1"},
	}
}

// outcomeDigest is the digest of the outcome file interptest.Review writes for o.
func outcomeDigest(o workflow.ReviewOutcome) string {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		panic(err)
	}
	return "sha256:" + sum(append(data, '\n'))
}

func buildExp(t *testing.T, n int, h string, feedback string) interptest.Expectation {
	inputs := map[string]string{"spec": specDigest(t)}
	if feedback != "" {
		inputs["feedback"] = feedback
	}
	return interptest.Expectation{
		Step: "build", Attempt: n, Profile: "builder", Inputs: inputs,
		Standards: []string{"standards/go.md"}, Skills: []string{"go-tdd", "guildhall-contributing"},
		Respond: interptest.Succeed("built", interptest.File{Path: "head", Data: headFile(h)}),
	}
}

func reviewExp(t *testing.T, step, profile string, n int, h string, previous *workflow.ReviewOutcome, o workflow.ReviewOutcome) interptest.Expectation {
	inputs := map[string]string{"spec": specDigest(t), "head": "sha256:" + sum(headFile(h))}
	if previous != nil {
		inputs["previous"] = outcomeDigest(*previous)
	}
	return interptest.Expectation{
		Step: step, Attempt: n, Profile: profile, Inputs: inputs,
		Standards: []string{"standards/go.md", "standards/review.md"}, Respond: interptest.Review(o),
	}
}

// accepted runs plan and the Guildmaster's approval of loop.json or straight.json and returns the next wait.
func (e *env) accepted(t *testing.T) interp.Next {
	t.Helper()
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.answer(t, "accept-1", "approved")
	return e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
}

func (e *env) messages(t *testing.T) map[string]string {
	t.Helper()
	page, err := e.mailbox.List(store.Recipient{Role: store.RecipientDirector}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range page.Messages {
		out[m.ID] = m.Text
	}
	return out
}

func want(t *testing.T, got, want interp.Next) {
	t.Helper()
	if got != want {
		t.Fatalf("next = %+v, want %+v", got, want)
	}
}

// TestRoundTraceThreeReviews is the round trace of report.md §7.1 with the Guildhall repository's three
// code reviews on distinct profiles: review 1 finds f1 (S2) and f2 (S10); build 2 fixes both; review 2
// resolves them and finds f3 (S4) and f4, a suggestion with no rule; build 3 fixes f3; review 3 approves
// with f4 still open; the strict review asks for changes with a suggestion only, which does not block;
// the Astra review approves and the merge decision opens for head 3.
// The trace runs again with step ids that end in -<digits> and -x<digits> beside a step of the same prefix.
func TestRoundTraceThreeReviews(t *testing.T) {
	for _, ids := range [][3]string{{"review-opus", "review-opus-strict", "review-astra"}, {"review", "review-2", "review-x2"}} {
		t.Run(ids[0], func(t *testing.T) { roundTraceThreeReviews(t, ids) })
	}
}

func roundTraceThreeReviews(t *testing.T, ids [3]string) {
	f1 := workflow.Finding{ID: "f1", Rule: "S2", File: "internal/cli/task.go", Line: 40, Summary: "No red test shown."}
	f2 := workflow.Finding{ID: "f2", Rule: "S10", File: "docs/tasks.md", Line: 12, Summary: "Two sentences on one line."}
	f3 := workflow.Finding{ID: "f3", Rule: "S4", File: "docs/tasks.md", Line: 30, Summary: "Claim without acceptance."}
	f4 := workflow.Finding{ID: "f4", File: "internal/cli/task.go", Line: 7, Summary: "Prefer a shorter name."}
	o1 := bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{f1, f2})
	o2 := bound(t, workflow.VerdictChangesRequested, head2, []workflow.Finding{f3, f4},
		workflow.Disposition{Finding: "f1", State: workflow.Resolved}, workflow.Disposition{Finding: "f2", State: workflow.Resolved})
	o3 := bound(t, workflow.VerdictApproved, head3, []workflow.Finding{f4},
		workflow.Disposition{Finding: "f3", State: workflow.Resolved}, workflow.Disposition{Finding: "f4", State: workflow.StillOpen})
	design := workflow.ReviewOutcome{Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.AutomatedReviewer, Verdict: workflow.VerdictApproved,
		Revisions: workflow.Revisions{Subject: workflow.Subject{DocumentDigest: specDigest(t)}, StandardsRevision: "standards-1"}}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, renameSteps(loadWorkflow(t, "three-reviews"), map[string]string{"review-opus": ids[0], "review-opus-strict": ids[1], "review-astra": ids[2]}),
				planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
				interptest.Expectation{Step: "design-review", Attempt: 1, Profile: "reviewer-opus", Inputs: map[string]string{"subject": specDigest(t)},
					Standards: []string{"standards/go.md"}, Respond: interptest.Review(design)},
				buildExp(t, 1, head, ""),
				reviewExp(t, ids[0], "reviewer-opus", 1, head, nil, o1),
				buildExp(t, 2, head2, outcomeDigest(o1)),
				reviewExp(t, ids[0], "reviewer-opus", 2, head2, &o1, o2),
				buildExp(t, 3, head3, outcomeDigest(o2)),
				reviewExp(t, ids[0], "reviewer-opus", 3, head3, &o2, o3),
				reviewExp(t, ids[1], "reviewer-opus-strict", 1, head3, nil,
					bound(t, workflow.VerdictChangesRequested, head3, []workflow.Finding{{ID: "s1", Summary: "Consider a table."}})),
				reviewExp(t, ids[2], "reviewer-astra", 1, head3, nil, bound(t, workflow.VerdictApproved, head3, nil)),
			)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "design-review", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "design-review", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			e.answer(t, "accept-1", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: ids[0], Attempt: 1})

			// Round 1 requests changes citing rules: the build runs again with the outcome as feedback.
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: ids[0], Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: ids[0], Attempt: 2})
			// Round 2 resolves f1 and f2 and finds f3, which cites a rule, and f4, which does not.
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: ids[0], Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 3})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 3}), interp.Next{Wait: interp.WaitAttempt, Step: ids[0], Attempt: 3})
			// Round 3 approves; the second and third reviews run on the same head.
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: ids[0], Attempt: 3}), interp.Next{Wait: interp.WaitAttempt, Step: ids[1], Attempt: 1})
			// A changes_requested verdict with suggestions only does not block.
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: ids[1], Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: ids[2], Attempt: 1})
			next := e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: ids[2], Attempt: 1})
			want(t, next, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if d := e.decision(t, "deliver-1"); d.Subject == nil || d.Subject.Digest != head3 {
				t.Errorf("merge decision bound to %+v, want head %s", d.Subject, head3)
			}
			if got, want := e.inbox(t), []string{"app.t7.decision.accept-1", "app.t7.decision.deliver-1"}; !slices.Equal(got, want) {
				t.Errorf("Director inbox = %q, want %q (no stall: every round made progress)", got, want)
			}

			// Replaying any earlier event records nothing new and launches nothing.
			attempts := len(e.attempts(t))
			for _, ev := range []interp.Event{{Kind: interp.TaskCreated}, {Kind: interp.AttemptResult, Step: ids[0], Attempt: 1}, {Kind: interp.DecisionAnswered, Decision: "accept-1"}} {
				want(t, e.advance(t, ev), next)
			}
			if len(e.attempts(t)) != attempts || len(e.script.Launched()) != 10 {
				t.Errorf("replay changed records: %d attempts, %d launches", len(e.attempts(t)), len(e.script.Launched()))
			}
		})
	}
}

// renameSteps returns def with each step id in ids renamed, with the inputs and loop rules that name it.
func renameSteps(def *workflow.Definition, ids map[string]string) *workflow.Definition {
	name := func(id string) string {
		if to, ok := ids[id]; ok {
			return to
		}
		return id
	}
	for i, s := range def.Steps {
		s.ID = name(s.ID)
		inputs := map[string]workflow.Refs{}
		for port, refs := range s.Inputs {
			for _, ref := range refs {
				from, out, _ := strings.Cut(ref, ".")
				inputs[port] = append(inputs[port], name(from)+"."+out)
			}
		}
		s.Inputs = inputs
		on := map[string]workflow.Rule{}
		for k, rule := range s.On {
			rule.Goto = name(rule.Goto)
			on[k] = rule
		}
		s.On = on
		def.Steps[i] = s
	}
	return def
}

func TestStaleOutcomeIsRefused(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			// The reviewer was given head 1 and reports an outcome bound to head 2.
			e := newEnvDefinition(t, b, loadWorkflow(t, "loop"), planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
				buildExp(t, 1, head, ""), reviewExp(t, "review", "reviewer", 1, head, nil, bound(t, workflow.VerdictApproved, head2, nil)))
			e.accepted(t)
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
			if !errors.Is(err, interp.ErrOutcome) || !strings.Contains(err.Error(), "stale") {
				t.Fatalf("Advance = %v, want ErrOutcome naming a stale outcome", err)
			}
			if _, ok := e.documents(t)["review-1-outcome"]; ok {
				t.Error("a stale outcome was recorded")
			}
			if a := e.attempts(t); a[len(a)-1].Step != "review" || a[len(a)-1].Outcome != "" {
				t.Errorf("attempts = %+v, want the review still running", a)
			}
		})
	}
}

func TestOutcomeMustDispositionEveryPriorFinding(t *testing.T) {
	f1 := workflow.Finding{ID: "f1", Rule: "S2", Summary: "No red test shown."}
	o1 := bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{f1})
	o2 := bound(t, workflow.VerdictApproved, head2, nil)
	e := newEnvDefinition(t, backends[0], loadWorkflow(t, "loop"), planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
		buildExp(t, 1, head, ""), reviewExp(t, "review", "reviewer", 1, head, nil, o1),
		buildExp(t, 2, head2, outcomeDigest(o1)), reviewExp(t, "review", "reviewer", 2, head2, &o1, o2))
	e.accepted(t)
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2})
	err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2})
	if !errors.Is(err, interp.ErrOutcome) || !strings.Contains(err.Error(), `"f1"`) {
		t.Fatalf("Advance = %v, want ErrOutcome naming f1", err)
	}
}

// TestStallSignals: builds that return the same head make the review run again on it; each round below
// the cap that requests changes again is reported to the Director as a stall, and the loop goes on.
func TestStallSignals(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := loadWorkflow(t, "loop")
			def.Steps[3].On["changes_requested"] = workflow.Rule{Goto: "build", Max: 3}
			f1 := workflow.Finding{ID: "f1", Rule: "S2", Summary: "No red test shown."}
			still := workflow.Disposition{Finding: "f1", State: workflow.StillOpen}
			o1 := bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{f1})
			o2 := bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{f1}, still)
			o3 := bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{f1}, still)
			e := newEnvDefinition(t, b, def, planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
				buildExp(t, 1, head, ""), reviewExp(t, "review", "reviewer", 1, head, nil, o1),
				buildExp(t, 2, head, outcomeDigest(o1)), reviewExp(t, "review", "reviewer", 2, head, &o1, o2),
				buildExp(t, 3, head, outcomeDigest(o2)), reviewExp(t, "review", "reviewer", 3, head, &o2, o3),
				buildExp(t, 4, head2, outcomeDigest(o3)),
			)
			e.accepted(t)
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
			// Build 2 returns the unchanged head, so the review runs again on it with its previous outcome.
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 2})
			if _, err := e.h.Records.FinishAttempt("launcher-review-2", "app", "t7", "review", 2, store.AttemptSucceeded); err != nil {
				t.Fatal(err)
			}
			e.eng.Outbox = &interruptedOutbox{Outbox: e.eng.Outbox, fail: true}
			if _, err := e.eng.Advance(context.Background(), interp.Event{Kind: interp.AttemptResult, Repository: "app", Task: "t7", Step: "review", Attempt: 2}); err == nil {
				t.Fatal("expected interrupted stall notification")
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 3})
			msgs := e.messages(t)
			stall, ok := msgs["app.t7.stall.review-2"]
			if !ok || !strings.Contains(stall, "head unchanged") || !strings.Contains(stall, "no finding resolved or withdrawn") || strings.Contains(stall, "two rounds") {
				t.Fatalf("stall message after round 2 = %q (inbox %v)", stall, msgs)
			}
			if _, ok := msgs["app.t7.stall.review-1"]; ok {
				t.Error("round 1 reported as a stall")
			}
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 3})
			// A stall is information only: the loop continues to build 4.
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 3}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 4})
			if stall := e.messages(t)["app.t7.stall.review-3"]; !strings.Contains(stall, "f1 still open for two rounds") {
				t.Errorf("stall message after round 3 = %q", stall)
			}
		})
	}
}

// TestMaxOpensLoopExit: exhausting max opens a loop-exit decision bound to the head; continue grants
// one more round, accept records the Guildmaster's approval as the step's outcome, which satisfies the
// review gate, and reject leaves the task to the Director.
func TestMaxOpensLoopExit(t *testing.T) {
	f1 := workflow.Finding{ID: "f1", Rule: "S2", Summary: "No red test shown."}
	still := workflow.Disposition{Finding: "f1", State: workflow.StillOpen}
	o1 := bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{f1})
	o2 := bound(t, workflow.VerdictChangesRequested, head2, []workflow.Finding{f1}, still)
	o3 := bound(t, workflow.VerdictChangesRequested, head3, []workflow.Finding{f1}, still)
	exps := func(t *testing.T) []interptest.Expectation {
		return []interptest.Expectation{
			planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
			buildExp(t, 1, head, ""), reviewExp(t, "review", "reviewer", 1, head, nil, o1),
			buildExp(t, 2, head2, outcomeDigest(o1)), reviewExp(t, "review", "reviewer", 2, head2, &o1, o2),
		}
	}
	toExit := func(t *testing.T, e *env) {
		e.accepted(t)
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
		want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2})
		want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "review", Decision: "review-x2"})
		d := e.decision(t, "review-x2")
		if d.Kind != store.DecisionLoopExit || d.Subject == nil || *d.Subject != (store.Subject{Kind: store.SubjectHead, Digest: head2}) ||
			!slices.Equal(d.Options, []string{"continue", "accept", "reject"}) {
			t.Fatalf("loop-exit decision = %+v", d)
		}
		if _, ok := e.messages(t)["app.t7.decision.review-x2"]; !ok {
			t.Error("loop-exit decision not sent to the Director")
		}
	}
	for _, b := range backends {
		t.Run(b.name+"/continue then accept", func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "loop"), append(exps(t),
				buildExp(t, 3, head3, outcomeDigest(o2)), reviewExp(t, "review", "reviewer", 3, head3, &o2, o3))...)
			toExit(t, e)
			e.answer(t, "review-x2", "continue\nOne more round.")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-x2"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 3})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 3})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 3}), interp.Next{Wait: interp.WaitDecision, Step: "review", Decision: "review-x3"})
			e.answer(t, "review-x3", "accept")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-x3"}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			doc := e.documents(t)["review-x3-outcome"]
			data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(doc.Path)))
			if err != nil {
				t.Fatal(err)
			}
			o, err := workflow.ParseReviewOutcome(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := o.BoundTo(bound(t, "", head3, nil).Revisions); err != nil || o.Reviewer != workflow.HumanReviewer || o.Verdict != workflow.VerdictApproved {
				t.Errorf("accepted outcome = %+v: %v", o, err)
			}
		})
		t.Run(b.name+"/reject", func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "loop"), exps(t)...)
			toExit(t, e)
			e.answer(t, "review-x2", "reject\nStart again from the plan.")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "review-x2"}), interp.Next{Wait: interp.WaitDirector, Step: "review", Decision: "review-x2"})
			if _, ok := e.messages(t)["app.t7.loop-exit-rejected.review-x2"]; !ok {
				t.Errorf("Director not told of the rejection: %v", e.messages(t))
			}
		})
	}
}

// TestGuildmasterChangesLoopToPlan: the Guildmaster's changes_requested answer sends the plan back with
// the answer as feedback, and the approval is asked again for the new spec.
func TestGuildmasterChangesLoopToPlan(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			words := "changes_requested\nTighten the scope."
			v2, err := os.ReadFile(filepath.Join(interptest.Testdata(), "docs", "spec-v2.md"))
			if err != nil {
				t.Fatal(err)
			}
			plan2 := interptest.Expectation{Step: "plan", Attempt: 2, Profile: "planner", Inputs: map[string]string{"feedback": "sha256:" + sum([]byte(words))},
				Standards: []string{"standards/go.md"}, Respond: interptest.Succeed("replanned", interptest.File{Path: "spec.md", Data: v2})}
			e := newEnvDefinition(t, b, loadWorkflow(t, "loop"), planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})), plan2)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			e.answer(t, "accept-1", words)
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-2"})
			if d := e.decision(t, "accept-2"); d.Subject == nil || d.Subject.Digest != "sha256:"+sum(v2) {
				t.Errorf("second approval bound to %+v, want spec v2", d.Subject)
			}
		})
	}
}

func TestChangesWithoutALoopRuleWaitForTheDirector(t *testing.T) {
	exps := straightScript(t)
	exps[2].Respond = interptest.Review(bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{{ID: "f1", Rule: "S2", Summary: "No red test shown."}}))
	e := newEnv(t, backends[0], exps...)
	e.accepted(t)
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "review", Attempt: 1})
	if _, ok := e.messages(t)["app.t7.changes-requested.review-1"]; !ok {
		t.Errorf("Director not told: %v", e.messages(t))
	}
}

// TestReviewGateAtDeliver: deliver refuses a review outcome that is not bound to the current standards
// revision, even when every review step has run.
func TestReviewGateAtDeliver(t *testing.T) {
	def := straight(t)
	def.Steps = slices.Delete(def.Steps, 3, 4) // drop the Artisan review: only the Guildmaster's sign-off remains
	def.Steps[4].Inputs["review"] = workflow.Refs{"sign-off.outcome"}
	e := newEnvDefinition(t, backends[0], def, straightScript(t)[:2]...)
	e.accepted(t)
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "sign-off", Decision: "sign-off-1"})
	e.answer(t, "sign-off-1", "approved")
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	e.eng.StandardsRevision = "standards-2"
	if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"}); !errors.Is(err, interp.ErrReviewGate) {
		t.Fatalf("Advance = %v, want ErrReviewGate", err)
	}
}
