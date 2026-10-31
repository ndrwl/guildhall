package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
// Every hallmark attempt is given a baseline: inputs without one expect it at any digest.
func hallmarkExp(n int, inputs map[string]string, r interptest.Response) interptest.Expectation {
	if _, ok := inputs["baseline"]; !ok {
		inputs["baseline"] = interptest.AnyDigest
	}
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

// rankedReport is a hallmark report in the template's shape, with two ranked fixes.
const rankedReport = "# Hallmark\n\n## Ranked fixes\n\n### H1. Tighten the parser\n\nWhy.\n\n### H2. Drop the stale cache\n\nWhy.\n"

// waitsOnFollowUps is what a finished hallmark waits on while its follow-ups decision is open.
var waitsOnFollowUps = interp.Next{Wait: interp.WaitDecision, Step: "hallmark", Decision: interp.FollowUpsDecision}

// TestHallmarkFinishesDone: a succeeded hallmark attempt that leaves the tracked tree and HEAD as prepared, with an
// untracked file besides its report, records the report, opens the follow-ups decision bound to the report and naming
// its ranked fixes, and sends a finished message naming the report and the decision, then waits on the decision with
// the task still open; it sends no merged message and records the prepared head as the head the work is safe at. An
// advance before the answer still waits. Answering the decision and advancing with it closes the task done, sending
// nothing new; a close that fails replays to the same messages, and advancing the done task is refused, as for any
// closed task.
func TestHallmarkFinishesDone(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newHallmarkEnv(t, b, report(rankedReport, interptest.File{Path: "cache/out.txt", Data: []byte("cached\n")}))
			if got := h.git(t, "rev-parse", "HEAD"); got != h.base {
				t.Fatalf("worktree HEAD = %s, want the base %s", got, h.base)
			}
			ev := interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 1}
			want(t, h.advance(t, ev), waitsOnFollowUps)
			if got := h.task(t).State; got != store.TaskOpen {
				t.Fatalf("task state after the report = %s, want open until follow-ups is answered", got)
			}
			sent := h.inbox(t)
			want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), waitsOnFollowUps)
			if got := h.inbox(t); !slices.Equal(got, sent) {
				t.Errorf("inbox after a second advance = %q, want %q", got, sent)
			}
			if got := h.task(t).State; got != store.TaskOpen {
				t.Fatalf("task state = %s, want open", got)
			}
			// The open task's messages and decision, before the answer.
			if text := h.messages(t)["app.t7.finished"]; !strings.Contains(text, "stays open until it is answered") ||
				!strings.Contains(text, "guildhall task start --decision follow-ups --repo app t7") || strings.Contains(text, "the task is done") {
				t.Errorf("finished message = %q, want it to say the task stays open until follow-ups is answered", text)
			}
			if d := h.decision(t, "follow-ups"); !d.Open() {
				t.Fatalf("follow-ups = %+v, want it open", d)
			}
			h.answer(t, "follow-ups", "pick\nH1")
			answered := interp.Event{Kind: interp.DecisionAnswered, Decision: interp.FollowUpsDecision}
			records := h.eng.Records
			h.eng.Records = &failClose{Records: records}
			if err := advanceErr(t, h.env, answered); !errors.Is(err, errInjected) {
				t.Fatalf("advance = %v, want the injected failure", err)
			}
			want(t, h.advance(t, answered), interp.Next{Wait: interp.WaitDone, Step: "hallmark"})
			h.eng.Records = records
			if got := h.inbox(t); !slices.Equal(got, sent) {
				t.Errorf("inbox after the replayed close = %q, want %q", got, sent)
			}
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
			if !ok || !strings.Contains(text, "no pull request") || !strings.Contains(text, filepath.FromSlash(docs["hallmark-1-report"].Path)) ||
				!strings.Contains(text, "Decision follow-ups is open") {
				t.Fatalf("finished message = %q (sent %v), want it to name the report and the decision", text, ok)
			}
			if _, ok := msgs["app.t7.decision.follow-ups"]; !ok {
				t.Errorf("messages = %v, want decision.follow-ups", msgs)
			}
			for id := range msgs {
				if strings.Contains(id, "merged") {
					t.Errorf("unexpected message %s", id)
				}
			}
			ds, err := h.h.Records.Decisions("app", "t7")
			if err != nil || len(ds) != 1 {
				t.Fatalf("decisions = %v, %v; want the follow-ups decision only", ds, err)
			}
			d := ds[0]
			path := filepath.Join(h.eng.DocumentDir, filepath.FromSlash(docs["hallmark-1-report"].Path))
			if d.ID != "follow-ups" || d.Kind != store.DecisionFollowUps || !d.Answered || d.Choice != "pick" || d.Void ||
				d.Subject == nil || *d.Subject != (store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + docs["hallmark-1-report"].SHA256}) ||
				!slices.Equal(d.Options, []string{"pick", "none"}) {
				t.Fatalf("decision = %+v, want the answered follow-ups decision on the report offering pick and none", d)
			}
			if !strings.Contains(d.Question, "Report: "+path+"\n") || !strings.Contains(d.Question, "\nH1. Tighten the parser\nH2. Drop the stale cache\n") {
				t.Errorf("question = %q, want the report path and its ranked fixes in order", d.Question)
			}
			if _, err := h.eng.Advance(context.Background(), interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t7"}); !errors.Is(err, interp.ErrTaskClosed) {
				t.Fatalf("advance of the done task = %v, want ErrTaskClosed", err)
			}
		})
	}
}

