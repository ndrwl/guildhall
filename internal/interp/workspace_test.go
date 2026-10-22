package interp_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
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
