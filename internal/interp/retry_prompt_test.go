package interp_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// retryPrompt is the prompt attempt n of step was rendered with.
func retryPrompt(t *testing.T, e *env, step string, n int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.stageDir(step, n), "prompt", "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// boundRetry is the retry context recorded in attempt n of step's binding, nil when it has none.
func boundRetry(t *testing.T, e *env, step string, n int) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, "tasks", "app", "t7", fmt.Sprintf("%s-%d.inputs.json", step, n)))
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Retry map[string]any `json:"retry"`
	}
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	return b.Retry
}

// wantRetrySection checks prompt has the retry section, after the identity header and before the role, holding
// every one of parts.
func wantRetrySection(t *testing.T, prompt string, parts ...string) {
	t.Helper()
	at := strings.Index(prompt, "## Why this is a retry\n")
	if at < 0 {
		t.Fatalf("prompt has no retry section:\n%s", prompt)
	}
	role := strings.Index(prompt, "## Role\n")
	if role < at || strings.Index(prompt, "\nAttempt: ") > at {
		t.Fatalf("retry section is not between the identity header and the role:\n%s", prompt)
	}
	section := prompt[at:role]
	for _, p := range parts {
		if !strings.Contains(section, p) {
			t.Errorf("retry section lacks %q:\n%s", p, section)
		}
	}
}

// TestRetryPromptSaysWhy: a retried attempt's binding records the retry, and its prompt quotes the Director's
// reason and the failed attempt's summary; the first attempt's prompt has no retry section.
func TestRetryPromptSaysWhy(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			plan2 := planExpectation(t, interptest.Fail("still no plan"))
			plan2.Attempt = 2
			e := newEnv(t, b, planExpectation(t, interptest.Fail("the spec was unreadable")), plan2)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			want(t, e.advance(t, retried("plan", 1, "the spec is fixed now")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})

			got := boundRetry(t, e, "plan", 2)
			if got["attempt"] != float64(1) || got["outcome"] != "failed" || got["reason"] != "the spec is fixed now" || got["summary"] != "the spec was unreadable" {
				t.Errorf("bound retry = %v", got)
			}
			prompt := retryPrompt(t, e, "plan", 2)
			wantRetrySection(t, prompt, "This is a retry of attempt 1, which failed.",
				"Director's reason:\n\n```text\nthe spec is fixed now\n```", "Previous summary:\n\n```text\nthe spec was unreadable\n```",
				"Address this in your attempt; the stage guide and required outputs still apply.")
			if strings.Contains(prompt, "Intake rejection:") {
				t.Error("a retry of an accepted failure shows an intake rejection")
			}
			if strings.Contains(retryPrompt(t, e, "plan", 1), "Why this is a retry") || boundRetry(t, e, "plan", 1) != nil {
				t.Error("the first attempt has a retry section")
			}
		})
	}
}