// followUpsOf runs the hallmark of report text to its wait on the follow-ups decision and returns the decision.
func followUpsOf(t *testing.T, b backend, text string) (*hallmarkEnv, store.Decision) {
	t.Helper()
	h := newHallmarkEnv(t, b, report(text))
	want(t, h.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 1}), waitsOnFollowUps)
	if got := h.task(t).State; got != store.TaskOpen {
		t.Fatalf("task state = %s, want open", got)
	}
	return h, h.decision(t, "follow-ups")
}

// finishHallmark answers the hallmark's follow-ups decision with answer and advances with it, which closes the task.
func finishHallmark(t *testing.T, e *env, task, answer string) {
	t.Helper()
	if _, err := e.h.AnswerDecision("answer:"+task+":follow-ups", "app", task, interp.FollowUpsDecision, answer); err != nil {
		t.Fatal(err)
	}
	next, err := e.eng.Advance(context.Background(), interp.Event{Kind: interp.DecisionAnswered, Repository: "app", Task: task, Decision: interp.FollowUpsDecision})
	if err != nil {
		t.Fatalf("Advance(%s, follow-ups answered) = %v", task, err)
	}
	want(t, next, interp.Next{Wait: interp.WaitDone, Step: "hallmark"})
}

// TestHallmarkFollowUpsDeferred: a deferred follow-ups decision is still open, so the hallmark keeps waiting on it
// with its task open; answering it then closes the task.
func TestHallmarkFollowUpsDeferred(t *testing.T) {
	h, _ := followUpsOf(t, backends[1], rankedReport)
	s := h.h.Records.(*store.Store)
	if _, err := h.h.CreateTask("task:app:t9", store.Task{Repository: "app", ID: "t9", Title: "Release", Workflow: h.task(t).Workflow, ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeferDecision("defer:follow-ups", "app", "t7", interp.FollowUpsDecision, "After the release.", []store.TaskRef{{Repository: "app", ID: "t9"}}, 0); err != nil {
		t.Fatal(err)
	}
	want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), waitsOnFollowUps)
	if got := h.task(t).State; got != store.TaskOpen {
		t.Fatalf("task state = %s, want open while follow-ups is deferred", got)
	}
	finishHallmark(t, h.env, "t7", "none")
	if got := h.task(t).State; got != store.TaskDone {
		t.Fatalf("task state = %s, want done", got)
	}
}

// TestHallmarkFollowUpsWithoutHeadings: a report with no "### H<n>." headings still opens a follow-ups decision,
// whose question names only the report's path.
func TestHallmarkFollowUpsWithoutHeadings(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h, d := followUpsOf(t, b, "# Hallmark\n\nNothing ranked.\n### H. no number\n### Hx. not an id\n")
			path := filepath.Join(h.eng.DocumentDir, filepath.FromSlash(h.documents(t)["hallmark-1-report"].Path))
			if !strings.Contains(d.Question, "Report: "+path+"\n") || strings.Contains(d.Question, "Ranked follow-ups") {
				t.Errorf("question = %q, want the report path and no list", d.Question)
			}
		})
	}
}

// TestHallmarkFollowUpsCut: a report whose ranked fixes would not fit the question has the list cut, ending with a
// pointer to the report, so the decision still opens.
func TestHallmarkFollowUpsCut(t *testing.T) {
	var text strings.Builder
	text.WriteString("# Hallmark\n\n")
	for i := 1; text.Len() <= 2*store.MaxTextBytes; i++ {
		fmt.Fprintf(&text, "### H%d. %s\n", i, strings.Repeat("fix ", 20))
	}
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			_, d := followUpsOf(t, b, text.String())
			if len(d.Question) > store.MaxTextBytes || !strings.Contains(d.Question, "\nH1. fix") || !strings.Contains(d.Question, "\n… see the report\n") ||
				!strings.HasSuffix(d.Question, "the Director creates one for each id picked.\n") {
				t.Errorf("question of %d bytes = %.200q…, want a cut list within %d bytes", len(d.Question), d.Question, store.MaxTextBytes)
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
				if ds, err := h.h.Records.Decisions("app", "t7"); err != nil || len(ds) != 0 {
					t.Fatalf("decisions = %v, %v; want none", ds, err)
				}
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
			want(t, advance("t3", interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 1}), waitsOnFollowUps)
			finishHallmark(t, e, "t3", "none")
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
			want(t, advance("t8", interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 1}), waitsOnFollowUps)
			if got := last(); got != "t3" {
				t.Fatalf("last hallmark while t8 waits on its follow-ups = %s, want t3", got)
			}
			finishHallmark(t, e, "t8", "pick\nH1")
			if got := last(); got != "t8" {
				t.Fatalf("last hallmark after t8 = %s, want t8", got)
			}

			// t7's retry keeps the report it was first given, not t8's.
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptRetried, Step: "hallmark", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 2}), waitsOnFollowUps)
		})
	}
}
