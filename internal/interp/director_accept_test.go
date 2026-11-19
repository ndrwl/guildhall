package interp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// smallPlan is a one-slice plan with no open questions that touches nothing sensitive, about 120 lines in 4 files.
const smallPlan = "# Sort the task list\n\n## Summary\n\n" +
	"- **Goal:** Sort the task list by priority.\n" +
	"- **Size:** about 120 non-test lines in 4 files.\n" +
	"- **Touches:** none.\n" +
	"- **Open questions:** None.\n\n" +
	"## Goal\n\nSort it.\n\n## Open questions\n\nNone.\n"

// onAccept is a Director acceptance threshold of 200 lines and 8 files under the default budget.
var onAccept = config.DirectorAccept{Lines: 200, Files: 8, SetLines: 200, SetFiles: 8, BudgetLines: 400, BudgetFiles: 15}

// TestDirectorAcceptable: one case per rule says no and names its reason; the fully eligible case says yes.
func TestDirectorAcceptable(t *testing.T) {
	plan := func(old, new string) []byte { return []byte(strings.Replace(smallPlan, old, new, 1)) }
	eligible := interp.AcceptCase{Accept: onAccept, Grill: interp.AcceptGrillUnattended, Spec: []byte(smallPlan)}
	for _, c := range []struct {
		name   string
		edit   func(*interp.AcceptCase)
		reason string
	}{
		{"setting off", func(c *interp.AcceptCase) { c.Accept = config.DirectorAccept{} }, "are not both set"},
		{"half set", func(c *interp.AcceptCase) { c.Accept = config.DirectorAccept{SetLines: 200} }, "are not both set"},
		{"live grill", func(c *interp.AcceptCase) { c.Grill = interp.AcceptGrillLive }, "the grill is live"},
		{"no grill", func(c *interp.AcceptCase) { c.Grill = interp.AcceptGrillNone }, "the workflow has no grill"},
		{"two slices", func(c *interp.AcceptCase) {
			c.Spec = plan("## Open questions\n\nNone.\n", "## Slices\n\n1. **One**: the first.\n2. **Two**: the second.\n\n## Open questions\n\nNone.\n")
		}, "it has 2 slices"},
		{"open questions", func(c *interp.AcceptCase) {
			c.Spec = plan("## Open questions\n\nNone.\n", "## Open questions\n\n**Q1. Which key?**\nOptions: (a) priority; (b) id.\nRecommended: (a). It is asked for.\n")
		}, "it has 1 open questions"},
		{"no open questions section", func(c *interp.AcceptCase) { c.Spec = plan("## Open questions\n\nNone.\n", "") }, "its open questions do not read"},
		{"lines over the key", func(c *interp.AcceptCase) { c.Spec = plan("about 120 non-test", "about 250 non-test") },
			"about 250 non-test lines is over director_accept_lines = 200"},
		{"files over the key", func(c *interp.AcceptCase) { c.Spec = plan("in 4 files", "in 9 files") }, "9 files is over director_accept_files = 8"},
		{"lines over the budget", func(c *interp.AcceptCase) {
			c.Accept = config.DirectorAccept{Lines: 100, Files: 8, SetLines: 300, SetFiles: 8, BudgetLines: 100, BudgetFiles: 15}
		}, "about 120 non-test lines is over quality_size_budget_lines = 100, the repository's size budget, which caps director_accept_lines = 300"},
		{"touches", func(c *interp.AcceptCase) { c.Spec = plan("**Touches:** none.", "**Touches:** secrets.") }, "it touches secrets"},
		{"task override", func(c *interp.AcceptCase) { c.RequiresGuildmaster = true }, "accept.requires_guildmaster keeps it with the Guildmaster"},
		{"missing size", func(c *interp.AcceptCase) { c.Spec = plan("- **Size:** about 120 non-test lines in 4 files.\n", "") }, "its size does not read"},
		{"free-text size", func(c *interp.AcceptCase) { c.Spec = plan("about 120 non-test lines", "about 120 lines") }, "its size does not read"},
		{"missing touches", func(c *interp.AcceptCase) { c.Spec = plan("- **Touches:** none.\n", "") }, "its touches do not read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := eligible
			c.edit(&in)
			v := interp.DirectorAcceptable(in)
			if v.OK || !strings.Contains(strings.Join(v.Reasons, "\n"), c.reason) {
				t.Errorf("verdict = %+v, want no, with %q", v, c.reason)
			}
		})
	}
	for _, grill := range []interp.AcceptGrill{interp.AcceptGrillUnattended, interp.AcceptGrillArtisan} {
		in := eligible
		in.Grill = grill
		if v := interp.DirectorAcceptable(in); !v.OK || !strings.HasPrefix(v.String(), "the Director may accept it: the grill was "+string(grill)) {
			t.Errorf("%s: verdict = %+v (%s), want yes", grill, v, v)
		}
	}
	one := eligible
	one.Spec = []byte(strings.Replace(smallPlan, "## Open questions\n\nNone.\n", "## Slices\n\n1. **One**: the only one.\n\n## Open questions\n\nNone.\n", 1))
	if v := interp.DirectorAcceptable(one); !v.OK {
		t.Errorf("one listed slice: verdict = %+v, want yes", v)
	}
}

