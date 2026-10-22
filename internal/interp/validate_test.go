package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// validationResult is a recorded validation result, guildhall.validation-result/1, in its documented field order.
type validationResult struct {
	Schema  string        `json:"schema"`
	Remote  string        `json:"remote"`
	Number  int           `json:"number"`
	URL     string        `json:"url"`
	Head    string        `json:"head"`
	Verdict string        `json:"verdict"`
	Checks  []forge.Check `json:"checks"`
	Failing []string      `json:"failing,omitempty"`
	NoCI    bool          `json:"no_ci,omitempty"`
}

// digest is the digest of the document the engine records for v.
func (v validationResult) digest(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return "sha256:" + sum(data)
}

// result is the validation result of head h on pull request 1 with checks.
func result(h, verdict string, checks []forge.Check, failing ...string) validationResult {
	return validationResult{Schema: "guildhall.validation-result/1", Remote: repoRemote, Number: 1, URL: "https://forge.invalid/1",
		Head: h, Verdict: verdict, Checks: checks, Failing: failing}
}

var (
	ciPassed = []forge.Check{{Name: "ci", State: forge.CheckPassed}, {Name: "lint", State: forge.CheckPassed}}
	ciFailed = []forge.Check{{Name: "ci", State: forge.CheckFailed, URL: "https://ci.invalid/ci"}, {Name: "lint", State: forge.CheckPassed}}
)

// rerun is ciFailed after the Director re-ran its ci check as run-2, which now stands at state.
func rerun(state forge.CheckState) []forge.Check {
	return []forge.Check{{Name: "ci", State: state, URL: "https://ci.invalid/ci", ID: "run-2"}, {Name: "lint", State: forge.CheckPassed}}
}

// newValidateEnv is validate.json over a fake forge holding heads 1 to 3, scripted through review 1.
func newValidateEnv(t *testing.T, b backend) (*env, *forgetest.Fake) {
	t.Helper()
	return newValidateEnvDefinition(t, b, loadWorkflow(t, "validate"))
}

func newValidateEnvDefinition(t *testing.T, b backend, def *workflow.Definition) (*env, *forgetest.Fake) {
	t.Helper()
	e := newEnvDefinition(t, b, def,
		planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
		buildExp(t, 1, head, ""),
		reviewExp(t, "review", "reviewer", 1, head, nil, bound(t, workflow.VerdictApproved, head, nil)))
	f := forgetest.NewFake()
	f.SetBranch(repoRemote, "main", baseHead)
	for _, h := range []string{head, head2, head3} {
		f.AddCommit(repoPath, h)
	}
	e.eng.Forge = f
	e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoPath, Remote: repoRemote, MergePolicy: config.MergeApproval}}
	return e, f
}

// toValidate runs validate.json through review 1, whose result publishes head 1 and reaches validate.
func (e *env) toValidate(t *testing.T) interp.Next {
	t.Helper()
	e.accepted(t)
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
	return e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
}

