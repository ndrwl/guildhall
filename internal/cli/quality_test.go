package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter/codex"
	"github.com/ndrwl/guildhall/internal/adapter/codex/codextest"
	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// judgedSelfReview is self-review 1 of app/t1 with D1 a pending score; its brief tells the fake judge what to write.
const judgedSelfReview = `{"schema": "guildhall.self-review/1", "repository": "app", "task": "t1", "number": 1,
 "head": "9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b",
 "criteria": [{"id": "D1", "kind": "score", "judge": "pending", "score": null, "evidence": "nontest_lines: 140"}]}`

const judgedBrief = "Score D1 against docs/quality.md.\n" +
	`guildhall-fake: output=judgement.json:{"merge_as_is": "no", "criteria": [{"id": "D1", "score": 2, "evidence": "140 non-test lines"}]}` + "\n"

// fakeJudgeProfile is the one judge profile shortCopy configures.
const fakeJudgeProfile = "[profile.judge-fake]\nadapter = \"fake\"\n"

// shortCopy is shortCopyWith judged by judge-fake alone.
func shortCopy(t *testing.T) string {
	t.Helper()
	return shortCopyWith(t, "judge-fake", fakeJudgeProfile)
}

// shortCopyWith is newCopy, with quality_judge_profiles judges and the profile tables profiles, in a short temporary
// directory, so the attempts' endpoint paths fit the AF_UNIX limit. Off Windows it is under /tmp: macOS's per-user
// TMPDIR leaves the judge's endpoint, under the copy's state, too long.
func shortCopyWith(t *testing.T, judges, profiles string) string {
	t.Helper()
	root := ""
	if runtime.GOOS != "windows" {
		root = "/tmp"
	}
	dir, err := os.MkdirTemp(root, "gq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.CopyFS(dir, os.DirFS(filepath.Dir(writeDirector(t, strings.Replace(directorTOML, "[local]\n", "[local]\nquality_judge_profiles = \""+judges+"\"\n", 1)+"\n"+profiles)))); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	app, _, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"store", "init", filepath.Join(dir, "director.store")}); code != 0 {
		t.Fatalf("store init: exit %d, stderr %s", code, stderr)
	}
	return dir
}

// judgeCopy is a Director over the copy dir.
func judgeCopy(t *testing.T, dir string) runDirector {
	t.Helper()
	d := runDirector{store: filepath.Join(dir, "director.store"), config: filepath.Join(dir, "config.toml"), state: filepath.Join(dir, "state")}
	t.Cleanup(func() { d.stopAll(t) })
	return d
}

