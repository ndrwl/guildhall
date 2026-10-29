package interp_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// main0 is the remote's default branch as a fast-forward test starts.
var main0 = strings.Repeat("a", 40)

// newForwardEnv is the straight workflow for app/t7 with no depends_on, over the workspace double whose remote's
// default branch is at main0.
func newForwardEnv(t *testing.T, b backend, exps ...interptest.Expectation) (*env, *gittest.Workspaces, git.Workspace) {
	t.Helper()
	e := newEnv(t, b, exps...)
	ws, w := withWorkspaces(t, e)
	ws.SetDefault(w.Remote, main0)
	return e, ws, w
}

// failedBuild is build attempt n of the straight workflow failing, with feedback's digest as its feedback input
// when not empty.
func failedBuild(t *testing.T, n int, feedback string) interptest.Expectation {
	exp := buildExp(t, n, head, feedback)
	exp.Respond = interptest.Fail("nothing built")
	return exp
}

// recorded is the content of the recorded document name, trimmed.
func (e *env) recorded(t *testing.T, name string) string {
	t.Helper()
	d, ok := e.documents(t)[name]
	if !ok {
		t.Fatalf("document %s not recorded", name)
	}
	data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(d.Path)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// published is attempt n of step's published prompt.
func (e *env) published(t *testing.T, step string, n int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.stageDir(step, n), "prompt", render.PromptFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// branchKept returns the Director's branch-kept messages by id.
func (e *env) branchKept(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for id, text := range e.messages(t) {
		if strings.HasPrefix(id, "app.t7.branch-kept.") {
			out[id] = text
		}
	}
	return out
}

// behindSection is the prompt's "Behind main" section, or "" when it has none.
func behindSection(prompt string) string {
	_, rest, ok := strings.Cut(prompt, "\n## Behind main\n")
	if !ok {
		return ""
	}
	section, _, _ := strings.Cut(rest, "\n## ")
	return section
}

// TestUntouchedBranchStartsOnMain is d23's failure: a task with no depends_on whose branch has no commits of its
// own, main moves on, and the next build attempt, a retry, a send-back or the first build after the plan, starts
// on the new main tip and records it as its base.
func TestUntouchedBranchStartsOnMain(t *testing.T) {
	feedback := "Build it again on the new main."
	for _, tc := range []struct {
		name string
		exps func(t *testing.T) []interptest.Expectation
		// run takes the task to the build attempt that must start on main, moving main with advance on the way,
		// and returns that attempt's number.
		run func(t *testing.T, e *env, advance func() string) int
	}{
		{"retry", func(t *testing.T) []interptest.Expectation {
			return []interptest.Expectation{straightScript(t)[0], failedBuild(t, 1, ""), failedBuild(t, 2, "")}
		}, func(t *testing.T, e *env, advance func() string) int {
			want(t, e.accepted(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "build", Attempt: 1})
			advance()
			want(t, e.advance(t, retried("build", 1, "")), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			return 2
		}},
		{"send-back", func(t *testing.T) []interptest.Expectation {
			return []interptest.Expectation{straightScript(t)[0], buildExp(t, 1, main0, ""), failedBuild(t, 2, "sha256:"+sum([]byte(feedback)))}
		}, func(t *testing.T, e *env, advance func() string) int {
			want(t, e.accepted(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			e.eng.Hold = func() (bool, error) { return true, nil }
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitPaused, Step: "review"})
			e.eng.Hold = nil
			advance()
			want(t, e.advance(t, interp.Event{Kind: interp.StepSentBack, Step: "build", Attempt: 1, Reason: feedback}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			return 2
		}},
		{"first attempt", func(t *testing.T) []interptest.Expectation {
			return []interptest.Expectation{straightScript(t)[0], failedBuild(t, 1, "")}
		}, func(t *testing.T, e *env, advance func() string) int {
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			advance()
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			e.answer(t, "accept-1", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			return 1
		}},
	} {
		for _, b := range backends {
			t.Run(tc.name+"/"+b.name, func(t *testing.T) {
				e, ws, w := newForwardEnv(t, b, tc.exps(t)...)
				var moved string
				n := tc.run(t, e, func() string { moved = ws.AdvanceDefault(w.Remote, "moved.txt"); return moved })
				if got := ws.Head(w.Dir); got != moved {
					t.Errorf("build %d runs at %s, want the new main %s", n, got, moved)
				}
				if got := e.recorded(t, fmt.Sprintf("build-%d-base", n)); got != moved {
					t.Errorf("build-%d-base = %s, want the new main %s", n, got, moved)
				}
				if got := e.branchKept(t); len(got) != 0 {
					t.Errorf("a moved branch sent %v", got)
				}
				if s := behindSection(e.published(t, "build", n)); s != "" {
					t.Errorf("a build on the current main has a behind-main note:\n%s", s)
				}
			})
		}
	}
}

// TestBranchWithCommitsIsKept: a build branch with a commit of its own is never moved and no branch-kept message is
// sent, but the build retry is told it is two commits behind main and to take main's changes with git rebase,
// never git merge. A review of the same branch is told nothing about main.
func TestBranchWithCommitsIsKept(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, ws, w := newForwardEnv(t, b, straightScript(t)[0], failedBuild(t, 1, ""))
			want(t, e.accepted(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			own := ws.Commit(w.Dir)
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "build", Attempt: 1})
			ws.AdvanceDefault(w.Remote, "one.txt")
			ws.AdvanceDefault(w.Remote, "two.txt")
			e.script.Expect(buildExp(t, 2, own, ""), reviewExp(t, "review", "reviewer", 1, own, nil, bound(t, workflow.VerdictApproved, own, nil)))
			want(t, e.advance(t, retried("build", 1, "")), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})

			if got := ws.Head(w.Dir); got != own {
				t.Errorf("build 2 runs at %s, want the branch's own commit %s", got, own)
			}
			if got := e.recorded(t, "build-2-base"); got != main0 {
				t.Errorf("build-2-base = %s, want the merge base %s", got, main0)
			}
			if got := e.branchKept(t); len(got) != 0 {
				t.Errorf("a branch with its own commits sent %v", got)
			}
			note := behindSection(e.published(t, "build", 2))
			if !strings.Contains(note, "2 commits behind") || !strings.Contains(note, "`git fetch`") || !strings.Contains(note, "`git rebase origin/main`") {
				t.Errorf("behind-main note =\n%s\nwant 2 commits behind and git fetch then git rebase origin/main", note)
			}
			if strings.Count(note, "git merge") != strings.Count(note, "never use `git merge`") {
				t.Errorf("behind-main note advises git merge:\n%s", note)
			}
			if strings.Contains(note, "not moved") {
				t.Errorf("a branch with its own commits is said to be kept:\n%s", note)
			}

			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 1})
			if s := behindSection(e.published(t, "review", 1)); s != "" {
				t.Errorf("a review has a behind-main note:\n%s", s)
			}
		})
	}
}