// validationDoc reads a recorded validation result.
func (e *env) validationDoc(t *testing.T, name string) validationResult {
	t.Helper()
	var v validationResult
	raw := e.pullRequestDoc(t, name)
	data, _ := json.Marshal(raw)
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var waitChecks = interp.Next{Wait: interp.WaitChecks, Step: "validate"}

// TestValidateWaitsForChecks: validate reads the forge's checks of the published head at each advance; no checks
// and pending checks wait, telling the Director once per head, and passed checks record the result and reach
// the merge decision.
func TestValidateWaitsForChecks(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			want(t, e.toValidate(t), waitChecks)
			if _, ok := e.documents(t)["publish-1-head"]; !ok {
				t.Fatal("head 1 was not published before validate")
			}
			pending := "app.t7.checks-pending.validate." + head[:12]
			if got := e.inbox(t); got[len(got)-1] != pending {
				t.Errorf("inbox = %q, want %s last", got, pending)
			}
			n := len(e.inbox(t))
			f.SetChecks(repoRemote, head, forge.Check{Name: "ci", State: forge.CheckPending}, forge.Check{Name: "lint", State: forge.CheckPassed})
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), waitChecks)
			if got := len(e.inbox(t)); got != n {
				t.Errorf("waiting again sent %d messages", got-n)
			}
			for name := range e.documents(t) {
				if strings.HasPrefix(name, "validate-") {
					t.Errorf("pending checks recorded %s", name)
				}
			}

			f.SetChecks(repoRemote, head, ciPassed...)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if got, w := e.validationDoc(t, "validate-1-result"), result(head, "passed", ciPassed); !equalResult(got, w) {
				t.Errorf("validate-1-result = %+v, want %+v", got, w)
			}
			if d := e.documents(t)["validate-1-result"]; d.Path != "tasks/app/t7/validate-1/result.json" || "sha256:"+d.SHA256 != result(head, "passed", ciPassed).digest(t) {
				t.Errorf("validate-1-result = %+v", d)
			}
			msgs := e.messages(t)
			if !strings.Contains(msgs["app.t7.checks-passed.validate-1"], head) {
				t.Errorf("messages = %q, want checks-passed.validate-1 naming the head", msgs)
			}
			// Replays read nothing new from the forge once the result is recorded.
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
		})
	}
}

func equalResult(a, b validationResult) bool {
	return a.Schema == b.Schema && a.Remote == b.Remote && a.Number == b.Number && a.URL == b.URL && a.Head == b.Head &&
		a.Verdict == b.Verdict && slices.Equal(a.Checks, b.Checks) && slices.Equal(a.Failing, b.Failing) && a.NoCI == b.NoCI
}