// deliver files the next task in app, which must be assigned the id task, and records it done with self-review 1,
// judgedSelfReview for that task, and its brief.
func deliver(t *testing.T, d runDirector, dir, task, brief string) {
	t.Helper()
	if got := newTask(t, d.store, d.config, "app", "Sort the task list "+task).TaskID; got != task {
		t.Fatalf("task new assigned %q, want %q", got, task)
	}
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.SetTaskState("deliver-"+task, "app", task, store.TaskDone)
	}); err != nil {
		t.Fatal(err)
	}
	reviews := filepath.Join(dir, "quality", "app", task)
	if err := os.MkdirAll(reviews, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"self-review-1.json": strings.Replace(judgedSelfReview, `"t1"`, `"`+task+`"`, 1), "self-review-1-judge.md": brief} {
		if err := os.WriteFile(filepath.Join(reviews, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// judgeLog is the launcher log of judge attempt n of app/<task>.
func judgeLog(d runDirector, task string, n int) []byte {
	log, _ := os.ReadFile(filepath.Join(d.judgeDir(task, n), "launcher.log"))
	return log
}

func (d runDirector) judgeDir(task string, n int) string {
	return filepath.Join(d.state, "app", task, workflow.JudgeStep+"-"+strconv.Itoa(n))
}

// startJudge runs quality judge for self-review 1 of app/<task> and returns what it started.
func startJudge(t *testing.T, d runDirector, dir, task string, attempt int) quality.Started {
	t.Helper()
	code, out, stderr := d.run(t, "quality", "judge", "--repo", "app", "--review", "1", "--copy", dir, task)
	if code != 0 {
		t.Fatalf("quality judge: exit %d, stdout %q, stderr %q; launcher log:\n%s", code, out, stderr, judgeLog(d, task, attempt))
	}
	return decodeJSON[quality.Started](t, out)
}

// awaitJudgement waits for judge attempt n of app/<task> to record its judgement and tell the Director verdict.
func awaitJudgement(t *testing.T, d runDirector, task string, n int, verdict string) {
	t.Helper()
	awaitJudge(t, d, task, n, func(docs []store.DocumentRef, msgs []store.Message, _ []store.StageAttempt) bool {
		return slices.ContainsFunc(docs, func(doc store.DocumentRef) bool {
			return doc.Name == workflow.JudgeStep+"-"+strconv.Itoa(n)+"-judgement"
		}) &&
			slices.ContainsFunc(msgs, func(m store.Message) bool {
				return m.Repository == "app" && m.TaskID == task && m.Type == "judgement" && strings.Contains(m.Text, "would merge it as is: "+verdict)
			})
	})
}

// awaitJudge waits until done holds for app/<task>'s documents, the Director's messages and the task's attempts.
func awaitJudge(t *testing.T, d runDirector, task string, n int, done func([]store.DocumentRef, []store.Message, []store.StageAttempt) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(2*time.Minute))
	defer cancel()
	for {
		docs, err := withStore(d.store, true, func(s *store.Store) ([]store.DocumentRef, error) { return s.Documents("app", task) })
		if err != nil {
			t.Fatal(err)
		}
		msgs, err := withStore(d.store, true, func(s *store.Store) ([]store.Message, error) {
			m, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
			return m, err
		})
		if err != nil {
			t.Fatal(err)
		}
		attempts, err := withStore(d.store, true, func(s *store.Store) ([]store.StageAttempt, error) { return s.Attempts("app", task) })
		if err != nil {
			t.Fatal(err)
		}
		if done(docs, msgs, attempts) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("judge attempt %d of %s not observed done; documents %+v; Director messages %+v; attempts %+v; launcher log:\n%s", n, task, docs, msgs, attempts, judgeLog(d, task, n))
		case <-time.After(50 * time.Millisecond): // timing:allow condition polling bounded by the overall context.
		}
	}
}

// TestQualityJudgeEndToEnd: over a copy, quality judge launches the judge of a delivered task's self-review through
// the owned launcher with the fake adapter, whose advance, quality judged, records the judgement and tells the
// Director; quality agreement then counts it as judged and not audited; the task stays done.
func TestQualityJudgeEndToEnd(t *testing.T) {
	t.Setenv(roleEnv, "guildhall")
	dir := shortCopy(t)
	d := judgeCopy(t, dir)
	deliver(t, d, dir, "t1", judgedBrief)

	if code, out, stderr := d.run(t, "quality", "judge", "--repo", "app", "--review", "x", "--copy", dir, "t1"); code != 2 {
		t.Fatalf("a review that is not a number: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	started := startJudge(t, d, dir, "t1", 1)
	if started.Attempt != 1 || started.Review != 1 || started.Judge.Adapter != "fake" || started.Judge.Profile != "judge-fake" {
		t.Errorf("quality judge printed %+v, want attempt 1 on profile judge-fake", started)
	}
	awaitJudgement(t, d, "t1", 1, "no")
	task, err := withStore(d.store, true, func(s *store.Store) (store.Task, error) { return s.Task("app", "t1") })
	if err != nil || task.State != store.TaskDone {
		t.Errorf("task %+v, %v; want it still done", task, err)
	}

	code, out, stderr := d.run(t, "quality", "agreement", "--copy", dir)
	if code != 0 {
		t.Fatalf("quality agreement: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if rep := decodeJSON[quality.Report](t, out); rep.Judged != 1 || rep.Audited != 0 || len(rep.Pairs) != 0 {
		t.Errorf("quality agreement printed %+v, want one judged task and no pair", rep)
	}
}

// codexJudgeProfiles configures judge-codex, a codex profile with a model and an effort, ahead of judge-fake.
const codexJudgeProfiles = "[profile.judge-codex]\nadapter = \"codex\"\nmodel = \"gpt-5.5\"\neffort = \"high\"\n\n" + fakeJudgeProfile

// TestQualityJudgeEndToEndOnCodex: with a codex profile listed first, quality judge runs the judge through the owned
// launcher and `guildhall codex-artisan` over the codextest stub, the profile's model and effort on Codex's read-only
// command line; the wrapper writes the stub's answer as judgement.json and the advance records it and tells the
// Director. A Codex judge that runs out of quota publishes nothing, so its attempt fails, and the next quality judge
// for that self-review passes over the codex profile to judge-fake.
func TestQualityJudgeEndToEndOnCodex(t *testing.T) {
	t.Setenv(roleEnv, "guildhall")
	codextest.Install(t)
	dir := shortCopyWith(t, "judge-codex, judge-fake", codexJudgeProfiles)
	d := judgeCopy(t, dir)

	deliver(t, d, dir, "t1", "Score D1 against docs/quality.md.\n")
	started := startJudge(t, d, dir, "t1", 1)
	if started.Attempt != 1 || started.Judge.Adapter != "codex" || started.Judge.Profile != "judge-codex" {
		t.Errorf("quality judge printed %+v, want attempt 1 on profile judge-codex", started)
	}
	awaitJudgement(t, d, "t1", 1, "yes")
	attempt := d.judgeDir("t1", 1)
	args, err := codextest.RecordedArgs(filepath.Join(attempt, "prompt", render.PromptFile))
	if err != nil {
		t.Fatalf("the stub's recorded command line: %v; launcher log:\n%s", err, judgeLog(d, "t1", 1))
	}
	for _, want := range [][]string{{"--sandbox", codex.Sandbox}, {"-m", "gpt-5.5"}, {"-c", "model_reasoning_effort=high"}, codex.Switches} {
		if !slices.ContainsFunc(consecutiveRuns(args, len(want)), func(w []string) bool { return slices.Equal(w, want) }) {
			t.Errorf("codex exec %q lacks %q", args, want)
		}
	}
	if _, err := os.Stat(filepath.Join(attempt, "work", quality.AnswerFile)); err != nil {
		t.Errorf("the wrapper did not write %s: %v", quality.AnswerFile, err)
	}

	deliver(t, d, dir, "t2", judgedBrief+codextest.Directive+" "+string(codextest.TurnFailed)+"\n")
	if started := startJudge(t, d, dir, "t2", 1); started.Judge.Profile != "judge-codex" {
		t.Fatalf("quality judge printed %+v, want profile judge-codex first", started)
	}
	awaitJudge(t, d, "t2", 1, func(docs []store.DocumentRef, _ []store.Message, attempts []store.StageAttempt) bool {
		return slices.ContainsFunc(attempts, func(a store.StageAttempt) bool {
			return a.Step == workflow.JudgeStep && a.Number == 1 && a.Outcome == store.AttemptFailed
		})
	})
	if log := judgeLog(d, "t2", 1); !strings.Contains(string(log), codextest.QuotaMessage) {
		t.Errorf("the launcher log does not name the quota error:\n%s", log)
	}
	if started := startJudge(t, d, dir, "t2", 2); started.Attempt != 2 || started.Judge.Profile != "judge-fake" {
		t.Errorf("quality judge after the Codex judge failed printed %+v, want attempt 2 on judge-fake", started)
	}
	awaitJudgement(t, d, "t2", 2, "no")
}

// consecutiveRuns is every run of n consecutive elements of s.
func consecutiveRuns(s []string, n int) [][]string {
	var w [][]string
	for i := 0; i+n <= len(s); i++ {
		w = append(w, s[i:i+n])
	}
	return w
}