// TestRetryPromptShowsTheRejection: the retry of an attempt intake rejected quotes the rejection; with no reason
// the section says so and no reason message is sent.
func TestRetryPromptShowsTheRejection(t *testing.T) {
	plan2 := planExpectation(t, interptest.Fail("still no plan"))
	plan2.Attempt = 2
	e := newEnv(t, backends[0], planExpectation(t, interptest.Succeed("missing document")), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	rejection := e.attempts(t)[0].IntakeRejection
	if rejection == "" {
		t.Fatal("attempt 1 was not rejected")
	}
	want(t, e.advance(t, retried("plan", 1, "")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
	wantRetrySection(t, retryPrompt(t, e, "plan", 2), "Director's reason: None given.", "Intake rejection:\n\n```text\n"+rejection+"\n```",
		"Previous summary:\n\n```text\nmissing document\n```")
	if got := e.inbox(t); !slices.Equal(got, []string{"app.t7.attempt-failed.plan-1"}) {
		t.Errorf("Director inbox = %q", got)
	}
}

// TestRetryPromptOfAStoppedAttempt: a stopped attempt has no result, so the retry's prompt records no summary.
func TestRetryPromptOfAStoppedAttempt(t *testing.T) {
	plan2 := planExpectation(t, interptest.Fail("no plan"))
	plan2.Attempt = 2
	e := newEnv(t, backends[1], planExpectation(t, interptest.Hang()), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	if _, err := e.h.Records.FinishAttempt("stop-1", "app", "t7", "plan", 1, store.AttemptStopped); err != nil {
		t.Fatal(err)
	}
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	want(t, e.advance(t, retried("plan", 1, "it hung")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
	wantRetrySection(t, retryPrompt(t, e, "plan", 2), "This is a retry of attempt 1, which stopped.", "it hung", "Previous summary: None recorded.")
}

// crashAfterStart is Records whose first capped start is recorded but reported failed, as if the advance died
// between recording the attempt and rendering it.
type crashAfterStart struct {
	interp.Records
	crashed bool
}

func (c *crashAfterStart) StartCappedAttempt(m, repo, task, step string, caps store.AttemptCaps) (store.Receipt, error) {
	r, err := c.Records.StartCappedAttempt(m, repo, task, step, caps)
	if err == nil && !c.crashed {
		c.crashed = true
		return store.Receipt{}, errors.New("crash after start")
	}
	return r, err
}

// TestRetryPromptSurvivesACrashBeforeLaunch: when the advance dies after binding and recording attempt 2 but
// before rendering it, a later advance without the retry event still renders the section from the binding.
func TestRetryPromptSurvivesACrashBeforeLaunch(t *testing.T) {
	plan2 := planExpectation(t, interptest.Fail("still no plan"))
	plan2.Attempt = 2
	e := newEnv(t, backends[0], planExpectation(t, interptest.Fail("no plan")), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	records := e.eng.Records
	e.eng.Records = &crashAfterStart{Records: records}
	if err := advanceErr(t, e, retried("plan", 1, "the runner was killed")); err == nil {
		t.Fatal("retry through a crashed start succeeded")
	}
	e.eng.Records = records
	if _, err := os.Stat(filepath.Join(e.stageDir("plan", 2), "prompt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attempt 2 was rendered before the crash: %v", err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
	wantRetrySection(t, retryPrompt(t, e, "plan", 2), "the runner was killed", "no plan")
}

// TestRetryRepeatedAfterACapKeepsTheFirstReason: a retry queued at the running cap has already bound attempt 2, so
// repeating it with another reason keeps the first in both the prompt and the reason message.
func TestRetryRepeatedAfterACapKeepsTheFirstReason(t *testing.T) {
	plan2 := planExpectation(t, interptest.Fail("still no plan"))
	plan2.Attempt = 2
	e := newEnv(t, backends[0], planExpectation(t, interptest.Fail("no plan")), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.eng.Caps = store.AttemptCaps{MaxRunning: 1}
	occupy(t, e, "plan")
	want(t, e.advance(t, retried("plan", 1, "first reason")), interp.Next{Wait: interp.WaitQueued, Step: "plan", Attempt: 2, Position: 1})
	if _, err := e.h.Records.FinishAttempt("finish:plan", "app", "t8", "plan", 1, store.AttemptFailed); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, retried("plan", 1, "second reason")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
	prompt := retryPrompt(t, e, "plan", 2)
	wantRetrySection(t, prompt, "first reason")
	if strings.Contains(prompt, "second reason") {
		t.Error("the prompt took the repeated retry's reason")
	}
	if got := e.messages(t)["app.t7.attempt-retried.plan-1"]; got != "first reason" {
		t.Errorf("reason message = %q, want the first reason", got)
	}
}

// TestHeldRetryBindsNothing: a retry while the engine is held records no binding for the next attempt, so the
// repeated retry binds its own reason.
func TestHeldRetryBindsNothing(t *testing.T) {
	plan2 := planExpectation(t, interptest.Fail("still no plan"))
	plan2.Attempt = 2
	e := newEnv(t, backends[0], planExpectation(t, interptest.Fail("no plan")), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	held := true
	e.eng.Hold = func() (bool, error) { return held, nil }
	want(t, e.advance(t, retried("plan", 1, "held reason")), interp.Next{Wait: interp.WaitPaused, Step: "plan"})
	if _, ok := e.documents(t)["plan-2-inputs"]; ok {
		t.Error("a held retry bound attempt 2")
	}
	held = false
	want(t, e.advance(t, retried("plan", 1, "released reason")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
	wantRetrySection(t, retryPrompt(t, e, "plan", 2), "released reason")
}

// TestRetryPromptNamesOnlyTheLatestRetry: attempt 3's prompt names attempt 2's retry, not attempt 1's.
func TestRetryPromptNamesOnlyTheLatestRetry(t *testing.T) {
	exps := []interptest.Expectation{planExpectation(t, interptest.Fail("first failure"))}
	for n, summary := range []string{"second failure", "third failure"} {
		x := planExpectation(t, interptest.Fail(summary))
		x.Attempt = n + 2
		exps = append(exps, x)
	}
	e := newEnv(t, backends[0], exps...)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.advance(t, retried("plan", 1, "first reason"))
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2})
	e.advance(t, retried("plan", 2, "second reason"))
	prompt := retryPrompt(t, e, "plan", 3)
	wantRetrySection(t, prompt, "This is a retry of attempt 2, which failed.", "second reason", "second failure")
	for _, old := range []string{"attempt 1", "first reason", "first failure"} {
		if strings.Contains(prompt, old) {
			t.Errorf("attempt 3's prompt names %q", old)
		}
	}
}

// TestDirectorStageRetryPromptSaysWhy: a retried Director stage's prompt carries the retry section too.
func TestDirectorStageRetryPromptSaysWhy(t *testing.T) {
	e := newEnvDefinition(t, backends[0], loadWorkflow(t, "director-grill"))
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	finishStage(t, e, "grill", 1, adapter.StatusFailed)
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1})
	want(t, e.advance(t, retried("grill", 1, "the Guildmaster is back")), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 2})
	wantRetrySection(t, retryPrompt(t, e, "grill", 2), "This is a retry of attempt 1, which failed.", "the Guildmaster is back", "the Director finished grill")
	if strings.Contains(retryPrompt(t, e, "grill", 1), "Why this is a retry") {
		t.Error("the first Director stage attempt has a retry section")
	}
}

// TestRerunWithoutRetryHasNoRetrySection: a loop re-run of a succeeded step is no Director retry, so its prompt
// has no retry section.
func TestRerunWithoutRetryHasNoRetrySection(t *testing.T) {
	words := "changes_requested\nTighten the scope."
	feedback := map[string]string{"feedback": "sha256:" + sum([]byte(words))}
	plan2 := interptest.Expectation{Step: "plan", Attempt: 2, Profile: "planner", Inputs: feedback,
		Standards: []string{"standards/go.md"}, Respond: interptest.Fail("no plan")}
	e := newEnvDefinition(t, backends[0], loadWorkflow(t, "loop"), planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})), plan2)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.answer(t, "accept-1", words)
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
	if strings.Contains(retryPrompt(t, e, "plan", 2), "Why this is a retry") || boundRetry(t, e, "plan", 2) != nil {
		t.Error("a loop re-run has a retry section")
	}
}
