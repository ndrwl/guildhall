package interp_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// withWorkspaces gives e's engine the workspace double and repository app a path and remote, and returns the
// double and the workspace every attempt of task t7 must run in.
func withWorkspaces(t *testing.T, e *env) (*gittest.Workspaces, git.Workspace) {
	t.Helper()
	f := gittest.NewWorkspaces()
	e.eng.Workspaces = f
	repo := &e.eng.Config.Repositories[0]
	repo.Path, repo.Remote = filepath.Join(t.TempDir(), "app"), "git@github.com:example/app.git"
	return f, git.Workspace{Repository: repo.Path, Remote: repo.Remote, Branch: "guildhall/t7", Dir: filepath.Join(e.eng.StateDir, "app", "t7", "work")}
}

// TestAttemptsRunInTheTaskWorkspace: every attempt of a task is launched in the one workspace, a worktree of the
// repository on branch guildhall/<task>, ensured before each launch, and its outputs are read from there.
func TestAttemptsRunInTheTaskWorkspace(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			plan2 := planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)}))
			plan2.Attempt = 2
			e := newEnv(t, b, planExpectation(t, interptest.Fail("no plan")), plan2)
			f, ws := withWorkspaces(t, e)
			f.SetDefault(ws.Remote, strings.Repeat("b", 40))

			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			want(t, e.advance(t, retried("plan", 1, "")), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})

			if got := f.Calls(); len(got) != 2 || got[0] != ws || got[1] != ws {
				t.Errorf("Ensure calls = %+v, want %+v before each of two launches", got, ws)
			}
			for _, s := range e.script.Launched() {
				if s.WorkDir != ws.Dir {
					t.Errorf("attempt %s launched in %s, want the workspace %s", s.Identity.AttemptID, s.WorkDir, ws.Dir)
				}
			}
			if _, ok := e.documents(t)["plan-2-spec"]; !ok {
				t.Errorf("the plan's output was not recorded from the workspace: %v", e.documents(t))
			}
		})
	}
}

// TestWorkspaceFailureLaunchesNothing: when the workspace cannot be made, the attempt is recorded but nothing is
// launched or marked launched, and a later advance launches the same attempt once it can be.
func TestWorkspaceFailureLaunchesNothing(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)[0])
			f, ws := withWorkspaces(t, e)
			err := advanceErr(t, e, interp.Event{Kind: interp.TaskCreated})
			if err == nil || !strings.Contains(err.Error(), "no default branch") {
				t.Fatalf("Advance without a workspace = %v, want the workspace's error", err)
			}
			if got := len(e.script.Launched()); got != 0 {
				t.Fatalf("launched %d attempts without a workspace", got)
			}
			if _, err := os.Stat(filepath.Join(e.eng.StateDir, "app", "t7", "plan-1", "launched.json")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("attempt marked launched: %v", err)
			}
			f.SetDefault(ws.Remote, strings.Repeat("b", 40))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			if got := e.attempts(t); len(got) != 1 {
				t.Errorf("attempts = %+v, want the one recorded first", got)
			}
			if got := e.script.Launched(); len(got) != 1 || got[0].WorkDir != ws.Dir {
				t.Errorf("launches = %+v, want one in %s", got, ws.Dir)
			}
		})
	}
}