// TestValidateFailedLoopsToBuild: a failed check records a failed result and takes the failed loop to build
// with the result, naming the failing checks, as feedback; the new head is reviewed, published and validated
// again in the next round.
func TestValidateFailedLoopsToBuild(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			failed := result(head, "failed", ciFailed, "ci")
			e.script.Expect(buildExp(t, 2, head2, failed.digest(t)),
				reviewExp(t, "review", "reviewer", 2, head2, ptr(bound(t, workflow.VerdictApproved, head, nil)), bound(t, workflow.VerdictApproved, head2, nil)))
			f.SetChecks(repoRemote, head, ciFailed...)
			want(t, e.toValidate(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			if got := e.validationDoc(t, "validate-1-result"); !equalResult(got, failed) {
				t.Errorf("validate-1-result = %+v, want %+v", got, failed)
			}
			msg := e.messages(t)["app.t7.checks-failed.validate-1"]
			if !strings.Contains(msg, "ci") || !strings.Contains(msg, head) {
				t.Errorf("checks-failed message = %q, want the failing check and the head", msg)
			}
			// Replays start nothing new.
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})

			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), waitChecks)
			if got := f.Branch(repoRemote, "guildhall/t7"); got != head2 {
				t.Errorf("task branch = %s, want head 2", got)
			}
			f.SetChecks(repoRemote, head2, ciPassed...)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if got := e.validationDoc(t, "validate-2-result"); !equalResult(got, result(head2, "passed", ciPassed)) {
				t.Errorf("validate-2-result = %+v", got)
			}
			if d := e.decision(t, "deliver-1"); d.Subject == nil || d.Subject.Digest != head2 {
				t.Errorf("merge decision = %+v, want it bound to head 2", d)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// TestValidateLoopExit: a failed round past the loop's max opens loop exit validate-x<k>, offering continue or
// reject; continue takes the loop once more, and reject waits for the Director.
func TestValidateLoopExit(t *testing.T) {
	for _, choice := range []string{"continue", "reject"} {
		t.Run(choice, func(t *testing.T) {
			e, f := newValidateEnv(t, backends[0])
			failed1 := result(head, "failed", ciFailed, "ci")
			failed2 := result(head2, "failed", ciFailed, "ci")
			e.script.Expect(buildExp(t, 2, head2, failed1.digest(t)),
				reviewExp(t, "review", "reviewer", 2, head2, ptr(bound(t, workflow.VerdictApproved, head, nil)), bound(t, workflow.VerdictApproved, head2, nil)))
			f.SetChecks(repoRemote, head, ciFailed...)
			f.SetChecks(repoRemote, head2, ciFailed...)
			want(t, e.toValidate(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2})
			exit := interp.Next{Wait: interp.WaitDecision, Step: "validate", Decision: "validate-x2"}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), exit)
			d := e.decision(t, "validate-x2")
			if d.Kind != store.DecisionLoopExit || !slices.Equal(d.Options, []string{"continue", "reject"}) || d.Subject == nil || d.Subject.Digest != head2 ||
				!strings.Contains(d.Question, "ci") {
				t.Errorf("loop exit = %+v, want continue or reject on head 2 naming the failing check", d)
			}
			if got := e.validationDoc(t, "validate-2-result"); !equalResult(got, failed2) {
				t.Errorf("validate-2-result = %+v, want %+v", got, failed2)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), exit)

			e.answer(t, "validate-x2", choice)
			if choice == "reject" {
				rejected := interp.Next{Wait: interp.WaitDirector, Step: "validate", Decision: "validate-x2"}
				want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "validate-x2"}), rejected)
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), rejected)
				// The Director re-ran the failing check, which now passes: the rejected step reads it again.
				f.SetChecks(repoRemote, head2, rerun(forge.CheckPassed)...)
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
				if got := e.validationDoc(t, "validate-3-result"); !equalResult(got, result(head2, "passed", rerun(forge.CheckPassed))) {
					t.Errorf("validate-3-result = %+v", got)
				}
				return
			}
			e.script.Expect(buildExp(t, 3, head3, failed2.digest(t)),
				reviewExp(t, "review", "reviewer", 3, head3, ptr(bound(t, workflow.VerdictApproved, head2, nil)), bound(t, workflow.VerdictApproved, head3, nil)))
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "validate-x2"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 3})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 3})
			f.SetChecks(repoRemote, head3, ciPassed...)
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 3}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
		})
	}
}

// TestValidateFailedSameHead: a build round that answers failed checks with the same head spends no round while
// the head's failed check runs are unchanged, launching nothing; a re-run of the failing check is new evidence:
// passing, it reaches the merge decision with the unchanged head published once, and failing again, it is the
// next round, here past max 1, so the loop exit opens.
func TestValidateFailedSameHead(t *testing.T) {
	for _, b := range backends {
		for _, passes := range []bool{true, false} {
			t.Run(b.name+map[bool]string{true: "/re-run passes", false: "/re-run fails"}[passes], func(t *testing.T) {
				e, f := newValidateEnv(t, b)
				failed := result(head, "failed", ciFailed, "ci")
				approved := bound(t, workflow.VerdictApproved, head, nil)
				// Build 2's head document is another revision of build.head, so review 2 reviews the same head again.
				e.script.Expect(buildExp(t, 2, head, failed.digest(t)), reviewExp(t, "review", "reviewer", 2, head, &approved, approved))
				f.SetChecks(repoRemote, head, ciFailed...)
				want(t, e.toValidate(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 2})
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), waitChecks)
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), waitChecks)
				if _, ok := e.documents(t)["validate-2-result"]; ok {
					t.Error("unchanged failed checks recorded validate-2-result")
				}
				f.SetChecks(repoRemote, head, rerun(forge.CheckPending)...)
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), waitChecks)
				if !passes {
					f.SetChecks(repoRemote, head, rerun(forge.CheckFailed)...)
					want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "validate", Decision: "validate-x2"})
					if got, w := e.validationDoc(t, "validate-2-result"), result(head, "failed", rerun(forge.CheckFailed), "ci"); !equalResult(got, w) {
						t.Errorf("validate-2-result = %+v, want %+v", got, w)
					}
					return
				}
				f.SetChecks(repoRemote, head, rerun(forge.CheckPassed)...)
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
				if got := e.validationDoc(t, "validate-2-result"); !equalResult(got, result(head, "passed", rerun(forge.CheckPassed))) {
					t.Errorf("validate-2-result = %+v", got)
				}
				if publishes, _ := f.Calls(); publishes != 1 {
					t.Errorf("publishes = %d, want 1 for the unchanged head", publishes)
				}
			})
		}
	}
}

