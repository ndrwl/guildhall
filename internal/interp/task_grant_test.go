package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// grantCreator is the write both backends' records have and the engine never makes.
type grantCreator interface {
	CreateTaskGrant(mutationID string, g store.TaskGrant, steps []string) (store.Receipt, error)
}

// grant records grant id of task on step until until, with rules, and answers its decision with answer unless it
// is empty.
func (e *env) grant(t *testing.T, task, id, step string, until store.GrantUntil, answer string, rules ...string) {
	t.Helper()
	g := store.TaskGrant{Repository: "app", TaskID: task, ID: id, Step: step, Rules: rules, Reason: "The probe needs them.", Until: until}
	if _, err := e.h.Records.(grantCreator).CreateTaskGrant("grant:"+task+":"+id, g, []string{"plan", "build", "review"}); err != nil {
		t.Fatal(err)
	}
	if answer != "" {
		if _, err := e.h.AnswerDecision("answer:"+task+":"+id, "app", task, store.GrantDecision(id), answer); err != nil {
			t.Fatal(err)
		}
	}
}

// boundGrant reads the grant attempt n of step's binding records, nil when it has none.
func (e *env) boundGrant(t *testing.T, step string, n int) *store.BoundGrant {
	t.Helper()
	d, ok := e.documents(t)[step+"-"+itoa(n)+"-inputs"]
	if !ok {
		t.Fatalf("no binding for %s-%d", step, n)
	}
	data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(d.Path)))
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Grant *store.BoundGrant `json:"grant"`
	}
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	return b.Grant
}

func itoa(n int) string { return string(rune('0' + n)) }

func taskGrants(t *testing.T, e *env, task string) []store.TaskGrant {
	t.Helper()
	gs, err := e.h.Records.TaskGrants("app", task)
	if err != nil {
		t.Fatal(err)
	}
	return gs
}

// TestPrepareRendersTheTaskDetail: a task's detail reaches the rendered stage with its title.
func TestPrepareRendersTheTaskDetail(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			const detail = "Running X fails:\n\n    error: Y"
			e := newEnvTasks(t, b, straight(t), func(task *store.Task) { task.Detail = detail }, planExpectation(t, interptest.Fail("no plan")))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if len(e.stages) != 1 || e.stages[0].Title != "Straight path" || e.stages[0].Detail != detail {
				t.Fatalf("rendered stages = %+v, want plan-1 with the task's title and detail", e.stages)
			}
		})
	}
}

// TestTaskGrantBindsOneAttempt: an approved grant that lasts one attempt is recorded on the binding of the first
// attempt of its step bound after approval, and the next attempt, after a retry, gets none; another task's grant
// never reaches this task.
func TestTaskGrantBindsOneAttempt(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			plan2 := planExpectation(t, interptest.Fail("no plan again"))
			plan2.Attempt = 2
			e := newEnvTasks(t, b, straight(t), func(*store.Task) {}, planExpectation(t, interptest.Fail("no plan")), plan2)
			e.grant(t, "t3", "g1", "plan", store.GrantUntilDone, "approve", "Bash(herdr:*)")
			e.grant(t, "t7", "g1", "plan", store.GrantUntilAttempt, "approve", "Bash(go:*)", "Bash(claude:*)")

			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if got := e.boundGrant(t, "plan", 1); got == nil || got.ID != "g1" || !slices.Equal(got.Rules, []string{"Bash(go:*)", "Bash(claude:*)"}) {
				t.Fatalf("plan-1 grant = %+v", got)
			}
			if gs := taskGrants(t, e, "t7"); !slices.Equal(gs[0].Bound, []store.GrantUse{{Step: "plan", Attempt: 1}}) {
				t.Errorf("bound = %+v", gs[0].Bound)
			}
			if gs := taskGrants(t, e, "t3"); len(gs[0].Bound) != 0 {
				t.Errorf("t3's grant was bound: %+v", gs[0].Bound)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "plan", Attempt: 1})
			e.advance(t, retried("plan", 1, "Try again."))
			if got := e.boundGrant(t, "plan", 2); got != nil {
				t.Errorf("plan-2 took the one-attempt grant: %+v", got)
			}
			if gs := taskGrants(t, e, "t7"); len(gs[0].Bound) != 1 {
				t.Errorf("bound = %+v", gs[0].Bound)
			}
		})
	}
}

// TestTaskGrantUntilDoneBindsEveryAttempt: a grant until done, here for every step, binds each attempt bound while it
// is active; a pending, rejected or revoked one binds none.
func TestTaskGrantUntilDoneBindsEveryAttempt(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			plan2 := planExpectation(t, interptest.Fail("no plan again"))
			plan2.Attempt = 2
			e := newEnv(t, b, planExpectation(t, interptest.Fail("no plan")), plan2)
			e.grant(t, "t7", "g1", store.GrantAllSteps, store.GrantUntilDone, "approve", "Bash(go:*)")
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			e.advance(t, retried("plan", 1, "Try again."))
			for n := 1; n <= 2; n++ {
				if got := e.boundGrant(t, "plan", n); got == nil || got.ID != "g1" {
					t.Errorf("plan-%d grant = %+v", n, got)
				}
			}
			if gs := taskGrants(t, e, "t7"); !slices.Equal(gs[0].Bound, []store.GrantUse{{Step: "plan", Attempt: 1}, {Step: "plan", Attempt: 2}}) {
				t.Errorf("bound = %+v", gs[0].Bound)
			}
		})
	}
	for name, answer := range map[string]string{"pending": "", "rejected": "reject"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, backends[1], planExpectation(t, interptest.Fail("no plan")))
			e.grant(t, "t7", "g1", "plan", store.GrantUntilAttempt, answer, "Bash(go:*)")
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if got := e.boundGrant(t, "plan", 1); got != nil {
				t.Errorf("plan-1 grant = %+v", got)
			}
		})
	}
	t.Run("revoked", func(t *testing.T) {
		e := newEnv(t, backends[1], planExpectation(t, interptest.Fail("no plan")))
		e.grant(t, "t7", "g1", "plan", store.GrantUntilAttempt, "approve", "Bash(go:*)")
		if _, err := e.h.Records.(*store.Store).RevokeTaskGrant("revoke", "app", "t7", "g1", "Not needed."); err != nil {
			t.Fatal(err)
		}
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		if got := e.boundGrant(t, "plan", 1); got != nil {
			t.Errorf("plan-1 grant = %+v", got)
		}
	})
}