// TestWithoutWorkspacesTheWorkDirectoryIsPlain: an engine without Workspaces keeps the plain working directory.
func TestWithoutWorkspacesTheWorkDirectoryIsPlain(t *testing.T) {
	e := newEnv(t, backends[0], straightScript(t)[0])
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	dir := filepath.Join(e.eng.StateDir, "app", "t7", "work")
	if got := e.script.Launched(); len(got) != 1 || got[0].WorkDir != dir {
		t.Errorf("launches = %+v, want one in %s", got, dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("work directory %s: %v, want a directory", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("work directory %s is a worktree: %v", dir, err)
	}
}

// failingRetire is the workspace double with a Retire that always fails.
type failingRetire struct{ *gittest.Workspaces }

func (failingRetire) Retire(context.Context, git.Retirement) ([]string, error) {
	return nil, errors.New("git worktree remove: permission denied")
}

// newRetireEnv is the publish path with the workspace double over a repository whose remote's default branch is
// the build's head, so a worktree nobody changed holds nothing the merge did not deliver.
func newRetireEnv(t *testing.T, b backend) (*env, *gittest.Workspaces, git.Workspace) {
	t.Helper()
	e, fg := newPublishEnv(t, b)
	f, ws := withWorkspaces(t, e)
	fg.AddCommit(ws.Repository, head)
	f.SetDefault(ws.Remote, head)
	e.eng.Config.Repositories[0].Remote = ws.Remote
	return e, f, ws
}

// keptMessage returns the text of the one workspace-kept message, or "".
func (e *env) keptMessage(t *testing.T) string {
	t.Helper()
	var texts []string
	for id, text := range e.messages(t) {
		if strings.HasPrefix(id, "app.t7.workspace-kept.deliver-1.") {
			texts = append(texts, text)
		}
	}
	if len(texts) > 1 {
		t.Fatalf("workspace-kept messages = %q, want at most one", texts)
	}
	if len(texts) == 0 {
		return ""
	}
	return texts[0]
}

// TestMergeRetiresTheWorkspace: the merge that closes a task removes its workspace, whose only untracked files
// are the outputs Guildhall recorded, and tells the Director nothing more.
func TestMergeRetiresTheWorkspace(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := newRetireEnv(t, b)
			want(t, e.toSignOff(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
			if _, err := os.Stat(ws.Dir); !errors.Is(err, os.ErrNotExist) || f.Head(ws.Dir) != "" {
				t.Errorf("workspace still there after the merge: %v", err)
			}
			if got := e.keptMessage(t); got != "" {
				t.Errorf("workspace-kept = %q, want none", got)
			}
			if got := e.task(t).State; got != store.TaskDone {
				t.Errorf("task state = %q, want done", got)
			}
		})
	}
}

// TestMergeRetiresAnEarlierRecordedOutput: a workspace whose untracked file is a copy of the task's earliest
// recorded document at its path, while a document that sorts after it records other bytes there (as a narrowed
// review after a rebase, deliver-rebase-review-1, sorts before review-1), is removed with no workspace-kept message.
func TestMergeRetiresAnEarlierRecordedOutput(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := newRetireEnv(t, b)
			want(t, e.toSignOff(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			spec, ok := e.documents(t)["plan-1-spec"]
			if !ok {
				t.Fatal("plan-1-spec not recorded")
			}
			if _, err := os.Stat(filepath.Join(ws.Dir, "spec.md")); err != nil {
				t.Fatalf("the workspace holds no spec.md: %v", err)
			}
			later := store.DocumentRef{Repository: "app", TaskID: "t7", Name: "research-1-spec", Path: "tasks/app/t7/research-1/spec.md", SHA256: sum([]byte("a later spec\n"))}
			if later.SHA256 == spec.SHA256 {
				t.Fatal("the later spec has the recorded spec's bytes")
			}
			if _, err := e.h.Records.PutDocument("test.later-spec", later); err != nil {
				t.Fatal(err)
			}
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
			if got := e.keptMessage(t); got != "" {
				t.Errorf("workspace-kept = %q, want none", got)
			}
			if _, err := os.Stat(ws.Dir); !errors.Is(err, os.ErrNotExist) || f.Head(ws.Dir) != "" {
				t.Errorf("workspace still there after the merge: %v", err)
			}
		})
	}
}

