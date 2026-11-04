package cli

import (
	"encoding/json"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestWebGrillMode pins the band's grill-mode line and the stepper's grill row (d172): a task with no record reads
// as live with its grill counted in rounds; an unattended record names the row "Unattended grill (Artisan)", which
// says finished rather than rounds, and the band carries task show's line; a workflow with no grill, a bug's, has no
// line.
func TestWebGrillMode(t *testing.T) {
	def := &workflow.Definition{Steps: []workflow.Step{
		{ID: "grill", Type: "grill", Version: 1, Settings: map[string]json.RawMessage{
			workflow.ActorSetting: json.RawMessage(`"director"`), "skills": json.RawMessage(`["guildhall-grill"]`)}},
		{ID: "research", Type: "research", Version: 1},
	}}
	ct := clerkTask{view: taskView{Task: store.Task{Repository: "app", ID: "t1", State: store.TaskOpen}}}
	tl := taskTimeline{Attempts: []timedAttempt{{StageAttempt: store.StageAttempt{Step: "grill", Number: 1, Outcome: store.AttemptSucceeded}}}}

	live := webBuilder{ct: ct, tl: tl, def: def}
	if got := live.grillLine(); got != "grill: live" {
		t.Errorf("live grill line = %q", got)
	}
	if st := live.steps(); st[0].Name != "Grill" || st[0].Note != "1 round" {
		t.Errorf("live grill row = %+v", st[0])
	}

	h := store.TaskHistory{GrillModes: []store.GrillMode{{Repository: "app", TaskID: "t1", Number: 1, Mode: workflow.GrillUnattended,
		Reason: "contracts already written", By: store.GrillModeByDirector}}}
	un := webBuilder{ct: ct, h: h, tl: tl, def: def}
	const line = "grill: unattended (director): contracts already written"
	if got := un.grillLine(); got != line {
		t.Errorf("unattended grill line = %q, want %q", got, line)
	}
	st := un.steps()
	if st[0].Name != "Unattended grill (Artisan)" || st[0].State != web.StateDone || st[0].Note != "finished" {
		t.Errorf("unattended grill row = %+v", st[0])
	}
	if st[1].Name != "Research" || st[1].Note != "later" {
		t.Errorf("research row = %+v", st[1])
	}
	if def.Steps[0].Actor() != workflow.DirectorActor {
		t.Errorf("steps changed the bound grill's actor to %q", def.Steps[0].Actor())
	}

	// A grill switched back to live reads as live again, counted in rounds.
	h.GrillModes = append(h.GrillModes, store.GrillMode{Repository: "app", TaskID: "t1", Number: 2, Mode: workflow.GrillLive,
		Reason: "grill me on this", By: store.GrillModeByGuildmaster})
	back := webBuilder{ct: ct, h: h, tl: tl, def: def}
	if got := back.grillLine(); got != "grill: live (guildmaster): grill me on this" {
		t.Errorf("switched grill line = %q", got)
	}
	if st := back.steps(); st[0].Name != "Grill" || st[0].Note != "1 round" {
		t.Errorf("switched grill row = %+v", st[0])
	}

	bug := webBuilder{ct: ct, def: &workflow.Definition{Steps: []workflow.Step{{ID: "build", Type: "build", Version: 1}}}}
	if got := bug.grillLine(); got != "" {
		t.Errorf("bug grill line = %q, want none", got)
	}
	if got := (webBuilder{ct: ct}).grillLine(); got != "" {
		t.Errorf("no-workflow grill line = %q, want none", got)
	}
}
