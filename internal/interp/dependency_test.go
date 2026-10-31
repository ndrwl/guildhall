package interp_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// heldAt is where the dependency gate holds the director-grill workflow: research, after the grill.
var heldAt = interp.Next{Wait: interp.WaitDependencies, Step: "research"}

// withTasks is b with tasks created in its records before app/t7, such as dependencies in other repositories.
func withTasks(b backend, tasks ...store.Task) backend {
	return backend{b.name, func(t *testing.T) (interptest.RecordsHarness, messaging.Mailbox) {
		h, mailbox := b.new(t)
		for _, task := range tasks {
			if _, err := h.CreateTask("task:"+task.Repository+":"+task.ID, task); err != nil {
				t.Fatal(err)
			}
		}
		return h, mailbox
	}}
}

// libTask is a task of another repository, lib/l1, for a cross-repository dependency.
func libTask(def *workflow.Definition) store.Task {
	return store.Task{Repository: "lib", ID: "l1", Title: "A library change", Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64)}
}

// newDependentEnv is the director-grill workflow for app/t7, which depends on app/t3 and the tasks in deps, created
// before it in b's records.
func newDependentEnv(t *testing.T, b backend, def *workflow.Definition, deps []store.Task, exps ...interptest.Expectation) *env {
	t.Helper()
	refs := []store.TaskRef{{Repository: "app", ID: "t3"}}
	for _, d := range deps {
		refs = append(refs, store.TaskRef{Repository: d.Repository, ID: d.ID})
	}
	return newEnvTasks(t, withTasks(b, deps...), def, func(task *store.Task) { task.DependsOn = refs }, exps...)
}

// grilled runs the grill to its finish; the next advance meets the dependency gate.
func (e *env) grilled(t *testing.T) interp.Next {
	t.Helper()
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1})
	finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
	return e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1})
}

// held returns the Director's dependency-held messages by id.
func (e *env) held(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for id, text := range e.messages(t) {
		if strings.HasPrefix(id, "app.t7.dependency-held.") {
			out[id] = text
		}
	}
	return out
}

// stepsAttempted returns the steps of app/t7's recorded attempts.
func (e *env) stepsAttempted(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, a := range e.attempts(t) {
		out = append(out, a.Step)
	}
	return out
}

func (e *env) closeTask(t *testing.T, repo, id string, state store.TaskState) {
	t.Helper()
	if _, err := e.h.Records.SetTaskState("close:"+repo+":"+id, repo, id, state); err != nil {
		t.Fatal(err)
	}
}

// TestDependencyGateHoldsResearch: with app/t3 open, the grill runs and finishes, then research is held: nothing is
// recorded for it, one dependency-held message is sent, and advancing again in the same state sends nothing. A
// failed attempt of the dependency sends a new message; once it is done, research starts.
func TestDependencyGateHoldsResearch(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := loadWorkflow(t, "director-grill")
			e := newDependentEnv(t, b, def, nil, researchExpectation([]byte("# Research\n")))
			want(t, e.grilled(t), heldAt)
			if got := e.stepsAttempted(t); !slices.Equal(got, []string{"grill"}) {
				t.Fatalf("attempts = %v, want only the grill's", got)
			}
			if _, err := os.Stat(e.stageDir("research", 1)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a held advance prepared research: %v", err)
			}
			if d := e.documents(t)["grill-1-notes"]; d.Name == "" {
				t.Error("the grill's notes were not recorded while held")
			}
			first := e.held(t)
			if len(first) != 1 {
				t.Fatalf("dependency-held messages = %v, want one", first)
			}
			for id, text := range first {
				for _, part := range []string{"research", "app/t3: in progress", "guildhall task start --ignore-deps --reason"} {
					if !strings.Contains(text, part) {
						t.Errorf("message %s does not name %q:\n%s", id, part, text)
					}
				}
				m, err := e.mailbox.Read(id)
				if err != nil || m.Type != store.MessageDependencyHeld {
					t.Errorf("message type %q, %v; want %q", m.Type, err, store.MessageDependencyHeld)
				}
			}

			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), heldAt)
			if got := e.held(t); len(got) != 1 {
				t.Fatalf("a second held advance in the same state sent %v", got)
			}
			// The dependency moving through its steps is still in progress.
			if _, err := e.h.Records.StartAttempt("dep-plan-1", "app", "t3", "plan"); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), heldAt)
			if got := e.held(t); len(got) != 1 {
				t.Fatalf("a dependency in progress at another step sent %v", got)
			}

			if _, err := e.h.Records.FinishAttempt("dep-plan-1-finish", "app", "t3", "plan", 1, store.AttemptFailed); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), heldAt)
			second := e.held(t)
			if len(second) != 2 {
				t.Fatalf("a failed dependency sent %v, want a second message", second)
			}
			for id, text := range second {
				if _, ok := first[id]; !ok && !strings.Contains(text, "app/t3: waiting for the Director: plan attempt 1 failed") {
					t.Errorf("new message %s:\n%s", id, text)
				}
			}

			e.closeTask(t, "app", "t3", store.TaskDone)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			if got := e.stepsAttempted(t); !slices.Equal(got, []string{"grill", "research"}) {
				t.Errorf("attempts = %v", got)
			}
		})
	}
}

