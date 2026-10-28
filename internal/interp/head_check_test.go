package interp_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// headEnv is the straight path over a real repository and worktree custody, git.Program, up to a launched build.
type headEnv struct {
	*env
	work, remote, base string
}

// newHeadEnv launches build 1 of task t7 in a real worktree on guildhall/t7, whose result is build: an omitted
// head when it writes no files, an explicit one when it writes head.
func newHeadEnv(t *testing.T, b backend, build interptest.Response) *headEnv {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	exps := straightScript(t)[:2]
	exps[1].Respond = build
	e := newEnv(t, b, exps...)
	repository, remote, base := gittest.Repository(t)
	e.eng.Workspaces = git.Program{}
	repo := &e.eng.Config.Repositories[0]
	repo.Path, repo.Remote = repository, remote
	h := &headEnv{env: e, work: filepath.Join(e.eng.StateDir, "app", "t7", "work"), remote: remote, base: base}
	want(t, e.accepted(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
	return h
}

// git runs git in dir and returns its trimmed output.
func (h *headEnv) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit commits a new file in the worktree and returns the commit.
func (h *headEnv) commit(t *testing.T, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.work, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.git(t, h.work, "add", name)
	h.git(t, h.work, "commit", "-q", "-m", name)
	return h.git(t, h.work, "rev-parse", "HEAD")
}

// writeHead writes the explicit head file the build's result lists.
func (h *headEnv) writeHead(t *testing.T, commit string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.work, "head"), headFile(commit), 0o644); err != nil {
		t.Fatal(err)
	}
}

// moveRemote commits to the remote's default branch from a clone of its own and returns the new commit.
func (h *headEnv) moveRemote(t *testing.T) string {
	t.Helper()
	clone := t.TempDir()
	h.git(t, clone, "clone", "-q", h.remote, ".")
	if err := os.WriteFile(filepath.Join(clone, "upstream.txt"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.git(t, clone, "add", "upstream.txt")
	h.git(t, clone, "commit", "-q", "-m", "upstream")
	h.git(t, clone, "push", "-q", "origin", "HEAD:trunk")
	return h.git(t, clone, "rev-parse", "HEAD")
}

// document returns the bytes of the recorded document name, trimmed, or fails t.
func (h *headEnv) document(t *testing.T, name string) string {
	t.Helper()
	d, ok := h.documents(t)[name]
	if !ok {
		t.Fatalf("document %s not recorded", name)
	}
	data, err := os.ReadFile(filepath.Join(h.eng.DocumentDir, filepath.FromSlash(d.Path)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// accepts advances the build's result and checks it recorded head as build-n-head and launched review n.
func (h *headEnv) accepts(t *testing.T, n int, commit string) {
	t.Helper()
	h.script.Expect(reviewExp(t, "review", "reviewer", n, commit, nil, bound(t, workflow.VerdictApproved, commit, nil)))
	want(t, h.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: n}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: n})
	name := "build-" + strconv.Itoa(n) + "-head"
	if got := h.document(t, name); got != commit {
		t.Fatalf("%s = %s, want %s", name, got, commit)
	}
}

// rejects advances the build's result and checks it was refused with reason, recorded no head and told the
// Director the reason in attempt-failed.
func (h *headEnv) rejects(t *testing.T, reason string) {
	t.Helper()
	ev := interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}
	assertRejected(t, h.env, ev, reason)
	if _, ok := h.documents(t)["build-1-head"]; ok {
		t.Fatal("a refused head was recorded")
	}
	h.advance(t, ev)
	if got := h.messages(t)["app.t7.attempt-failed.build-1"]; !strings.Contains(got, "Result rejected: ") || !strings.Contains(got, reason) {
		t.Fatalf("attempt-failed = %q, want the rejection reason %q", got, reason)
	}
}

func explicitBuild() interptest.Response {
	return interptest.Succeed("built", interptest.File{Path: "head", Data: headFile(head)})
}

// TestHeadCheckAccepts: a head that is the task branch's tip and descends from the attempt's base is accepted, on
// both the omitted and explicit paths, and the base is the default branch the workspace started at.
func TestHeadCheckAccepts(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name+"/omitted", func(t *testing.T) {
			h := newHeadEnv(t, b, interptest.Succeed("built"))
			if got := h.document(t, "build-1-base"); got != h.base {
				t.Fatalf("build-1-base = %s, want %s", got, h.base)
			}
			h.accepts(t, 1, h.commit(t, "code.go"))
		})
		t.Run(b.name+"/explicit", func(t *testing.T) {
			h := newHeadEnv(t, b, explicitBuild())
			commit := h.commit(t, "code.go")
			h.writeHead(t, commit)
			h.accepts(t, 1, commit)
		})
	}
}

