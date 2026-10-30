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

// linkedSpec is the document of task app/t2 these tests link to app/t7.
var linkedSpec = []byte("# Plan 1 spec of t2\n\nThe flag is --copy.\n")

// linkSource records task app/t2 with its document plan-1-spec, the file written under the document directory.
func (e *env) linkSource(t *testing.T) store.DocumentRef {
	t.Helper()
	src, err := e.h.Records.Task("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	src.ID, src.Title = "t2", "The linked task"
	if _, err := e.h.CreateTask("task:app:t2", src); err != nil {
		t.Fatal(err)
	}
	d := store.DocumentRef{Repository: "app", TaskID: "t2", Name: "plan-1-spec", Path: "tasks/app/t2/plan-1/spec.md", SHA256: sum(linkedSpec)}
	file := filepath.Join(e.eng.DocumentDir, filepath.FromSlash(d.Path))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, linkedSpec, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.Records.PutDocument("doc:t2:plan-1-spec", d); err != nil {
		t.Fatal(err)
	}
	return d
}

// link records the Director's link record n on app/t7, op on source with prior earlier records for it.
func (e *env) link(t *testing.T, n int, op store.TaskLinkOp, source store.DocumentRef, prior int, text string) {
	t.Helper()
	named := store.DocumentRef{Repository: source.Repository, TaskID: source.TaskID, Name: source.Name}
	if rc, err := e.h.AddTaskLink("task-link:"+itoa(n), "app", "t7", op, named, prior, text); err != nil || rc.Record != store.TaskLinkRecord(n) {
		t.Fatalf("AddTaskLink %d = %+v, %v", n, rc, err)
	}
}

type boundIn struct{ Port, Path, SHA256 string }

// boundLinked is the linked inputs attempt n of step's binding records, in order.
func (e *env) boundLinked(t *testing.T, step string, n int) []boundIn {
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
		Inputs []boundIn `json:"inputs"`
	}
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(b.Inputs, func(in boundIn) bool { return in.Port != interp.LinkedPort })
}

// TestTaskLinkReachesLaterAttemptsOnly: a link recorded before the plan is bound reaches it as the index links-1 and
// the linked document, by digest, both listed in the prompt and copied beside it; removing the link after the plan
// succeeded re-runs nothing, and the build, bound after the removal, gets no linked input, while the plan's binding
// keeps both.
func TestTaskLinkReachesLaterAttemptsOnly(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			script := straightScript(t)
			script[0].Inputs = map[string]string{interp.LinkedPort: interptest.AnyDigest}
			e := newEnv(t, b, script[:2]...)
			spec := e.linkSource(t)
			words := "Guildmaster: the build must follow `t2`'s spec."
			e.link(t, 1, store.TaskLinkAdd, spec, 0, words)

			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			index, text := e.contextDoc(t, "links-1")
			wantBound := []boundIn{{interp.LinkedPort, "tasks/app/t7/links-1.md", index.SHA256}, {interp.LinkedPort, spec.Path, spec.SHA256}}
			if got := e.boundLinked(t, "plan", 1); !reflect.DeepEqual(got, wantBound) {
				t.Fatalf("plan-1 linked inputs = %+v, want %+v", got, wantBound)
			}
			for _, part := range []string{"# Linked documents for task app/t7\n", "## Link 1: app/t2/plan-1-spec\n", "- Path: inputs/tasks/app/t2/plan-1/spec.md\n",
				"- Digest: sha256:" + spec.SHA256 + "\n", "by director.", "```text\n" + words + "\n```\n"} {
				if !strings.Contains(text, part) {
					t.Errorf("links-1 lacks %q:\n%s", part, text)
				}
			}
			prompt := retryPrompt(t, e, "plan", 1)
			for _, line := range []string{"- linked: inputs/tasks/app/t7/links-1.md (sha256:" + index.SHA256 + ")\n",
				"- linked: inputs/tasks/app/t2/plan-1/spec.md (sha256:" + spec.SHA256 + ")\n"} {
				if !strings.Contains(prompt, line) {
					t.Errorf("plan-1 prompt lacks %q:\n%s", line, prompt)
				}
			}
			if data, err := os.ReadFile(filepath.Join(e.stageDir("plan", 1), "prompt", "inputs", "tasks", "app", "t2", "plan-1", "spec.md")); err != nil || string(data) != string(linkedSpec) {
				t.Errorf("the Artisan's copy of the linked spec = %q, %v", data, err)
			}

			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			e.link(t, 2, store.TaskLinkRemove, spec, 1, "Guildmaster: the build no longer needs it.")
			e.answer(t, "accept-1", "approved")
			// The plan finished before the removal: it is not stale, so the walk moves on to the build.
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			if n := len(slices.DeleteFunc(e.attempts(t), func(a store.StageAttempt) bool { return a.Step != "plan" })); n != 1 {
				t.Errorf("plan ran %d times", n)
			}
			if got := e.boundLinked(t, "build", 1); len(got) != 0 {
				t.Errorf("build-1 bound linked inputs %+v after the removal", got)
			}
			if got := e.boundLinked(t, "plan", 1); !reflect.DeepEqual(got, wantBound) {
				t.Errorf("plan-1 linked inputs became %+v", got)
			}
			if _, ok := e.documents(t)["links-2"]; ok {
				t.Error("links-2 was written though no link is active")
			}
		})
	}
}