// TestValidateWithoutLoopRule: failed checks without a failed rule wait for the Director, telling it once, and are
// read again, so a re-run that passes completes the step.
func TestValidateWithoutLoopRule(t *testing.T) {
	def := editValidate(t, func(s *workflow.Step) { s.On = nil })
	e, f := newValidateEnvDefinition(t, backends[0], def)
	f.SetChecks(repoRemote, head, ciFailed...)
	director := interp.Next{Wait: interp.WaitDirector, Step: "validate"}
	want(t, e.toValidate(t), director)
	if !strings.Contains(e.messages(t)["app.t7.checks-failed.validate-1"], "Director") {
		t.Errorf("messages = %q, want checks-failed.validate-1 handing the Director the failure", e.messages(t))
	}
	n := len(e.inbox(t))
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), director)
	if got := len(e.inbox(t)); got != n {
		t.Errorf("waiting again sent %d messages", got-n)
	}
	f.SetChecks(repoRemote, head, rerun(forge.CheckPassed)...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	if got := e.validationDoc(t, "validate-2-result"); !equalResult(got, result(head, "passed", rerun(forge.CheckPassed))) {
		t.Errorf("validate-2-result = %+v", got)
	}
}

// TestValidateNoCI: in a repository configured no_ci, a head the forge reports no checks for passes with a result
// marked no_ci, while checks the forge does report still count.
func TestValidateNoCI(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _ := newValidateEnv(t, b)
			e.eng.Config.Repositories[0].NoCI = true
			want(t, e.toValidate(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			w := result(head, "passed", []forge.Check{})
			w.NoCI = true
			if got := e.validationDoc(t, "validate-1-result"); !equalResult(got, w) {
				t.Errorf("validate-1-result = %+v, want %+v", got, w)
			}
			if msg := e.messages(t)["app.t7.checks-passed.validate-1"]; !strings.Contains(msg, "no CI") || !strings.Contains(msg, head) {
				t.Errorf("checks-passed message = %q, want it naming no CI and the head", msg)
			}
		})
	}
	t.Run("reported checks count", func(t *testing.T) {
		e, f := newValidateEnv(t, backends[0])
		e.eng.Config.Repositories[0].NoCI = true
		e.script.Expect(buildExp(t, 2, head2, result(head, "failed", ciFailed, "ci").digest(t)))
		f.SetChecks(repoRemote, head, ciFailed...)
		want(t, e.toValidate(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
	})
}

// TestValidateChecksRefresh: with a checks refresh, a waiting validate step reads the same head's checks again
// only once the refresh has passed since its last read.
func TestValidateChecksRefresh(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			e.eng.Now = func() time.Time { return now }
			e.eng.ChecksRefresh = time.Minute
			want(t, e.toValidate(t), waitChecks)
			f.SetChecks(repoRemote, head, ciPassed...)
			now = now.Add(59 * time.Second)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), waitChecks)
			if _, ok := e.documents(t)["validate-1-result"]; ok {
				t.Error("an advance within the refresh read the checks")
			}
			now = now.Add(time.Second)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if got := e.validationDoc(t, "validate-1-result"); !equalResult(got, result(head, "passed", ciPassed)) {
				t.Errorf("validate-1-result = %+v", got)
			}
		})
	}
}

