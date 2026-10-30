package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/messaging/messagingtest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// hallmarkDef is the fixture hallmark workflow: one read-only hallmark step, no deliver step.
func hallmarkDef(t *testing.T) *workflow.Definition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(interptest.Testdata(), "workflows", "hallmark.json"))
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Validate(def); err != nil {
		t.Fatal(err)
	}
	return def
}

// hallmarkExp expects attempt n of the hallmark step under the researcher profile with inputs, responding with r.
func hallmarkExp(n int, inputs map[string]string, r interptest.Response) interptest.Expectation {
	return interptest.Expectation{Step: "hallmark", Attempt: n, Profile: "researcher", Inputs: inputs, Standards: []string{"standards/go.md"}, Respond: r}
}

// report is a hallmark response writing the report and, when extra is set, an untracked file besides it.
func report(text string, extra ...interptest.File) interptest.Response {
	return interptest.Succeed("reported", append([]interptest.File{{Path: "report.md", Data: []byte(text)}}, extra...)...)
}

// hallmarkEnv is task app/t7 bound to the hallmark workflow in a real worktree on guildhall/t7, launched.
type hallmarkEnv struct {
	*env
	work, base string
}

func newHallmarkEnv(t *testing.T, b backend, r interptest.Response) *hallmarkEnv {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	e := newEnvDefinition(t, b, hallmarkDef(t), hallmarkExp(1, map[string]string{}, r))
	repository, remote, base := gittest.Repository(t)
	e.eng.Workspaces = git.Program{}
	repo := &e.eng.Config.Repositories[0]
	repo.Path, repo.Remote = repository, remote
	h := &hallmarkEnv{env: e, work: filepath.Join(e.eng.StateDir, "app", "t7", "work"), base: base}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
	return h
}

// git runs git in the worktree and returns its trimmed output.
func (h *hallmarkEnv) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", h.work, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestHallmarkFinishesDone: a succeeded hallmark attempt that leaves the tracked tree and HEAD as prepared, with an
// untracked file besides its report, records the report, closes the task done with a finished message naming the
// report and sends no merged message, opens no decision and records the prepared head as the head the work is safe
// at. Advancing again is refused, as for any closed task.
func TestHallmarkFinishesDone(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newHallmarkEnv(t, b, report("# Hallmark\n", interptest.File{Path: "cache/out.txt", Data: []byte("cached\n")}))
			if got := h.git(t, "rev-parse", "HEAD"); got != h.base {
				t.Fatalf("worktree HEAD = %s, want the base %s", got, h.base)
			}
			want(t, h.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 1}), interp.Next{Wait: interp.WaitDone, Step: "hallmark"})
			if got := h.task(t).State; got != store.TaskDone {
				t.Fatalf("task state = %s, want done", got)
			}
			docs := h.documents(t)
			for _, name := range []string{"hallmark-1-report", "hallmark-1-prepared", "finished"} {
				if _, ok := docs[name]; !ok {
					t.Fatalf("document %s not recorded", name)
				}
			}
			data, err := os.ReadFile(filepath.Join(h.eng.DocumentDir, filepath.FromSlash(docs["finished"].Path)))
			if err != nil {
				t.Fatal(err)
			}
			var finished struct{ Schema, Head string }
			if err := json.Unmarshal(data, &finished); err != nil || finished.Schema != "guildhall.finished/1" || finished.Head != h.base {
				t.Fatalf("finished = %s (%v), want head %s", data, err, h.base)
			}
			msgs := h.messages(t)
			text, ok := msgs["app.t7.finished"]
			if !ok || !strings.Contains(text, "no pull request") || !strings.Contains(text, filepath.FromSlash(docs["hallmark-1-report"].Path)) {
				t.Fatalf("finished message = %q (sent %v), want it to name the report", text, ok)
			}
			for id := range msgs {
				if strings.Contains(id, "merged") || strings.Contains(id, "decision") {
					t.Errorf("unexpected message %s", id)
				}
			}
			if ds, err := h.h.Records.Decisions("app", "t7"); err != nil || len(ds) != 0 {
				t.Fatalf("decisions = %v, %v; want none", ds, err)
			}
			if _, err := h.eng.Advance(context.Background(), interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t7"}); !errors.Is(err, interp.ErrTaskClosed) {
				t.Fatalf("advance of the done task = %v, want ErrTaskClosed", err)
			}
		})
	}
}

