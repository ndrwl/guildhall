package interp_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// unattendedGrill is grill attempt n run by an Artisan under the researcher profile with the unattended skills,
// answering with r.
func unattendedGrill(n int, r interptest.Response) interptest.Expectation {
	return interptest.Expectation{Step: "grill", Attempt: n, Profile: "researcher", Inputs: map[string]string{}, Standards: []string{"standards/go.md"},
		Skills: workflow.UnattendedGrillSkills, Respond: r}
}

// setGrill records task app/t7's next grill-mode record.
func (e *env) setGrill(t *testing.T, mode workflow.GrillMode, by string) {
	t.Helper()
	got, err := e.h.Records.GrillModes("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.SetGrillMode("grill-mode:"+itoa(len(got)+1), "app", "t7", mode, "The task needs it "+string(mode)+".", by, len(got)); err != nil {
		t.Fatal(err)
	}
}

// boundGrill is the grill mode attempt n of step was bound under, nil when its binding has none.
func (e *env) boundGrill(t *testing.T, step string, n int) *struct {
	Mode   workflow.GrillMode `json:"mode"`
	Number int                `json:"number"`
} {
	t.Helper()
	var b struct {
		GrillMode *struct {
			Mode   workflow.GrillMode `json:"mode"`
			Number int                `json:"number"`
		} `json:"grill_mode"`
	}
	if err := json.Unmarshal(e.bindingBytes(t, step, n), &b); err != nil {
		t.Fatal(err)
	}
	return b.GrillMode
}

// TestUnattendedGrillRunsAnArtisan: a task whose grill mode is unattended runs its Director grill as an Artisan
// attempt under the researcher profile with the unattended skills and the Artisan's role, binding the mode, and its
// succeeded notes move the task on to research as a live grill's would. The bound revision is unchanged.
func TestUnattendedGrillRunsAnArtisan(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := loadWorkflow(t, "director-grill")
			digest := def.Digest()
			notes := []byte("# Grill notes\n\n## Questions\n\n1. Sort by id? Recommended: yes.\n")
			e := newEnvDefinition(t, b, def, unattendedGrill(1, interptest.Succeed("grilled", interptest.File{Path: "notes.md", Data: notes})), researchExpectation([]byte("notes\n")))
			e.setGrill(t, workflow.GrillUnattended, store.GrillModeByDirector)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "grill", Attempt: 1})
			if len(e.messages(t)) != 0 {
				t.Errorf("messages = %v, want no director-stage message", e.messages(t))
			}
			if len(e.stages) != 1 || e.stages[0].Step.Actor() != workflow.ArtisanActor {
				t.Fatalf("stages = %+v, want the grill rendered for an Artisan", e.stages)
			}
			if g := e.boundGrill(t, "grill", 1); g == nil || g.Mode != workflow.GrillUnattended || g.Number != 1 {
				t.Errorf("grill 1 bound grill mode %+v, want unattended, record 1", g)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			if d := e.documents(t)["grill-1-notes"]; d.SHA256 != sum(notes) {
				t.Errorf("grill-1-notes = %+v, want the Artisan's notes", d)
			}
			if g := e.boundGrill(t, "research", 1); g != nil {
				t.Errorf("research bound grill mode %+v, want none", g)
			}
			if def.Digest() != digest || def.Steps[0].Actor() != workflow.DirectorActor {
				t.Errorf("the bound definition changed")
			}
		})
	}
}

// TestLiveGrillBindsNoGrillMode: a task with no grill-mode record runs the Director's live grill, and its binding
// reads as it did before grill modes existed.
func TestLiveGrillBindsNoGrillMode(t *testing.T) {
	e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"))
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1})
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(e.bindingBytes(t, "grill", 1), &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["grill_mode"]; ok || len(fields) != 1 {
		t.Errorf("binding fields = %v, want only inputs", fields)
	}
}

// TestUnattendedGrillEscalatesToLive: an Artisan grill that fails waits for the Director, which switches the task to
// live and retries the grill: attempt 2 is a Director stage.
func TestUnattendedGrillEscalatesToLive(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), unattendedGrill(1, interptest.Fail("the goal depends on the Guildmaster")))
			e.setGrill(t, workflow.GrillUnattended, store.GrillModeByDirector)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "grill", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "grill", Attempt: 1})
			e.setGrill(t, workflow.GrillLive, store.GrillModeByDirector)
			want(t, e.advance(t, retried("grill", 1, "the goal needs the Guildmaster")), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 2})
			if _, ok := e.messages(t)["app.t7.stage.grill-2"]; !ok {
				t.Errorf("messages = %v, want the stage message for grill 2", e.messages(t))
			}
			if len(e.script.Launched()) != 1 {
				t.Errorf("launched %d Artisans, want only grill 1", len(e.script.Launched()))
			}
			if g := e.boundGrill(t, "grill", 2); g == nil || g.Mode != workflow.GrillLive || g.Number != 2 {
				t.Errorf("grill 2 bound grill mode %+v, want live, record 2", g)
			}
		})
	}
}

// TestGrillModeChangeReopensTheGrill: a grill that succeeded live, while research is held, runs again unattended once
// the mode changes, and its notes are the ones research and plan read.
func TestGrillModeChangeReopensTheGrill(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			notes := []byte("# Grill notes\n\n## Questions\n\n1. Keep the old order? Recommended: no.\n")
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), unattendedGrill(2, interptest.Succeed("grilled", interptest.File{Path: "notes.md", Data: notes})), researchExpectation([]byte("notes\n")))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1})
			finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
			held := true
			e.eng.Hold = func() (bool, error) { return held, nil }
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitPaused, Step: "research"})

			e.setGrill(t, workflow.GrillUnattended, store.GrillModeByGuildmaster)
			held = false
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "grill", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			if d := e.documents(t)["grill-2-notes"]; d.SHA256 != sum(notes) {
				t.Errorf("grill-2-notes = %+v, want the unattended notes", d)
			}
		})
	}
}

// TestDesignRoundRefusedForAnUnattendedGrill: a design round follows a live grill, so an unattended task's request is
// refused, recording nothing, with the command that switches it to live.
func TestDesignRoundRefusedForAnUnattendedGrill(t *testing.T) {
	e := newEnvDefinition(t, backends[1], loadWorkflow(t, "director-grill"))
	e.setGrill(t, workflow.GrillUnattended, store.GrillModeByDirector)
	before := e.counts(t)
	err := advanceErr(t, e, designRequested(designBrief))
	if !errors.Is(err, interp.ErrDesignRound) || !strings.Contains(err.Error(), "grill is unattended") || !strings.Contains(err.Error(), "guildhall task grill app/t7 live") {
		t.Errorf("request = %v, want a design-round refusal naming the unattended grill and task grill", err)
	}
	if strings.Contains(err.Error(), "bugs") {
		t.Errorf("refusal %q calls the task a bug", err)
	}
	if after := e.counts(t); after != before {
		t.Errorf("a refused request recorded %v, had %v", after, before)
	}
}
