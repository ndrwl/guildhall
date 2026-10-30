package interp_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// addContext records the Director's next context entry on app/t7.
func (e *env) addContext(t *testing.T, n int, text string) {
	t.Helper()
	if rc, err := e.h.AddTaskContext("task-context:"+itoa(n), "app", "t7", text); err != nil || rc.Record != store.TaskContextRecord(n) {
		t.Fatalf("AddTaskContext %d = %+v, %v", n, rc, err)
	}
}

// boundContext is the path of the context input attempt n of step's binding records, empty when it has none.
func (e *env) boundContext(t *testing.T, step string, n int) string {
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
		Inputs []struct{ Port, Path string } `json:"inputs"`
	}
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, in := range b.Inputs {
		if in.Port == interp.ContextPort {
			paths = append(paths, in.Path)
		}
	}
	if len(paths) > 1 {
		t.Fatalf("%s-%d has %d context inputs: %q", step, n, len(paths), paths)
	}
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

// contextDoc reads the task's context document name, checking its recorded digest.
func (e *env) contextDoc(t *testing.T, name string) (store.DocumentRef, string) {
	t.Helper()
	d, ok := e.documents(t)[name]
	if !ok {
		t.Fatalf("no document %s", name)
	}
	data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(d.Path)))
	if err != nil || sum(data) != d.SHA256 {
		t.Fatalf("document %s: %v", d.Path, err)
	}
	return d, string(data)
}

// TestTaskContextReachesLaterAttemptsOnly: an entry recorded before the plan is bound reaches it as context-1; a
// second entry, added after the plan succeeded, re-runs nothing, and the build, bound after it, gets context-2 with
// both entries fenced, while the plan's binding keeps context-1. The prompt lists the context under Inputs.
func TestTaskContextReachesLaterAttemptsOnly(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			script := straightScript(t)
			script[0].Inputs = map[string]string{"context": interptest.AnyDigest}
			script[1].Inputs["context"] = interptest.AnyDigest
			e := newEnv(t, b, script[:2]...)
			first := "Guildmaster: plain `guildhall director start` must record the pane."
			e.addContext(t, 1, first)

			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			c1, text1 := e.contextDoc(t, "context-1")
			if c1.Path != "tasks/app/t7/context-1.md" || e.boundContext(t, "plan", 1) != c1.Path {
				t.Fatalf("context-1 = %+v, plan-1 bound %q", c1, e.boundContext(t, "plan", 1))
			}
			for _, part := range []string{"# Guildmaster context for task app/t7\n", "## Context 1\n", "by director.", "```text\n" + first + "\n```\n"} {
				if !strings.Contains(text1, part) {
					t.Errorf("context-1 lacks %q:\n%s", part, text1)
				}
			}
			if prompt := retryPrompt(t, e, "plan", 1); !strings.Contains(prompt, c1.Path) {
				t.Errorf("plan-1 prompt does not list %s:\n%s", c1.Path, prompt)
			}

			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			second := "Supersedes context 1:\n```sh\nguildhall director start --copy ./\n```"
			e.addContext(t, 2, second)
			e.answer(t, "accept-1", "approved")
			// The plan finished before the entry: it is not stale, so the walk moves on to the build.
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			if n := len(slices.DeleteFunc(e.attempts(t), func(a store.StageAttempt) bool { return a.Step != "plan" })); n != 1 {
				t.Errorf("plan ran %d times", n)
			}
			c2, text2 := e.contextDoc(t, "context-2")
			if e.boundContext(t, "build", 1) != c2.Path || e.boundContext(t, "plan", 1) != c1.Path {
				t.Errorf("build-1 bound %q, plan-1 bound %q", e.boundContext(t, "build", 1), e.boundContext(t, "plan", 1))
			}
			for _, part := range []string{"## Context 1\n", "## Context 2\n", "```text\n" + first + "\n```\n", "````text\n" + second + "\n````\n"} {
				if !strings.Contains(text2, part) {
					t.Errorf("context-2 lacks %q:\n%s", part, text2)
				}
			}
			if strings.Index(text2, "## Context 1") > strings.Index(text2, "## Context 2") {
				t.Errorf("context-2 entries out of order:\n%s", text2)
			}
		})
	}
}

// TestTaskContextKeepsAnExistingBinding: an attempt whose binding was recorded before an entry, here one a cap
// refused to start, launches with the inputs it was bound with and no context.
func TestTaskContextKeepsAnExistingBinding(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, planExpectation(t, interptest.Fail("no plan")))
			records := e.h.Records
			e.eng.Records = capRefused{records}
			if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, store.ErrAttemptCap) {
				t.Fatalf("capped advance: %v", err)
			}
			e.addContext(t, 1, "Too late for this binding.")
			e.eng.Records = records
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			if got := e.boundContext(t, "plan", 1); got != "" {
				t.Errorf("the existing plan-1 binding took context %q", got)
			}
			if _, ok := e.documents(t)["context-1"]; ok {
				t.Error("context-1 was written for a binding that was already recorded")
			}
		})
	}
}

// TestTaskContextRelaunchKeepsThePreparation: a build launched with context-1 whose acknowledgement was lost is
// relaunched, after a second entry, with the same published preparation, which still matches its binding.
func TestTaskContextRelaunchKeepsThePreparation(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			plan := straightScript(t)[0]
			plan.Inputs = map[string]string{"context": interptest.AnyDigest}
			e := newEnv(t, b, plan)
			e.addContext(t, 1, "Keep the flag names.")
			l := &buildLauncher{script: e.script, launch: func(call int, spec interp.Spec) (interp.Handle, error) {
				if call == 1 {
					return interp.Handle{}, errAckLost
				}
				return interp.Handle{}, errLiveOwner
			}}
			if err := toBuild(t, e, l); !errors.Is(err, errAckLost) {
				t.Fatalf("first build launch: %v, want the lost acknowledgement", err)
			}
			e.addContext(t, 2, "And the defaults.")
			if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}); !errors.Is(err, errLiveOwner) {
				t.Fatalf("replay: %v, want the live owner's refusal", err)
			}
			if len(l.specs) != 2 || !reflect.DeepEqual(l.specs[1], l.specs[0]) {
				t.Fatalf("replay launched %+v, want the first spec again", l.specs)
			}
			c1, _ := e.contextDoc(t, "context-1")
			if got := e.boundContext(t, "build", 1); got != c1.Path {
				t.Errorf("build-1 bound %q, want %q", got, c1.Path)
			}
			if _, ok := e.documents(t)["context-2"]; ok {
				t.Error("context-2 was written though no attempt was bound after it")
			}
		})
	}
}

// TestNoTaskContextAddsNothing: a task without entries gets no context document and no context input.
func TestNoTaskContextAddsNothing(t *testing.T) {
	e := newEnv(t, backends[0], straightScript(t)[0])
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	if got := e.boundContext(t, "plan", 1); got != "" {
		t.Errorf("plan-1 bound context %q", got)
	}
	for name := range e.documents(t) {
		if strings.HasPrefix(name, "context-") {
			t.Errorf("document %s recorded", name)
		}
	}
}
