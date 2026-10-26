package interp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
)

func TestAdvanceRefusesProfilelessLegacyTask(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := straight(t)
			delete(def.Steps[0].Settings, "profile")
			e := newEnvDefinition(t, b, def)
			err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated})
			if !errors.Is(err, config.ErrMissingAdapter) || !strings.Contains(err.Error(), `step "plan"`) || !strings.Contains(err.Error(), "custom workflow") {
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