// capRefused refuses every start at the daily cap, after the binding is recorded.
type capRefused struct{ interp.Records }

func (capRefused) StartCappedAttempt(string, string, string, string, store.AttemptCaps) (store.Receipt, error) {
	return store.Receipt{}, &store.CapRefusal{Cap: store.CapDaily, Limit: 1, Count: 1}
}

// TestTaskGrantSkipsAnExistingBinding: a grant approved after an attempt's binding was recorded, as when a cap refused
// its start, waits for the next bound attempt; it never reaches that one.
func TestTaskGrantSkipsAnExistingBinding(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, planExpectation(t, interptest.Fail("no plan")))
			records := e.h.Records
			e.eng.Records = capRefused{records}
			if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, store.ErrAttemptCap) {
				t.Fatalf("capped advance: %v", err)
			}
			e.grant(t, "t7", "g1", "plan", store.GrantUntilAttempt, "approve", "Bash(go:*)")
			e.eng.Records = records
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if got := e.boundGrant(t, "plan", 1); got != nil {
				t.Errorf("the existing plan-1 binding took a grant: %+v", got)
			}
			if gs := taskGrants(t, e, "t7"); len(gs[0].Bound) != 0 {
				t.Errorf("bound = %+v", gs[0].Bound)
			}
		})
	}
}

// recordingLauncher accepts every launch without running anything.
type recordingLauncher struct{ specs []interp.Spec }

func (l *recordingLauncher) Launch(_ context.Context, s interp.Spec) (interp.Handle, error) {
	l.specs = append(l.specs, s)
	return interp.Handle{Identity: s.Identity, Ref: "recording"}, nil
}

func (*recordingLauncher) Stop(interp.Handle) error { return nil }

// TestTaskGrantSkipsTheFakeAdapter: a step whose profile runs the fake adapter, which grants nothing, takes no grant.
func TestTaskGrantSkipsTheFakeAdapter(t *testing.T) {
	e := newEnv(t, backends[1])
	e.eng.Config.Profiles["planner"] = config.Profile{Adapter: interp.FakeAdapter}
	e.eng.Launcher = &recordingLauncher{}
	e.grant(t, "t7", "g1", "plan", store.GrantUntilAttempt, "approve", "Bash(go:*)")
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	if got := e.boundGrant(t, "plan", 1); got != nil {
		t.Errorf("a fake attempt took a grant: %+v", got)
	}
}

// TestTaskGrantSkipsRebaserAttempts: a grant for every step reaches no rebaser or narrowed review attempt; only
// attempts bound for workflow steps take one.
func TestTaskGrantSkipsRebaserAttempts(t *testing.T) {
	e, f, ws := queueEnv(t, backends[1])
	e.grant(t, "t7", "g1", store.GrantAllSteps, store.GrantUntilDone, "approve", "Bash(go:*)")
	tier2(t, e, f, ws, func(h string) workflow.ReviewOutcome { return bound(t, workflow.VerdictApproved, h, nil) })
	for _, step := range []string{"deliver-rebase", "deliver-rebase-review"} {
		if got := e.boundGrant(t, step, 1); got != nil {
			t.Errorf("%s-1 took a grant: %+v", step, got)
		}
	}
	if gs := taskGrants(t, e, "t7"); len(gs[0].Bound) != 0 {
		t.Errorf("bound = %+v", gs[0].Bound)
	}
}

// TestTaskGrantSkipsResolutionAttempts: a grant for every step reaches neither the rebaser resolving validate's
// conflicts nor the narrowed review of its resolution (rebaser.go's synthetic attempts).
func TestTaskGrantSkipsResolutionAttempts(t *testing.T) {
	e, f, ws := validateRebaseEnv(t, backends[1])
	e.grant(t, "t7", "g1", store.GrantAllSteps, store.GrantUntilDone, "approve", "Bash(go:*)")
	minor(ws, baseN(1), head, commitN(1))
	withRebaser(e)
	resolved := squashed(baseN(1), commitN(2))
	f.AddCommit(repoPath, resolved)
	ws.setTip(commitN(2))
	e.script.Expect(rebaserExp("validate", 1, commitN(2), false), narrowExp(t, "validate", 1, resolved, bound(t, workflow.VerdictApproved, resolved, nil)))
	want(t, e.advance(t, reviewed), interp.Next{Wait: interp.WaitAttempt, Step: "validate-rebase", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "validate-rebase", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "validate-rebase-review", Attempt: 1})
	for _, step := range []string{"validate-rebase", "validate-rebase-review"} {
		if got := e.boundGrant(t, step, 1); got != nil {
			t.Errorf("%s-1 took a grant: %+v", step, got)
		}
	}
	if gs := taskGrants(t, e, "t7"); len(gs[0].Bound) != 0 {
		t.Errorf("bound = %+v", gs[0].Bound)
	}
}