// TestDependencyQuestionIsNotWaiting: a dependency whose only open decision is a Director's question is not
// reported as waiting for the Guildmaster.
func TestDependencyQuestionIsNotWaiting(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := loadWorkflow(t, "director-grill")
			e := newDependentEnv(t, b, def, []store.Task{libTask(def)}, researchExpectation([]byte("# Research\n")))
			if _, err := e.h.Records.OpenDecision("dep-question", store.Decision{Repository: "lib", TaskID: "l1", ID: "q1", Kind: store.DecisionQuestion,
				Options: []string{"a", "b"}, Question: "Which grill first?"}); err != nil {
				t.Fatal(err)
			}
			want(t, e.grilled(t), heldAt)
			msgs := e.held(t)
			if len(msgs) != 1 {
				t.Fatalf("messages = %v", msgs)
			}
			for _, text := range msgs {
				if !strings.Contains(text, "lib/l1: ") || strings.Contains(text, "waiting for the Guildmaster") {
					t.Errorf("message reports the question as a wait:\n%s", text)
				}
			}
			// The question, still open, does not hold the dependent once its dependencies are done.
			e.closeTask(t, "app", "t3", store.TaskDone)
			e.closeTask(t, "lib", "l1", store.TaskDone)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
		})
	}
}

// TestDependencyGateStates: an open decision, a cancelled dependency and one in another repository are each
// named in the message, in repository then id order, and research waits until every one is done.
func TestDependencyGateStates(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := loadWorkflow(t, "director-grill")
			e := newDependentEnv(t, b, def, []store.Task{libTask(def)}, researchExpectation([]byte("# Research\n")))
			if _, err := e.h.Records.OpenDecision("dep-decision", store.Decision{Repository: "lib", TaskID: "l1", ID: "accept-1", Kind: store.DecisionApproval,
				Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + strings.Repeat("d", 64)}, Options: []string{"approved", "changes_requested"}, Question: "Accept?"}); err != nil {
				t.Fatal(err)
			}
			want(t, e.grilled(t), heldAt)
			msgs := e.held(t)
			if len(msgs) != 1 {
				t.Fatalf("messages = %v", msgs)
			}
			for _, text := range msgs {
				i, j := strings.Index(text, "app/t3: in progress"), strings.Index(text, "lib/l1: waiting for the Guildmaster: decision accept-1 open")
				if i < 0 || j < 0 || i > j {
					t.Errorf("message does not list app/t3 then lib/l1's open decision:\n%s", text)
				}
			}

			e.closeTask(t, "app", "t3", store.TaskCancelled)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), heldAt)
			found := false
			for _, text := range e.held(t) {
				found = found || strings.Contains(text, "app/t3: cancelled; it can never merge; start anyway with guildhall task start --ignore-deps --reason <reason> --repo app t7")
			}
			if !found {
				t.Errorf("no message names the cancelled dependency: %v", e.held(t))
			}

			// A cancelled dependency never merges: only the override starts the task.
			e.closeTask(t, "lib", "l1", store.TaskDone)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), heldAt)
			if _, err := e.h.OverrideDependencies("dependency-override.app.t7", "app", "t7", "t3 was cancelled; t7 stands alone."); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
		})
	}
}