// TestResearchGetsNoBehindNote: a research retry on a branch with commits of its own, behind main, is told nothing
// about main, and the grill, a Director stage, is never fast-forwarded.
func TestResearchGetsNoBehindNote(t *testing.T) {
	research1 := researchExpectation(nil)
	research1.Respond = interptest.Fail("no notes")
	research2 := researchExpectation([]byte("# Research\n"))
	research2.Attempt = 2
	e := newEnvDefinition(t, backends[0], loadWorkflow(t, "director-grill"), research1, research2)
	ws, w := withWorkspaces(t, e)
	ws.SetDefault(w.Remote, main0)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1})
	if got := ws.FastForwards(); len(got) != 0 {
		t.Fatalf("the grill fast-forwarded %+v", got)
	}
	finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
	own := ws.Commit(w.Dir)
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "research", Attempt: 1})
	ws.AdvanceDefault(w.Remote, "one.txt")
	want(t, e.advance(t, retried("research", 1, "")), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 2})
	if got := ws.FastForwards(); len(got) != 2 {
		t.Errorf("fast-forwards = %+v, want one per research attempt", got)
	}
	if got := ws.Head(w.Dir); got != own {
		t.Errorf("research 2 runs at %s, want the branch's own commit %s", got, own)
	}
	if s := behindSection(e.published(t, "research", 2)); s != "" {
		t.Errorf("a research attempt has a behind-main note:\n%s", s)
	}
	if got := e.branchKept(t); len(got) != 0 {
		t.Errorf("a branch with its own commits sent %v", got)
	}
}