// acceptEnv is the director-grill workflow run unattended up to accept-1 with spec as the plan's, in a repository
// whose Director acceptance keys are lines and files; later are the mock agent's expectations after the plan.
func acceptEnv(t *testing.T, b backend, spec string, lines, files int, later ...interptest.Expectation) *env {
	t.Helper()
	def := loadWorkflow(t, "director-grill")
	notes := []byte("# Grill notes\n\n## Questions\n\nNone.\n")
	plan := planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: []byte(spec)}))
	plan.Inputs = map[string]string{"context": interptest.AnyDigest}
	e := newEnvDefinition(t, b, def, append([]interptest.Expectation{unattendedGrill(1, interptest.Succeed("grilled", interptest.File{Path: "notes.md", Data: notes})),
		researchExpectation([]byte("notes\n")), plan}, later...)...)
	e.eng.Config.Repositories[0].DirectorAcceptLines, e.eng.Config.Repositories[0].DirectorAcceptFiles = lines, files
	e.setGrill(t, workflow.GrillUnattended, store.GrillModeByDirector)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "grill", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
	return e
}

// TestDirectorAcceptanceCommand: Engine.DirectorAcceptance judges an open accept-1 as its message did; an answer the
// Director records passes the spec gate and build runs, after which the answered decision is
// no longer one it may judge. An unknown decision is not found.
func TestDirectorAcceptanceCommand(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			build := interptest.Expectation{Step: "build", Attempt: 1, Profile: "builder", Inputs: map[string]string{"spec": "sha256:" + sum([]byte(smallPlan))},
				Standards: []string{"standards/go.md"}, Skills: []string{"go-tdd", "guildhall-contributing"},
				Respond: interptest.Succeed("built", interptest.File{Path: "head", Data: []byte(head + "\n")})}
			e := acceptEnv(t, b, smallPlan, 200, 8, build)
			v, err := e.eng.DirectorAcceptance(ctx, "app", "t7", "accept-1")
			if err != nil || !v.OK {
				t.Fatalf("DirectorAcceptance = %+v, %v; want yes", v, err)
			}
			if _, err := e.eng.DirectorAcceptance(ctx, "app", "t7", "accept-9"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("unknown decision: err = %v, want ErrNotFound", err)
			}
			// The engine reads the answer, not where it was given; the store test pins AnsweredViaDirector.
			e.answer(t, "accept-1", "approved\nSmall and safe.")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			if _, ok := e.documents(t)["accept-1-spec"]; !ok {
				t.Error("the Director's acceptance recorded no accepted spec")
			}
			if _, err := e.eng.DirectorAcceptance(ctx, "app", "t7", "accept-1"); !errors.Is(err, interp.ErrNotPlanAcceptance) {
				t.Errorf("answered decision: err = %v, want ErrNotPlanAcceptance", err)
			}
		})
	}
}