// TestHeadCheckRejects: a head that is not the task branch's tip, that the repository lacks or that does not
// descend from the attempt's base is refused at intake with a reason the Director is told.
func TestHeadCheckRejects(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name+"/another branch", func(t *testing.T) {
			h := newHeadEnv(t, b, interptest.Succeed("built"))
			h.git(t, h.work, "checkout", "-q", "-b", "experiment")
			commit := h.commit(t, "code.go")
			h.rejects(t, "head "+commit[:7]+" is not the tip of guildhall/t7 (tip "+h.base[:7]+")")
		})
		t.Run(b.name+"/detached behind the tip", func(t *testing.T) {
			h := newHeadEnv(t, b, interptest.Succeed("built"))
			tip := h.commit(t, "code.go")
			h.git(t, h.work, "checkout", "-q", "--detach", h.base)
			h.rejects(t, "head "+h.base[:7]+" is not the tip of guildhall/t7 (tip "+tip[:7]+")")
		})
		t.Run(b.name+"/explicit head the repository lacks", func(t *testing.T) {
			h := newHeadEnv(t, b, explicitBuild())
			h.commit(t, "code.go")
			h.rejects(t, "head "+head[:7]+" is not the tip of guildhall/t7")
		})
		t.Run(b.name+"/unrelated history", func(t *testing.T) {
			h := newHeadEnv(t, b, interptest.Succeed("built"))
			h.git(t, h.work, "checkout", "-q", "--orphan", "scratch")
			h.git(t, h.work, "rm", "-rq", "--cached", ".")
			commit := h.commit(t, "unrelated.go")
			h.git(t, h.work, "checkout", "-q", "-B", "guildhall/t7")
			h.rejects(t, "head "+commit[:7]+" does not descend from the attempt's base "+h.base[:7])
		})
	}
}

// TestHeadCheckSendBackRebase: a send-back attempt that rebases the task branch onto a default branch that moved
// is checked against the base it recorded at its own launch, and accepted.
func TestHeadCheckSendBackRebase(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newHeadEnv(t, b, interptest.Succeed("built"))
			first := h.commit(t, "code.go")
			h.accepts(t, 1, first)
			want(t, h.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "sign-off", Decision: "sign-off-1"})
			moved := h.moveRemote(t)
			feedback := "Rebase onto the default branch."
			exp := buildExp(t, 2, head, "sha256:"+sum([]byte(feedback)))
			exp.Respond = interptest.Succeed("rebased")
			h.script.Expect(exp)
			want(t, h.advance(t, interp.Event{Kind: interp.StepSentBack, Step: "build", Attempt: 1, Reason: feedback}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			if got := h.document(t, "build-2-base"); got != h.base {
				t.Fatalf("build-2-base = %s, want the merge base %s", got, h.base)
			}
			h.git(t, h.work, "rebase", "-q", git.DefaultRef("guildhall/t7"))
			rebased := h.git(t, h.work, "rev-parse", "HEAD")
			if h.git(t, h.work, "rev-parse", "HEAD~1") != moved {
				t.Fatalf("the rebase did not land on %s", moved)
			}
			previous := bound(t, workflow.VerdictApproved, first, nil)
			h.script.Expect(reviewExp(t, "review", "reviewer", 2, rebased, &previous, bound(t, workflow.VerdictApproved, rebased, nil)))
			want(t, h.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 2})
			if got := h.document(t, "build-2-head"); got != rebased {
				t.Fatalf("build-2-head = %s, want the rebased %s", got, rebased)
			}
		})
	}
}

// TestHeadCheckReplayKeepsTheRecordedHead: once a head is recorded, intake replayed after the branch moved on does
// not refuse it, on both paths.
func TestHeadCheckReplayKeepsTheRecordedHead(t *testing.T) {
	for _, b := range backends {
		for _, explicit := range []bool{false, true} {
			name := b.name + "/omitted"
			build := interptest.Succeed("built")
			if explicit {
				name, build = b.name+"/explicit", explicitBuild()
			}
			t.Run(name, func(t *testing.T) {
				h := newHeadEnv(t, b, build)
				commit := h.commit(t, "code.go")
				h.writeHead(t, commit)
				h.accepts(t, 1, commit)
				if err := os.Remove(filepath.Join(h.eng.StateDir, "app", "t7", "build-1", "intake.json")); err != nil {
					t.Fatal(err)
				}
				h.commit(t, "later.go")
				h.advance(t, interp.Event{Kind: interp.TaskCreated})
				for _, a := range h.attempts(t) {
					if a.Step == "build" && (a.Outcome != store.AttemptSucceeded || a.IntakeRejection != "") {
						t.Fatalf("replayed build = %+v, want it to stand", a)
					}
				}
				if got := h.document(t, "build-1-head"); got != commit {
					t.Fatalf("build-1-head = %s after replay, want %s", got, commit)
				}
			})
		}
	}
}