// TestMergeKeepsUnmergedWork: a workspace holding work the merge did not deliver is kept, the Director is told what
// it holds, and the task still closes.
func TestMergeKeepsUnmergedWork(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _, ws := newRetireEnv(t, b)
			want(t, e.toSignOff(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if err := os.WriteFile(filepath.Join(ws.Dir, "notes.txt"), []byte("not delivered\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
			if _, err := os.Stat(filepath.Join(ws.Dir, "notes.txt")); err != nil {
				t.Errorf("kept workspace lost its file: %v", err)
			}
			if got := e.keptMessage(t); !strings.Contains(got, "notes.txt") || !strings.Contains(got, ws.Dir) {
				t.Errorf("workspace-kept = %q, want it to name %s and notes.txt", got, ws.Dir)
			}
			if got := e.task(t).State; got != store.TaskDone {
				t.Errorf("task state = %q, want done", got)
			}
		})
	}
}

// TestFailedRetirementNeverFailsTheMerge: a workspace that cannot be removed is reported to the Director, and the
// merged task still closes.
func TestFailedRetirementNeverFailsTheMerge(t *testing.T) {
	e, f, _ := newRetireEnv(t, backends[0])
	e.eng.Workspaces = failingRetire{f}
	want(t, e.toSignOff(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
	if got := e.keptMessage(t); !strings.Contains(got, "permission denied") {
		t.Errorf("workspace-kept = %q, want the failure", got)
	}
	if got := e.task(t).State; got != store.TaskDone {
		t.Errorf("task state = %q, want done", got)
	}
}

// interruptedRetire is the workspace double with a Retire interrupted by the advance's context ending.
type interruptedRetire struct {
	*gittest.Workspaces
	cancel context.CancelFunc
}

func (i interruptedRetire) Retire(ctx context.Context, _ git.Retirement) ([]string, error) {
	i.cancel()
	return nil, ctx.Err()
}

// TestInterruptedRetirementKeepsTheTaskOpen: an interrupt during retirement fails the advance before the task
// closes and tells the Director nothing, and the next advance retires the workspace and closes the task.
func TestInterruptedRetirementKeepsTheTaskOpen(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := newRetireEnv(t, b)
			want(t, e.toSignOff(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			e.answer(t, "deliver-1", "merge")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.eng.Workspaces = interruptedRetire{f, cancel}
			ev := interp.Event{Kind: interp.DecisionAnswered, Repository: "app", Task: "t7", Decision: "deliver-1"}
			if _, err := e.eng.Advance(ctx, ev); !errors.Is(err, context.Canceled) {
				t.Fatalf("interrupted Advance = %v, want context.Canceled", err)
			}
			if got := e.task(t).State; got == store.TaskDone {
				t.Errorf("task closed by an interrupted retirement")
			}
			if got := e.keptMessage(t); got != "" {
				t.Errorf("workspace-kept = %q, want none for an interrupt", got)
			}
			e.eng.Workspaces = f
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
			if _, err := os.Stat(ws.Dir); !errors.Is(err, os.ErrNotExist) || f.Head(ws.Dir) != "" {
				t.Errorf("workspace still there after the replay: %v", err)
			}
			if got := e.task(t).State; got != store.TaskDone {
				t.Errorf("task state = %q, want done", got)
			}
		})
	}
}

// TestRetirementOutputs: every document recorded at a path below its record directory is listed under that path,
// whatever order the document names sort in, and a document with no directory component is not.
func TestRetirementOutputs(t *testing.T) {
	doc := func(name, p, data string) store.DocumentRef {
		return store.DocumentRef{Repository: "app", TaskID: "t7", Name: name, Path: "tasks/app/t7/" + p, SHA256: sum([]byte(data))}
	}
	docs := []store.DocumentRef{
		doc("deliver-rebase-review-1-outcome", "deliver-rebase-review-1/outcome.json", "rebased"),
		doc("review-1-outcome", "review-1/outcome.json", "one"),
		doc("review-10-outcome", "review-10/outcome.json", "ten"),
		doc("review-9-outcome", "review-9/outcome.json", "nine"),
		doc("review-x1-outcome", "review-x1/outcome.json", "x1"),
		doc("plan-1-spec", "plan-1/spec.md", "spec"),
		doc("plan-1-inputs", "plan-1.inputs.json", "inputs"),
		{Repository: "app", TaskID: "t8", Name: "review-1-outcome", Path: "tasks/app/t8/review-1/outcome.json", SHA256: sum([]byte("other task"))},
	}
	got := interp.RetirementOutputs("app", "t7", docs)
	for rel, data := range map[string][]string{"outcome.json": {"rebased", "one", "ten", "nine", "x1"}, "spec.md": {"spec"}} {
		var want []string
		for _, d := range data {
			want = append(want, sum([]byte(d)))
		}
		if !slices.Equal(got[rel], want) {
			t.Errorf("outputs at %s = %q, want %q", rel, got[rel], want)
		}
	}
	if len(got) != 2 {
		t.Errorf("outputs = %q, want only outcome.json and spec.md", got)
	}
}