// TestRefusedFastForwardSendsBranchKept: an untouched branch git refuses to move, because an untracked file is in
// the way, is kept; the build retry still launches on it, the Director is sent exactly one branch-kept message for
// the attempt and the build's note says the branch was not moved and why. A relaunch of the recorded attempt, after
// a crash before it was marked launched and with a different reason, sends nothing new and keeps the prompt.
func TestRefusedFastForwardSendsBranchKept(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, ws, w := newForwardEnv(t, b, straightScript(t)[0], failedBuild(t, 1, ""), failedBuild(t, 2, ""))
			want(t, e.accepted(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "build", Attempt: 1})
			ws.AdvanceDefault(w.Remote, "moved.txt")
			if err := os.WriteFile(filepath.Join(w.Dir, "moved.txt"), []byte("mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			script := e.eng.Launcher
			e.eng.Launcher = &failingLaunch{Launcher: script}
			if err := advanceErr(t, e, retried("build", 1, "")); err == nil || !strings.Contains(err.Error(), "launcher unavailable") {
				t.Fatalf("first launch = %v, want the launcher's failure", err)
			}

			kept := e.branchKept(t)
			text, ok := kept["app.t7.branch-kept.build-2"]
			if len(kept) != 1 || !ok {
				t.Fatalf("branch-kept messages = %v, want only app.t7.branch-kept.build-2", kept)
			}
			if want := "Step build attempt 2 of app/t7 runs on its branch guildhall/t7, which was not moved to the current main, because "; !strings.HasPrefix(text, want) || !strings.Contains(text, "moved.txt") {
				t.Errorf("branch-kept = %q, want %q and the reason", text, want)
			}
			if m, err := e.mailbox.Read("app.t7.branch-kept.build-2"); err != nil || m.Type != "branch-kept" {
				t.Errorf("message type %q, %v; want branch-kept", m.Type, err)
			}
			prompt := e.published(t, "build", 2)
			note := behindSection(prompt)
			if !strings.Contains(note, "1 commit behind") || !strings.Contains(note, "was not moved to the current main") || !strings.Contains(note, "moved.txt") {
				t.Errorf("behind-main note =\n%s\nwant 1 commit behind, not moved, and why", note)
			}

			// Main moves again and a second file is in the way, so the relaunch's reason differs.
			ws.AdvanceDefault(w.Remote, "again.txt")
			if err := os.WriteFile(filepath.Join(w.Dir, "again.txt"), []byte("mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			if got := ws.Head(w.Dir); got != main0 {
				t.Errorf("the kept branch moved to %s", got)
			}
			if got := e.branchKept(t); len(got) != 1 || got["app.t7.branch-kept.build-2"] != text {
				t.Errorf("branch-kept messages after the relaunch = %v, want the first only", got)
			}
			if got := e.published(t, "build", 2); got != prompt {
				t.Errorf("the relaunch changed the prompt:\n%s\nwant\n%s", got, prompt)
			}
			launched := e.script.Launched()
			if len(launched) == 0 || launched[len(launched)-1].Identity.AttemptID != "build-2" {
				t.Errorf("launches = %+v, want build-2 launched last", launched)
			}
		})
	}
}