// editValidate is validate.json with its validate step edited.
func editValidate(t *testing.T, edit func(*workflow.Step)) *workflow.Definition {
	t.Helper()
	def := loadWorkflow(t, "validate")
	for i := range def.Steps {
		if def.Steps[i].ID == "validate" {
			edit(&def.Steps[i])
		}
	}
	if err := workflow.Validate(def); err != nil {
		t.Fatal(err)
	}
	return def
}

// TestValidateRefusals: validate without a forge, and of a head no publish step pushed, is unsupported; a forge
// that cannot answer fails the advance and records nothing.
func TestValidateRefusals(t *testing.T) {
	t.Run("unpublished head", func(t *testing.T) {
		e, _ := newValidateEnvDefinition(t, backends[0], editValidate(t, func(s *workflow.Step) { s.Inputs["head"] = workflow.Refs{"build.head"} }))
		e.accepted(t)
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
		if err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}); !errors.Is(err, interp.ErrUnsupported) {
			t.Errorf("validate of an unpublished head = %v, want ErrUnsupported", err)
		}
	})
	t.Run("forge", func(t *testing.T) {
		e, _ := newValidateEnv(t, backends[0])
		e.accepted(t)
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
		e.eng.Forge = publishOnly{e.eng.Forge}
		if err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}); err == nil || !strings.Contains(err.Error(), "checks unavailable") {
			t.Errorf("validate with an unreachable forge = %v, want the forge's error", err)
		}
		for name := range e.documents(t) {
			if strings.HasPrefix(name, "validate-") {
				t.Errorf("a failed read recorded %s", name)
			}
		}
		e.eng.Forge = nil
		if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, interp.ErrUnsupported) {
			t.Errorf("validate without a forge = %v, want ErrUnsupported", err)
		}
	})
}

// publishOnly is a forge that publishes but cannot read checks.
type publishOnly struct{ interp.Forge }

func (publishOnly) Checks(_ context.Context, _ forge.Commit) ([]forge.Check, error) {
	return nil, errors.New("checks unavailable")
}