// TestTaskLinkAddedAfterAFinishedStepMakesItNotStale: a link added after the plan succeeded re-runs nothing, and the
// build, bound after it, gets the index and the document.
func TestTaskLinkAddedAfterAFinishedStepMakesItNotStale(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			script := straightScript(t)
			script[1].Inputs[interp.LinkedPort] = interptest.AnyDigest
			e := newEnv(t, b, script[:2]...)
			spec := e.linkSource(t)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			e.link(t, 1, store.TaskLinkAdd, spec, 0, "Guildmaster: read t2's spec.")
			e.answer(t, "accept-1", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			if n := len(slices.DeleteFunc(e.attempts(t), func(a store.StageAttempt) bool { return a.Step != "plan" })); n != 1 {
				t.Errorf("plan ran %d times", n)
			}
			if got := e.boundLinked(t, "plan", 1); len(got) != 0 {
				t.Errorf("plan-1 took linked inputs %+v", got)
			}
			if got := e.boundLinked(t, "build", 1); len(got) != 2 || got[0].Path != "tasks/app/t7/links-1.md" || got[1].Path != spec.Path || got[1].SHA256 != spec.SHA256 {
				t.Errorf("build-1 linked inputs = %+v", got)
			}
		})
	}
}

// TestTaskLinkKeepsAnExistingBinding: an attempt whose binding was recorded before a link, here one a cap refused to
// start, launches with the inputs it was bound with and no linked document.
func TestTaskLinkKeepsAnExistingBinding(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, planExpectation(t, interptest.Fail("no plan")))
			spec := e.linkSource(t)
			records := e.h.Records
			e.eng.Records = capRefused{records}
			if err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated}); !errors.Is(err, store.ErrAttemptCap) {
				t.Fatalf("capped advance: %v", err)
			}
			e.link(t, 1, store.TaskLinkAdd, spec, 0, "Too late for this binding.")
			e.eng.Records = records
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			if got := e.boundLinked(t, "plan", 1); len(got) != 0 {
				t.Errorf("the existing plan-1 binding took linked inputs %+v", got)
			}
			if _, ok := e.documents(t)["links-1"]; ok {
				t.Error("links-1 was written for a binding that was already recorded")
			}
		})
	}
}

