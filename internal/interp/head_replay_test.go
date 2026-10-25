package interp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestBuildHeadReplay recreates a launcher-finished build whose empty result has
// never completed intake. Git is real; the Artisan and model are scripted.
func TestBuildHeadReplay(t *testing.T) {
	for _, b := range backends {
		for _, finished := range []bool{false, true} {
			for _, dirty := range []bool{false, true} {
				name := b.name + "/running"
				if finished {
					name = b.name + "/finished"
				}
				if dirty {
					name += "/dirty"
				}
				t.Run(name, func(t *testing.T) {
					t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
					t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "empty-gitconfig"))
					exps := straightScript(t)
					exps[1].Respond = interptest.Succeed("committed")
					e := newEnv(t, b)
					work := filepath.Join(e.eng.StateDir, "app", "t7", "work")
					if err := os.MkdirAll(work, 0o755); err != nil {
						t.Fatal(err)
					}
					git := func(args ...string) string {
						t.Helper()
						cmd := exec.Command("git", append([]string{"-C", work, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
						out, err := cmd.CombinedOutput()
						if err != nil {
							t.Fatalf("git %v: %v: %s", args, err, out)
						}
						return strings.TrimSpace(string(out))
					}
					git("init")
					if err := os.WriteFile(filepath.Join(work, "code.go"), []byte("package app\n"), 0o644); err != nil {
						t.Fatal(err)
					}
					git("add", "code.go")
					git("commit", "-m", "build")
					commit := git("rev-parse", "HEAD")
					exps[2].Inputs["head"] = "sha256:" + sum([]byte(commit+"\n"))
					exps[2].Respond = interptest.Review(bound(t, workflow.VerdictApproved, commit, nil))
					e.script = interptest.NewScript(t, exps...)
					e.eng.Launcher = e.script
					e.advance(t, interp.Event{Kind: interp.TaskCreated})
					next := e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
					e.answer(t, next.Decision, "approved")
					e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: next.Decision})
					if finished {
						if _, err := e.h.Records.FinishAttempt("launcher:build", "app", "t7", "build", 1, store.AttemptSucceeded); err != nil {
							t.Fatal(err)
						}
					}
					intake := filepath.Join(e.eng.StateDir, "app", "t7", "build-1", "intake.json")
					if _, err := os.Stat(intake); !os.IsNotExist(err) {
						t.Fatalf("expected absent intake: %v", err)
					}
					// Dirty tracked work must not silently disappear, regardless of staging.
					event := interp.Event{Repository: "app", Task: "t7", Kind: interp.TaskCreated}
					if !finished {
						event.Kind, event.Step, event.Attempt = interp.AttemptResult, "build", 1
					}
					if dirty {
						for _, staged := range []bool{false, true} {
							if err := os.WriteFile(filepath.Join(work, "code.go"), []byte("changed\n"), 0o644); err != nil {
								t.Fatal(err)
							}
							if staged {
								git("add", "code.go")
							}
							_, err := e.eng.Advance(context.Background(), event)
							if err == nil || !strings.Contains(err.Error(), "code.go") {
								t.Fatalf("dirty intake = %v, want error naming code.go", err)
							}
							if _, ok := e.documents(t)["build-1-head"]; ok {
								t.Fatal("dirty head recorded")
							}
							git("restore", "--staged", "--worktree", "code.go")
						}
					}
					// The reported task has untracked notes/spec; these must not block.
					for _, name := range []string{"notes.md", "research-notes.md", "spec.md"} {
						if err := os.WriteFile(filepath.Join(work, name), []byte("notes\n"), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					next = e.advance(t, event)
					if next.Step != "review" || next.Wait != interp.WaitAttempt {
						t.Fatalf("next = %+v, want review", next)
					}
					d := e.documents(t)["build-1-head"]
					data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(d.Path)))
					if err != nil || string(data) != commit+"\n" {
						t.Fatalf("head = %q, %v; want %s", data, err, commit)
					}
					if _, err := os.Stat(intake); err != nil {
						t.Fatal(err)
					}
					git("commit", "--allow-empty", "-m", "later")
					// Missing intake after document persistence must reuse the recorded head.
					if err := os.Remove(intake); err != nil {
						t.Fatal(err)
					}
					e.advance(t, interp.Event{Kind: interp.TaskCreated})
					if got := e.documents(t)["build-1-head"]; got != d {
						t.Fatalf("head changed on replay: %+v", got)
					}
				})
			}
		}
	}
}