// TestHallmarkRefusesChanges: a hallmark result that leaves a tracked file changed, staged or not, or a new commit,
// is refused at intake as the result's fault, naming why: the attempt fails, no report is recorded and the task stays
// open for the Director.
func TestHallmarkRefusesChanges(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(t *testing.T, h *hallmarkEnv)
		reason string
	}{
		{"tracked edit", func(t *testing.T, h *hallmarkEnv) {
			if err := os.WriteFile(filepath.Join(h.work, "a.txt"), []byte("edited\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "hallmarks must not change the project: uncommitted tracked changes"},
		{"staged edit", func(t *testing.T, h *hallmarkEnv) {
			if err := os.WriteFile(filepath.Join(h.work, "b.txt"), []byte("edited\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			h.git(t, "add", "b.txt")
		}, "hallmarks must not change the project: uncommitted tracked changes"},
		{"new commit", func(t *testing.T, h *hallmarkEnv) {
			if err := os.WriteFile(filepath.Join(h.work, "c.txt"), []byte("c\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			h.git(t, "add", "c.txt")
			h.git(t, "commit", "-q", "-m", "c")
		}, "hallmarks must not change the project: HEAD moved from " + "%s"},
	} {
		for _, b := range backends {
			t.Run(c.name+"/"+b.name, func(t *testing.T) {
				h := newHallmarkEnv(t, b, report("# Hallmark\n"))
				c.change(t, h)
				reason := strings.Replace(c.reason, "%s", h.base[:7], 1)
				ev := interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 1}
				assertRejected(t, h.env, ev, reason)
				if _, ok := h.documents(t)["hallmark-1-report"]; ok {
					t.Fatal("a refused report was recorded")
				}
				if got := h.task(t).State; got != store.TaskOpen {
					t.Fatalf("task state = %s, want open", got)
				}
				h.advance(t, ev)
				if got := h.messages(t)["app.t7.attempt-failed.hallmark-1"]; !strings.Contains(got, "Result rejected: ") || !strings.Contains(got, reason) {
					t.Fatalf("attempt-failed = %q, want the rejection reason %q", got, reason)
				}
			})
		}
	}
}

// TestHallmarkPreviousReport: the first hallmark of a repository has no previous input; a later one is given the
// report of the newest done hallmark task, never a done task of another workflow, copied into its own documents. The
// copy stands for its later attempts and replays after another hallmark finishes.
func TestHallmarkPreviousReport(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def, feature := hallmarkDef(t), straight(t)
			first, third := []byte("# First hallmark\n"), []byte("# Third hallmark\n")
			e := newEnvTasks(t, b, def, func(*store.Task) {},
				hallmarkExp(1, map[string]string{}, interptest.Succeed("first", interptest.File{Path: "report.md", Data: first})),
				hallmarkExp(1, map[string]string{"previous": "sha256:" + sum(first)}, interptest.Fail("stopped short")),
				hallmarkExp(1, map[string]string{"previous": "sha256:" + sum(first)}, interptest.Succeed("third", interptest.File{Path: "report.md", Data: third})),
				hallmarkExp(2, map[string]string{"previous": "sha256:" + sum(first)}, report("# Second hallmark\n")),
			)
			e.eng.Workflow = func(rev workflow.Revision) (*workflow.Definition, error) {
				switch rev.Name {
				case def.Name:
					return def, nil
				case feature.Name:
					return feature, nil
				}
				return nil, errors.New("unknown workflow")
			}
			if fake, ok := e.mailbox.(*messagingtest.Fake); ok {
				fake.AddTask("app", "t3")
				fake.AddTask("app", "t8")
			}
			advance := func(task string, ev interp.Event) interp.Next {
				t.Helper()
				ev.Repository, ev.Task = "app", task
				next, err := e.eng.Advance(context.Background(), ev)
				if err != nil {
					t.Fatalf("Advance(%s, %+v) = %v", task, ev, err)
				}
				return next
			}
			// A done feature task, which has a document a hallmark's report would be named as, is never chosen.
			if _, err := e.h.CreateTask("task:app:t9", store.Task{Repository: "app", ID: "t9", Title: "Feature", Workflow: feature.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.h.Records.PutDocument("doc:t9", store.DocumentRef{Repository: "app", TaskID: "t9", Name: "hallmark-1-report", Path: "tasks/app/t9/hallmark-1/report.md", SHA256: sum([]byte("x"))}); err != nil {
				t.Fatal(err)
			}

			// t3 is the repository's first hallmark: no previous input.
			want(t, advance("t3", interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			want(t, advance("t3", interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 1}), interp.Next{Wait: interp.WaitDone, Step: "hallmark"})
			if _, err := e.h.Records.SetTaskState("state:t9", "app", "t9", store.TaskDone); err != nil {
				t.Fatal(err)
			}

			// The shared selector, which the Director's cadence reads too, names the hallmark whose report is copied.
			last := func() string {
				t.Helper()
				tasks, err := e.h.Records.Tasks("app")
				if err != nil {
					t.Fatal(err)
				}
				closes, err := e.h.Records.TaskCloses()
				if err != nil {
					t.Fatal(err)
				}
				h, err := interp.LastHallmark(tasks, closes, e.h.Records.Documents, e.eng.Workflow, "")
				if err != nil || h == nil {
					t.Fatalf("LastHallmark = %+v, %v", h, err)
				}
				return h.Task.ID
			}
			if got := last(); got != "t3" {
				t.Fatalf("last hallmark = %s, want t3", got)
			}

			// t7 is given t3's report, copied as its own previous-hallmark document.
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			prev, ok := e.documents(t)["previous-hallmark"]
			if !ok || prev.SHA256 != sum(first) || prev.Path != "tasks/app/t7/previous-hallmark-t3.md" {
				t.Fatalf("previous-hallmark = %+v (%v), want t3's report", prev, ok)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "hallmark", Attempt: 1})

			// t8, a third hallmark, finishes while t7 waits; t8 is given t3's report too, since t7 is not done.
			if _, err := e.h.CreateTask("task:app:t8", store.Task{Repository: "app", ID: "t8", Title: "Third", Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
				t.Fatal(err)
			}
			want(t, advance("t8", interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			want(t, advance("t8", interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 1}), interp.Next{Wait: interp.WaitDone, Step: "hallmark"})
			if got := last(); got != "t8" {
				t.Fatalf("last hallmark after t8 = %s, want t8", got)
			}

			// t7's retry keeps the report it was first given, not t8's.
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptRetried, Step: "hallmark", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 2}), interp.Next{Wait: interp.WaitDone, Step: "hallmark"})
		})
	}
}
