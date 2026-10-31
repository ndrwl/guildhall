package interp_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// override records the Director's set of key to value on app/t7 as override record n, with prior earlier records for
// the key.
func (e *env) override(t *testing.T, n int, key, value string, prior int) {
	t.Helper()
	if rc, err := e.h.AddTaskOverride("task-override:"+itoa(n), "app", "t7", store.TaskOverrideSet, key, value, prior, "Guildmaster: run it so."); err != nil || rc.Record != store.TaskOverrideRecord(n) {
		t.Fatalf("AddTaskOverride %d = %+v, %v", n, rc, err)
	}
}

// validConfig completes the engine's config into one config.Load would accept, which applying overrides needs.
func (e *env) validConfig(t *testing.T) *config.Config {
	t.Helper()
	c := e.eng.Config
	c.Mode, c.Name = config.ModeDirector, "here"
	c.Repositories[0] = config.Repository{ID: "app", Path: t.TempDir(), Remote: "git@example.com:app.git", OwnerMachine: "here", Workflow: "straight"}
	return c
}

// applyOverrides gives the engine app/t7's overrides applied to base, as each `task start` does before it advances.
func (e *env) applyOverrides(t *testing.T, base *config.Config) {
	t.Helper()
	o, err := interp.ApplyTaskOverrides(e.h.Records, base, "app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	e.eng.Config, e.eng.Overrides = o.Config, &o
}

// bindingBytes is attempt n of step's recorded binding.
func (e *env) bindingBytes(t *testing.T, step string, n int) []byte {
	t.Helper()
	d, ok := e.documents(t)[step+"-"+itoa(n)+"-inputs"]
	if !ok {
		t.Fatalf("no binding for %s-%d", step, n)
	}
	data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(d.Path)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type boundOverrides struct {
	Overrides []struct {
		Key, Value string
		Number     int
	} `json:"overrides"`
	Skipped []config.Skipped `json:"skipped_overrides"`
}

func (e *env) boundOverrides(t *testing.T, step string, n int) boundOverrides {
	t.Helper()
	var b boundOverrides
	if err := json.Unmarshal(e.bindingBytes(t, step, n), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestTaskOverrideSetAfterAFinishedStepRerunsNothing: an override recorded after the plan succeeded leaves the plan
// current, so it runs once; the build, the next stage started, runs at the overridden effort and its binding records
// the override and the skipped one beside it, while the plan's binding records none.
func TestTaskOverrideSetAfterAFinishedStepRerunsNothing(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)[:2]...)
			base := e.validConfig(t)
			e.applyOverrides(t, base)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			e.override(t, 1, "default_effort", "high", 0)
			e.override(t, 2, "lock_wait", "1s", 0)
			e.override(t, 3, "merge.requires.ci_green", "false", 0)
			e.answer(t, "accept-1", "approved")
			e.applyOverrides(t, base)
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			if n := len(slices.DeleteFunc(e.attempts(t), func(a store.StageAttempt) bool { return a.Step != "plan" })); n != 1 {
				t.Errorf("plan ran %d times", n)
			}
			if l := e.script.Launched(); len(l) != 2 || l[0].Settings.Effort != "medium" || l[1].Settings.Effort != "high" {
				t.Errorf("launched %+v; want the plan at medium and the build at the overridden high", l)
			}
			if got := e.boundOverrides(t, "plan", 1); got.Overrides != nil || got.Skipped != nil {
				t.Errorf("plan-1 binding took overrides %+v", got)
			}
			got := e.boundOverrides(t, "build", 1)
			if len(got.Overrides) != 1 || got.Overrides[0].Key != "default_effort" || got.Overrides[0].Value != "high" || got.Overrides[0].Number != 1 {
				t.Errorf("build-1 binding overrides = %+v", got.Overrides)
			}
			if len(got.Skipped) != 2 || got.Skipped[0].Key != "lock_wait" || got.Skipped[0].Value != "1s" || got.Skipped[0].Reason == "" ||
				got.Skipped[1].Key != "merge.requires.ci_green" || !strings.Contains(got.Skipped[1].Reason, "only make merge requirements stricter") {
				t.Errorf("build-1 binding skipped overrides = %+v", got.Skipped)
			}
			if base.EffectiveEffort("") != "medium" {
				t.Error("applying the overrides changed the base config")
			}
		})
	}
}

// TestTaskOverridesNoneBindTheSameBytes: an advance given a task with no overrides binds its attempt to the same
// bytes as one given no overrides at all.
func TestTaskOverridesNoneBindTheSameBytes(t *testing.T) {
	var got [][]byte
	for _, apply := range []bool{false, true} {
		e := newEnv(t, backends[0], planExpectation(t, interptest.Succeed("plan written")))
		base := e.validConfig(t)
		if apply {
			e.applyOverrides(t, base)
		}
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		got = append(got, e.bindingBytes(t, "plan", 1))
	}
	if !bytes.Equal(got[0], got[1]) {
		t.Errorf("binding with no overrides = %s, want %s", got[1], got[0])
	}
}