// TestDependencyGateQueuedForMerge: a dependency waiting in its merge queue is "queued for merge", and one whose merge
// is held is "merge held".
func TestDependencyGateQueuedForMerge(t *testing.T) {
	def := loadWorkflow(t, "director-grill")
	e := newDependentEnv(t, backends[1], def, []store.Task{libTask(def)})
	s := e.h.Records.(*store.Store)
	if _, err := s.PutDocument("dep-auth", store.DocumentRef{Repository: "app", TaskID: "t3", Name: "deliver-1-authorized", Path: "tasks/app/t3/deliver-1/authorized.json", SHA256: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JoinMergeQueue("dep-join", "app", "t3", "deliver-1", "document/deliver-1-authorized", ""); err != nil {
		t.Fatal(err)
	}
	want(t, e.grilled(t), heldAt)
	has := func(part string) bool {
		for _, text := range e.held(t) {
			if strings.Contains(text, part) {
				return true
			}
		}
		return false
	}
	if !has("app/t3: queued for merge, place 1 of 1") {
		t.Fatalf("no queued dependency: %v", e.held(t))
	}
	if _, err := s.SetMergeHold("dep-hold", "app", "t3", true); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), heldAt)
	if !has("app/t3: merge held") {
		t.Fatalf("no held dependency: %v", e.held(t))
	}
}

// TestDependencyGateCrossRepository: a dependency in another repository of the same store holds the task until it
// is done.
func TestDependencyGateCrossRepository(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := loadWorkflow(t, "director-grill")
			e := newDependentEnv(t, b, def, []store.Task{libTask(def)}, researchExpectation([]byte("# Research\n")))
			e.closeTask(t, "app", "t3", store.TaskDone)
			want(t, e.grilled(t), heldAt)
			for _, text := range e.held(t) {
				if !strings.Contains(text, "lib/l1: in progress") || strings.Contains(text, "app/t3") {
					t.Errorf("message lists other than the open lib/l1:\n%s", text)
				}
			}
			e.closeTask(t, "lib", "l1", store.TaskDone)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
		})
	}
}

// TestDependencyOverridePassesTheGate: the Director's override starts a held task while its dependency is open, and
// the gate never holds it again.
func TestDependencyOverridePassesTheGate(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := loadWorkflow(t, "director-grill")
			e := newDependentEnv(t, b, def, nil, researchExpectation([]byte("# Research\n")))
			want(t, e.grilled(t), heldAt)
			if _, err := e.h.OverrideDependencies("dependency-override.app.t7", "app", "t7", "t7 does not touch t3's files."); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			if got := len(e.held(t)); got != 1 {
				t.Errorf("dependency-held messages = %d, want the one sent before the override", got)
			}
		})
	}
}

// hiddenDependencies is Records with app/t7's dependencies hidden: the engine before the dependency gate.
type hiddenDependencies struct{ interp.Records }

func (h hiddenDependencies) Task(repo, id string) (store.Task, error) {
	t, err := h.Records.Task(repo, id)
	if repo == "app" && id == "t7" {
		t.DependsOn = nil
	}
	return t, err
}

// TestDependencyGatePassesATaskPastResearch: a task that reached research before the gate existed is not held with
// its dependency open, nor after the Director sends research back.
func TestDependencyGatePassesATaskPastResearch(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := loadWorkflow(t, "director-grill")
			feedback := "Also read the list's tests."
			research2 := researchExpectation([]byte("# Research, again\n"))
			research2.Attempt, research2.Inputs = 2, map[string]string{"feedback": "sha256:" + sum([]byte(feedback))}
			e := newDependentEnv(t, b, def, nil, researchExpectation([]byte("# Research\n")), research2)
			records := e.eng.Records
			e.eng.Records = hiddenDependencies{records}
			want(t, e.grilled(t), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			e.eng.Records = records

			e.eng.Hold = func() (bool, error) { return true, nil }
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitPaused, Step: "plan"})
			e.eng.Hold = nil
			want(t, e.advance(t, interp.Event{Kind: interp.StepSentBack, Step: "research", Attempt: 1, Reason: feedback}),
				interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 2})
			if got := e.held(t); len(got) != 0 {
				t.Errorf("a task past research was told it is held: %v", got)
			}
		})
	}
}

