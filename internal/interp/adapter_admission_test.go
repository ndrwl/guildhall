package interp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
)

// TestAdvanceRunsAProfilelessStepUnderItsRoleProfile: a step that names no profile runs under its role profile,
// resolved at launch rather than written into the definition the task bound.
func TestAdvanceRunsAProfilelessStepUnderItsRoleProfile(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := straight(t)
			delete(def.Steps[0].Settings, "profile")
			e := newEnvDefinition(t, b, def, planExpectation(t, interptest.Succeed("plan written")))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if got := e.script.Launched(); len(got) != 1 || got[0].Profile != "planner" {
				t.Fatalf("launched %+v, want plan under planner", got)
			}
		})
	}
}

// TestAdvanceRefusesProfilelessLegacyTask: a config without the step's role profile, which a parsed Director config
// never is since it has the shipped profiles, refuses the step before recording or launching anything.
func TestAdvanceRefusesProfilelessLegacyTask(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := straight(t)
			delete(def.Steps[0].Settings, "profile")
			e := newEnvDefinition(t, b, def)
			delete(e.eng.Config.Profiles, "planner")
			err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated})
			if !errors.Is(err, config.ErrMissingAdapter) || !strings.Contains(err.Error(), `step "plan"`) || !strings.Contains(err.Error(), "declare [profile.planner]") {
				t.Fatalf("Advance = %v", err)
			}
			if got := e.attempts(t); len(got) != 0 {
				t.Fatalf("refusal recorded attempts: %+v", got)
			}
			if got := e.script.Launched(); len(got) != 0 {
				t.Fatalf("refusal launched attempts: %+v", got)
			}
		})
	}
}