// failingLaunch is a Launcher whose first launch fails, after the engine prepared the attempt.
type failingLaunch struct {
	interp.Launcher
	failed bool
}

func (f *failingLaunch) Launch(ctx context.Context, spec interp.Spec) (interp.Handle, error) {
	if !f.failed {
		f.failed = true
		return interp.Handle{}, errors.New("launcher unavailable")
	}
	return f.Launcher.Launch(ctx, spec)
}

// TestHeadCheckBaseRecordedOnce: an attempt recorded but never launched keeps the base its first launch recorded
// when it is launched again after the default branch moved and its merge base with the branch moved too.
func TestHeadCheckBaseRecordedOnce(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			global := filepath.Join(t.TempDir(), "gitconfig")
			if err := os.WriteFile(global, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", global)
			e := newEnv(t, b, straightScript(t)[:2]...)
			repository, remote, base := gittest.Repository(t)
			e.eng.Workspaces = git.Program{}
			repo := &e.eng.Config.Repositories[0]
			repo.Path, repo.Remote = repository, remote
			h := &headEnv{env: e, work: filepath.Join(e.eng.StateDir, "app", "t7", "work"), remote: remote, base: base}
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			e.answer(t, "accept-1", "approved")
			e.eng.Launcher = &failingLaunch{Launcher: e.script}
			if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}); err == nil || !strings.Contains(err.Error(), "launcher unavailable") {
				t.Fatalf("first launch = %v, want the launcher's failure", err)
			}
			if got := h.document(t, "build-1-base"); got != base {
				t.Fatalf("build-1-base = %s, want %s", got, base)
			}
			moved := h.moveRemote(t)
			h.git(t, h.work, "fetch", "-q", remote, "trunk")
			h.git(t, h.work, "merge", "-q", "--ff-only", "FETCH_HEAD")
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			if got, err := (git.Program{}).Base(context.Background(), repository, "guildhall/t7"); err != nil || got != moved {
				t.Fatalf("Base now = %s, %v; want %s, so a base read again would differ", got, err, moved)
			}
			if got := h.document(t, "build-1-base"); got != base {
				t.Fatalf("build-1-base after the relaunch = %s, want the first %s", got, base)
			}
		})
	}
}

// failingGit is git.Program with Tip or IsAncestor failing as git can.
type failingGit struct {
	git.Program
	tip, ancestry bool
}

func (f failingGit) Tip(ctx context.Context, repository, branch string) (string, error) {
	if f.tip {
		return "", errors.New("git show-ref: exit status 128: fatal: unable to read refs")
	}
	return f.Program.Tip(ctx, repository, branch)
}

func (f failingGit) IsAncestor(ctx context.Context, repository, commit, head string) (bool, error) {
	if f.ancestry {
		return false, errors.New("git merge-base: exit status 128: fatal: corrupt object")
	}
	return f.Program.IsAncestor(ctx, repository, commit, head)
}

// TestHeadCheckGitFailureReplays: a git failure reading the tip or the ancestry is an operational error, not a
// rejection, and the next advance takes the result.
func TestHeadCheckGitFailureReplays(t *testing.T) {
	for _, b := range backends {
		for _, fault := range []failingGit{{tip: true}, {ancestry: true}} {
			name := b.name + "/tip"
			if fault.ancestry {
				name = b.name + "/ancestry"
			}
			t.Run(name, func(t *testing.T) {
				h := newHeadEnv(t, b, interptest.Succeed("built"))
				commit := h.commit(t, "code.go")
				h.eng.Workspaces = fault
				if err := advanceErr(t, h.env, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}); err == nil {
					t.Fatal("intake with git failing succeeded")
				}
				for _, a := range h.attempts(t) {
					if a.Step == "build" && (a.Outcome == store.AttemptFailed || a.IntakeRejection != "") {
						t.Fatalf("operational error classified as rejection: %+v", a)
					}
				}
				if _, ok := h.documents(t)["build-1-head"]; ok {
					t.Fatal("head recorded although the check failed")
				}
				h.eng.Workspaces = git.Program{}
				h.accepts(t, 1, commit)
			})
		}
	}
}