// TestDirectorAcceptanceRefused: with the setting off accept-1 is no decision the Director may judge, and with it on
// a plan over the threshold is judged no, with its reason.
func TestDirectorAcceptanceRefused(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			off := acceptEnv(t, b, smallPlan, 0, 0)
			if _, err := off.eng.DirectorAcceptance(ctx, "app", "t7", "accept-1"); !errors.Is(err, interp.ErrNotPlanAcceptance) {
				t.Errorf("setting off: err = %v, want ErrNotPlanAcceptance", err)
			}
			large := acceptEnv(t, b, smallPlan, 100, 8)
			v, err := large.eng.DirectorAcceptance(ctx, "app", "t7", "accept-1")
			if err != nil || v.OK || !strings.Contains(v.String(), "over director_accept_lines = 100") {
				t.Errorf("too large: verdict = %+v, %v; want no, over the lines", v, err)
			}
		})
	}
}

// TestDirectorAcceptanceMessage: with the setting on, accept-1's decision message carries the verdict and its
// reasons, and the decision still opens and waits; with it off, the message is as it always was.
func TestDirectorAcceptanceMessage(t *testing.T) {
	const plain = "Decision accept-1 is open for step accept: approval"
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			for _, c := range []struct {
				name         string
				spec         string
				lines, files int
				want         string
			}{
				{"off", smallPlan, 0, 0, plain},
				{"eligible", smallPlan, 200, 8, plain + ". Director acceptance: the Director may accept it: the grill was unattended; one slice; no open questions; " +
					"about 120 non-test lines in 4 files, within 200 lines and 8 files; touches none."},
				{"too large", smallPlan, 100, 8, plain + ". Director acceptance: the Guildmaster must answer: about 120 non-test lines is over director_accept_lines = 100."},
			} {
				t.Run(c.name, func(t *testing.T) {
					e := acceptEnv(t, b, c.spec, c.lines, c.files)
					var got string
					for id, text := range e.messages(t) {
						if strings.HasSuffix(id, "decision.accept-1") || strings.Contains(text, "Decision accept-1") {
							got = text
						}
					}
					if got != c.want {
						t.Errorf("decision message = %q, want %q", got, c.want)
					}
					if d := e.decision(t, "accept-1"); d.Answered || d.Kind != store.DecisionApproval {
						t.Errorf("accept-1 = %+v, want an open approval", d)
					}
				})
			}
		})
	}
}

// TestDirectorAcceptanceTaskOverride: the task's accept.requires_guildmaster override puts the verdict to no, and a
// task override of the size budget does not raise the cap, which is the copy's.
func TestDirectorAcceptanceTaskOverride(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := acceptEnvOverrides(t, b, map[string]string{config.AcceptRequiresGuildmasterOverride: "true", config.QualitySizeBudgetLinesKey: "1000"},
				"quality_size_budget_lines", "100")
			var got string
			for _, text := range e.messages(t) {
				if strings.Contains(text, "Decision accept-1") {
					got = text
				}
			}
			for _, want := range []string{"the Guildmaster must answer", "accept.requires_guildmaster keeps it with the Guildmaster",
				"about 120 non-test lines is over quality_size_budget_lines = 100"} {
				if !strings.Contains(got, want) {
					t.Errorf("decision message = %q, want %q in it", got, want)
				}
			}
		})
	}
}

// acceptEnvOverrides is acceptEnv at 200 lines and 8 files with the copy's local setting key = value and the task's
// overrides applied as an advance applies them.
func acceptEnvOverrides(t *testing.T, b backend, overrides map[string]string, key, value string) *env {
	t.Helper()
	def := loadWorkflow(t, "director-grill")
	notes := []byte("# Grill notes\n\n## Questions\n\nNone.\n")
	plan := planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: []byte(smallPlan)}))
	plan.Inputs = map[string]string{"context": interptest.AnyDigest}
	e := newEnvDefinition(t, b, def, unattendedGrill(1, interptest.Succeed("grilled", interptest.File{Path: "notes.md", Data: notes})),
		researchExpectation([]byte("notes\n")), plan)
	base := e.validConfig(t)
	base.Repositories[0].Workflow = def.Name
	base.Repositories[0].DirectorAcceptLines, base.Repositories[0].DirectorAcceptFiles = 200, 8
	base.Local = map[string]string{key: value}
	for k, v := range overrides {
		if _, err := e.h.AddTaskOverride("override:"+k, "app", "t7", store.TaskOverrideSet, k, v, 0, "Guildmaster: keep it with me."); err != nil {
			t.Fatal(err)
		}
	}
	e.applyOverrides(t, base)
	if len(e.eng.Overrides.Skipped) > 0 {
		t.Fatalf("skipped overrides: %+v", e.eng.Overrides.Skipped)
	}
	e.setGrill(t, workflow.GrillUnattended, store.GrillModeByDirector)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "grill", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
	return e
}