// TestTaskLinkRelaunchKeepsThePreparation: a build launched with a linked document whose acknowledgement was lost is
// relaunched, after the link is removed, with the same published preparation, which still matches its binding.
func TestTaskLinkRelaunchKeepsThePreparation(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)[0])
			spec := e.linkSource(t)
			l := &buildLauncher{script: e.script, launch: func(call int, spec interp.Spec) (interp.Handle, error) {
				if call == 1 {
					return interp.Handle{}, errAckLost
				}
				return interp.Handle{}, errLiveOwner
			}}
			// The plan is bound before the link, so only the build gets it.
			e.eng.Launcher = l
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			e.link(t, 1, store.TaskLinkAdd, spec, 0, "Keep the flag names.")
			e.answer(t, "accept-1", "approved")
			if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}); !errors.Is(err, errAckLost) {
				t.Fatalf("first build launch: %v, want the lost acknowledgement", err)
			}
			e.link(t, 2, store.TaskLinkRemove, spec, 1, "Drop it.")
			if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}); !errors.Is(err, errLiveOwner) {
				t.Fatalf("replay: %v, want the live owner's refusal", err)
			}
			if len(l.specs) != 2 || !reflect.DeepEqual(l.specs[1], l.specs[0]) {
				t.Fatalf("replay launched %+v, want the first spec again", l.specs)
			}
			if got := e.boundLinked(t, "build", 1); len(got) != 2 || got[1].Path != spec.Path {
				t.Errorf("build-1 linked inputs = %+v", got)
			}
		})
	}
}

// TestNoTaskLinksAddsNothing: a task without link records gets no index and no linked input.
func TestNoTaskLinksAddsNothing(t *testing.T) {
	e := newEnv(t, backends[0], straightScript(t)[0])
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	if got := e.boundLinked(t, "plan", 1); len(got) != 0 {
		t.Errorf("plan-1 bound linked inputs %+v", got)
	}
	for name := range e.documents(t) {
		if strings.HasPrefix(name, "links-") {
			t.Errorf("document %s recorded", name)
		}
	}
}

// TestTaskLinkRefusesAMissingOrChangedDocument: a linked file deleted or changed after it was linked refuses the
// binding with an error naming the task, the link and its source, and nothing is bound or written.
func TestTaskLinkRefusesAMissingOrChangedDocument(t *testing.T) {
	for name, tc := range map[string]struct {
		spoil func(file string) error
		why   string
	}{
		"missing": {os.Remove, "file missing"},
		"changed": {func(file string) error { return os.WriteFile(file, []byte("other words\n"), 0o644) }, "holds sha256:" + sum([]byte("other words\n"))},
	} {
		for _, b := range backends {
			t.Run(name+"/"+b.name, func(t *testing.T) {
				e := newEnv(t, b)
				spec := e.linkSource(t)
				e.link(t, 1, store.TaskLinkAdd, spec, 0, "Read it.")
				if err := tc.spoil(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(spec.Path))); err != nil {
					t.Fatal(err)
				}
				err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated})
				if err == nil || !strings.Contains(err.Error(), "task app/t7: link 1 app/t2/plan-1-spec: ") || !strings.Contains(err.Error(), tc.why) {
					t.Fatalf("advance = %v, want the refusal of link 1 naming %q", err, tc.why)
				}
				docs := e.documents(t)
				if _, ok := docs["plan-1-inputs"]; ok {
					t.Error("plan-1 was bound without its linked document")
				}
				if _, ok := docs["links-1"]; ok {
					t.Error("links-1 was written though the binding was refused")
				}
				if got := e.script.Launched(); len(got) != 0 {
					t.Errorf("launched %d attempts", len(got))
				}
			})
		}
	}
}

// TestDirectorStageGetsTaskLinks: a Director stage, the grill, is given the linked documents too.
func TestDirectorStageGetsTaskLinks(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"))
			spec := e.linkSource(t)
			e.link(t, 1, store.TaskLinkAdd, spec, 0, "Grill with t2's spec in mind.")
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1})
			if got := e.boundLinked(t, "grill", 1); len(got) != 2 || got[0].Path != "tasks/app/t7/links-1.md" || got[1].Path != spec.Path || got[1].SHA256 != spec.SHA256 {
				t.Errorf("grill-1 linked inputs = %+v", got)
			}
			if _, err := os.Stat(filepath.Join(e.stageDir("grill", 1), "prompt", "inputs", filepath.FromSlash(spec.Path))); err != nil {
				t.Errorf("the grill's copy of the linked spec: %v", err)
			}
		})
	}
}