// TestDefaultWorkflowDelivers runs the shipped default workflow, with its grill overridden to an Artisan as a
// repository meant to run unattended does, from task creation to a merged pull request over the fake forge:
// grill, research, plan, acceptance, build, review, publish, a failed validation that takes the loop back to
// build, the new head reviewed, published and validated, and the merge.
func TestDefaultWorkflowDelivers(t *testing.T) {
	shipped, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.ApplyOverrides(shipped, map[string]workflow.Override{"grill": {Actor: workflow.ArtisanActor}})
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Validate(def); err != nil {
		t.Fatal(err)
	}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			notes := []byte("Agreed: the change is small.\n")
			research := []byte("The engine reads the forge's checks.\n")
			spec := specDigest(t)
			failed := result(head, "failed", ciFailed, "ci")
			reviewed := func(n int, h string, previous *workflow.ReviewOutcome) interptest.Expectation {
				inputs := map[string]string{"spec": spec, "head": "sha256:" + sum(headFile(h))}
				if previous != nil {
					inputs["previous"] = outcomeDigest(*previous)
				}
				return interptest.Expectation{Step: "review", Attempt: n, Inputs: inputs, Standards: []string{"standards/go.md", "standards/review.md"},
					Respond: interptest.Review(bound(t, workflow.VerdictApproved, h, nil))}
			}
			built := func(n int, h string, inputs map[string]string) interptest.Expectation {
				return interptest.Expectation{Step: "build", Attempt: n, Inputs: inputs, Standards: []string{"standards/go.md"},
					Respond: interptest.Succeed("built", interptest.File{Path: "head", Data: headFile(h)})}
			}
			e := newEnvDefinition(t, b, def,
				interptest.Expectation{Step: "grill", Attempt: 1, Inputs: map[string]string{}, Standards: []string{"standards/go.md"},
					Respond: interptest.Succeed("grilled", interptest.File{Path: "notes.md", Data: notes})},
				interptest.Expectation{Step: "research", Attempt: 1, Inputs: map[string]string{}, Standards: []string{"standards/go.md"},
					Respond: interptest.Succeed("researched", interptest.File{Path: "research.md", Data: research})},
				// Both notes reach the plan on port context; the script compares the last one wired.
				interptest.Expectation{Step: "plan", Attempt: 1, Inputs: map[string]string{"context": "sha256:" + sum(research)}, Standards: []string{"standards/go.md"},
					Respond: interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})},
				built(1, head, map[string]string{"spec": spec}),
				reviewed(1, head, nil),
				built(2, head2, map[string]string{"spec": spec, "feedback": failed.digest(t)}),
				reviewed(2, head2, ptr(bound(t, workflow.VerdictApproved, head, nil))),
			)
			f := forgetest.NewFake()
			f.SetBranch(repoRemote, "main", baseHead)
			f.AddCommit(repoPath, head)
			f.AddCommit(repoPath, head2)
			f.SetChecks(repoRemote, head, ciFailed...)
			e.eng.Forge = f
			e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoPath, Remote: repoRemote, MergePolicy: config.MergeApproval}}

			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "grill", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			e.answer(t, "accept-1", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 1})
			// Review 1 approves: head 1 is published, its checks fail and the failed loop sends the task back to build.
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), waitChecks)
			f.SetChecks(repoRemote, head2, ciPassed...)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), interp.Next{Wait: interp.WaitDone, Step: "deliver"})

			prs := f.PullRequests(repoRemote)
			if len(prs) != 1 || prs[0].State != forge.Merged || prs[0].Head != head2 || f.Branch(repoRemote, "main") != prs[0].MergeCommit {
				t.Errorf("pull requests = %+v, main = %s, want one merged at head 2", prs, f.Branch(repoRemote, "main"))
			}
			if got := e.task(t).State; got != store.TaskDone {
				t.Errorf("task state = %q, want done", got)
			}
			docs := e.documents(t)
			for _, name := range []string{"grill-1-notes", "research-1-notes", "publish-1-pr", "publish-2-pr", "validate-1-result", "validate-2-result", "deliver-1-merge"} {
				if _, ok := docs[name]; !ok {
					t.Errorf("%s not recorded", name)
				}
			}
		})
	}
}

// TestValidateReplay: a failure around recording a passed or failed result replays to one result, one message
// and, after a failed one, one launch of the loop's target.
func TestValidateReplay(t *testing.T) {
	for _, b := range backends {
		for _, verdict := range []string{"passed", "failed"} {
			for _, after := range []bool{false, true} {
				t.Run(b.name+"/"+verdict+map[bool]string{false: "/before", true: "/after"}[after], func(t *testing.T) {
					e, f := newValidateEnv(t, b)
					checks, wait := ciPassed, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"}
					if verdict == "failed" {
						checks, wait = ciFailed, interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2}
						e.script.Expect(buildExp(t, 2, head2, result(head, "failed", ciFailed, "ci").digest(t)))
					}
					f.SetChecks(repoRemote, head, checks...)
					fp := &failPut{Records: e.eng.Records, name: "validate-1-result", after: after}
					e.eng.Records = fp
					e.accepted(t)
					e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
					if err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}); !errors.Is(err, errInjected) {
						t.Fatalf("advance = %v, want the injected failure", err)
					}
					want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), wait)
					want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), wait)
					docs := e.documents(t)
					if _, ok := docs["validate-1-result"]; !ok {
						t.Errorf("validate-1-result not recorded")
					}
					if _, ok := docs["validate-2-result"]; ok {
						t.Errorf("a replay recorded validate-2-result")
					}
					if _, ok := e.messages(t)["app.t7.checks-"+verdict+".validate-1"]; !ok {
						t.Errorf("messages = %q, want checks-%s.validate-1", e.inbox(t), verdict)
					}
				})
			}
		}
	}
}