// TestDependencyGateBeforeThePauseHold: a held task waits for its dependencies whatever the pause hold says, and
// the hold is not read.
func TestDependencyGateBeforeThePauseHold(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newDependentEnv(t, b, loadWorkflow(t, "director-grill"), nil)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1})
			finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
			asked := 0
			e.eng.Hold = func() (bool, error) { asked++; return true, nil }
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), heldAt)
			if asked != 0 {
				t.Errorf("the pause hold was read %d times before the dependency gate", asked)
			}
		})
	}
}

// TestDependencyGateHoldsTheGrillOnlyAfter: the grill, a Director stage, is never held, even when the dependency is
// open from the start.
func TestDependencyGateHoldsTheGrillOnlyAfter(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newDependentEnv(t, b, loadWorkflow(t, "director-grill"), nil)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1})
			if got := e.held(t); len(got) != 0 {
				t.Errorf("the grill was held: %v", got)
			}
		})
	}
}

// TestDependencyGateHoldsANonAttemptStep: in a custom workflow whose first step after the grill is a Guildmaster
// review, the gate holds that step, opening no decision.
func TestDependencyGateHoldsANonAttemptStep(t *testing.T) {
	data := []byte(`{"schema": "guildhall.workflow/1", "name": "grill-then-review", "description": "The Guildmaster reviews the grill's notes first.",
  "steps": [
    {"id": "grill", "type": "grill", "version": 1, "settings": {"actor": "director"}},
    {"id": "check", "type": "document-review", "version": 1, "settings": {"actor": "guildmaster"}, "inputs": {"subject": ["grill.notes"]}},
    {"id": "plan", "type": "plan", "version": 1, "settings": {"profile": "planner"}, "inputs": {"context": ["grill.notes"]}},
    {"id": "accept", "type": "guildmaster-approval", "version": 1, "inputs": {"spec": ["plan.spec"]}},
    {"id": "build", "type": "build", "version": 1, "settings": {"profile": "builder"}, "inputs": {"spec": ["accept.spec"]}},
    {"id": "review", "type": "review", "version": 1, "settings": {"actor": "artisan", "profile": "reviewer"}, "inputs": {"spec": ["accept.spec"], "head": ["build.head"]}},
    {"id": "deliver", "type": "deliver", "version": 1, "inputs": {"head": ["build.head"], "review": ["review.outcome"]}}
  ]}`)
	def, err := workflow.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Validate(def); err != nil {
		t.Fatal(err)
	}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newDependentEnv(t, b, def, nil)
			want(t, e.grilled(t), interp.Next{Wait: interp.WaitDependencies, Step: "check"})
			if ds, err := e.h.Records.Decisions("app", "t7"); err != nil || len(ds) != 0 {
				t.Errorf("a held Guildmaster step opened decisions %+v, %v", ds, err)
			}
			e.closeTask(t, "app", "t3", store.TaskDone)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "check", Decision: "check-1"})
		})
	}
}

// TestDependencyGateReplayIsIdempotent: a held advance repeated after a crash, for any event the task last saw,
// records and sends nothing new.
func TestDependencyGateReplayIsIdempotent(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newDependentEnv(t, b, loadWorkflow(t, "director-grill"), nil)
			want(t, e.grilled(t), heldAt)
			attempts, docs, msgs := e.attempts(t), e.documents(t), e.messages(t)
			for range 2 {
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), heldAt)
			}
			if got := e.attempts(t); len(got) != len(attempts) {
				t.Errorf("replay recorded attempts %+v", got)
			}
			if got := e.documents(t); len(got) != len(docs) {
				t.Errorf("replay recorded %d documents, want %d", len(got), len(docs))
			}
			if got := e.messages(t); len(got) != len(msgs) {
				t.Errorf("replay sent messages %v, want %v", got, msgs)
			}
		})
	}
}

