package interp_test

import (
	"strings"
	"testing"

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
// whose Director acceptance keys are lines and files.
func acceptEnv(t *testing.T, b backend, spec string, lines, files int) *env {
	t.Helper()
	def := loadWorkflow(t, "director-grill")
	notes := []byte("# Grill notes\n\n## Questions\n\nNone.\n")
	plan := planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: []byte(spec)}))
	plan.Inputs = map[string]string{"context": interptest.AnyDigest}
	e := newEnvDefinition(t, b, def, unattendedGrill(1, interptest.Succeed("grilled", interptest.File{Path: "notes.md", Data: notes})),
		researchExpectation([]byte("notes\n")), plan)
	e.eng.Config.Repositories[0].DirectorAcceptLines, e.eng.Config.Repositories[0].DirectorAcceptFiles = lines, files
	e.setGrill(t, workflow.GrillUnattended, store.GrillModeByDirector)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "grill", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
	return e
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