// TestDirectorAcceptanceSuperseded: a round whose spec changed is refused once it is voided as superseded by
// accept-2, which is judged again on the new spec; a design review is never the Director's.
func TestDirectorAcceptanceSuperseded(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			plan2 := strings.Replace(smallPlan, "by priority", "by priority, then id", 1)
			// A design round follows a live grill, as TestDesignRoundWhileAcceptanceOpen runs it, so each round is judged
			// no for that; judged, not refused.
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), researchExpectation(researchSort), gplanExpectation(1, nil, []byte(smallPlan)),
				designerExpectation("designer", 1, designBrief, designOne), gplanExpectation(2, designOne, []byte(plan2)))
			e.eng.Config.Repositories[0].DirectorAcceptLines, e.eng.Config.Repositories[0].DirectorAcceptFiles = 200, 8
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			if v, err := e.eng.DirectorAcceptance(ctx, "app", "t7", "accept-1"); err != nil || v.OK {
				t.Errorf("accept-1 = %+v, %v; want it judged, no, as the grill is live", v, err)
			}
			want(t, e.advance(t, designRequested(designBrief)), interp.Next{Wait: interp.WaitAttempt, Step: "designer", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "designer", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "design", Decision: "design-1"})
			if _, err := e.eng.DirectorAcceptance(ctx, "app", "t7", "design-1"); !errors.Is(err, interp.ErrNotPlanAcceptance) {
				t.Errorf("design review: err = %v, want ErrNotPlanAcceptance", err)
			}
			e.answer(t, "design-1", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "design-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-2"})
			if _, err := e.eng.DirectorAcceptance(ctx, "app", "t7", "accept-1"); !errors.Is(err, interp.ErrNotPlanAcceptance) {
				t.Errorf("superseded accept-1: err = %v, want ErrNotPlanAcceptance", err)
			}
			if v, err := e.eng.DirectorAcceptance(ctx, "app", "t7", "accept-2"); err != nil || v.OK || !strings.Contains(v.String(), "the grill is live") {
				t.Errorf("accept-2 = %+v, %v; want it judged again, no, as the grill is live", v, err)
			}
		})
	}
}

// TestDirectorAcceptanceLoopExit: with the setting on, accept-1 of the loop workflow is judged, and its loop exit
// accept-x2, opened after two changes_requested rounds, is refused as no plan acceptance.
func TestDirectorAcceptanceLoopExit(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			plan := planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: []byte(smallPlan)}))
			plan2 := plan
			plan2.Attempt = 2
			plan2.Inputs = map[string]string{"feedback": "sha256:" + sum([]byte("changes_requested"))}
			e := newEnvDefinition(t, b, loadWorkflow(t, "loop"), plan, plan2)
			e.eng.Config.Repositories[0].DirectorAcceptLines, e.eng.Config.Repositories[0].DirectorAcceptFiles = 200, 8
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			if _, err := e.eng.DirectorAcceptance(ctx, "app", "t7", "accept-1"); err != nil {
				t.Errorf("accept-1: err = %v, want it judged", err)
			}
			e.answer(t, "accept-1", "changes_requested")
			e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2})
			e.answer(t, "accept-2", "changes_requested")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-2"}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-x2"})
			if _, err := e.eng.DirectorAcceptance(ctx, "app", "t7", "accept-x2"); !errors.Is(err, interp.ErrNotPlanAcceptance) {
				t.Errorf("loop exit: err = %v, want ErrNotPlanAcceptance", err)
			}
		})
	}
}