// TestDependencyGateFastForwards: a held task whose dependency merges starts research on the new main, because
// research's launch fast-forwards the untouched branch, not the gate; the grill and the held advances move
// nothing. A branch that cannot be moved sends one branch-kept message for the attempt and research proceeds on
// it, and a research retry fast-forwards again.
func TestDependencyGateFastForwards(t *testing.T) {
	for _, tc := range []struct {
		name string
		// block puts something in the worktree that keeps the branch where it is.
		block func(t *testing.T, ws *gittest.Workspaces, dir string)
	}{
		{"moved", nil},
		{"kept", func(t *testing.T, ws *gittest.Workspaces, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "merged.txt"), []byte("mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := loadWorkflow(t, "director-grill")
			research1 := researchExpectation(nil)
			research1.Respond = interptest.Fail("no notes")
			research2 := researchExpectation([]byte("# Research\n"))
			research2.Attempt = 2
			e := newDependentEnv(t, backends[0], def, nil, research1, research2)
			ws := gittest.NewWorkspaces()
			ws.SetDefault("origin", strings.Repeat("a", 40))
			e.eng.Workspaces = ws
			repoDir := t.TempDir()
			e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoDir, Remote: "origin"}}
			want(t, e.grilled(t), heldAt)
			if got := ws.FastForwards(); len(got) != 0 {
				t.Fatalf("a held advance fast-forwarded %+v", got)
			}
			work := filepath.Join(e.eng.StateDir, "app", "t7", "work")
			main := ws.AdvanceDefault("origin", "merged.txt")
			if tc.block != nil {
				tc.block(t, ws, work)
			}
			e.closeTask(t, "app", "t3", store.TaskDone)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			if got := ws.FastForwards(); len(got) != 1 || got[0].Dir != work || got[0].Branch != "guildhall/t7" {
				t.Fatalf("fast-forwards = %+v, want one of %s", got, work)
			}
			kept := e.messages(t)["app.t7.branch-kept.research-1"]
			if tc.block == nil {
				if got := ws.Head(work); got != main {
					t.Errorf("research runs at %s, want the new main %s", got, main)
				}
				if kept != "" {
					t.Errorf("a moved branch sent %q", kept)
				}
			} else {
				if !strings.Contains(kept, "merged.txt") || !strings.Contains(kept, "Step research attempt 1 of app/t7") {
					t.Errorf("branch-kept = %q, want the attempt and the reason", kept)
				}
				m, err := e.mailbox.Read("app.t7.branch-kept.research-1")
				if err != nil || m.Type != store.MessageBranchKept {
					t.Errorf("message type %q, %v", m.Type, err)
				}
			}
			if _, err := os.Stat(filepath.Join(work, "notes.md")); err != nil {
				t.Errorf("the grill's notes are gone from the worktree: %v", err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			if got := ws.FastForwards(); len(got) != 1 {
				t.Errorf("an advance that launched nothing fast-forwarded: %+v", got)
			}

			// A research retry fast-forwards again, onto a main that moved since.
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "research", Attempt: 1})
			later := ws.AdvanceDefault("origin", "later.txt")
			want(t, e.advance(t, retried("research", 1, "")), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 2})
			if got := ws.FastForwards(); len(got) != 2 {
				t.Errorf("fast-forwards after the retry = %+v, want a second one", got)
			}
			_, keptAgain := e.messages(t)["app.t7.branch-kept.research-2"]
			if tc.block == nil {
				if got := ws.Head(work); got != later {
					t.Errorf("research retry runs at %s, want the new main %s", got, later)
				}
				if keptAgain {
					t.Error("a moved branch sent branch-kept for the retry")
				}
			} else if !keptAgain {
				t.Error("the kept retry sent no branch-kept message of its own")
			}
		})
	}
}

// TestDependencyMessageTypesRegistered: the gate's held message and the launch's branch-kept message are engine
// types in the registry.
func TestDependencyMessageTypesRegistered(t *testing.T) {
	for _, typ := range []string{store.MessageDependencyHeld, store.MessageBranchKept} {
		if sender := store.MessageTypes[typ]; sender != "engine" {
			t.Errorf("store.MessageTypes[%q] = %q, want engine", typ, sender)
		}
	}
	if store.MessageDependencyHeld == "dependency-notice" {
		t.Error("dependency-held reuses the between-Directors dependency-notice type")
	}
}
