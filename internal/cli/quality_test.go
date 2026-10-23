package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/testenv"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// judgedSelfReview is self-review 1 of app/t-1 with D1 a pending score; its brief tells the fake judge what to write.
const judgedSelfReview = `{"schema": "guildhall.self-review/1", "repository": "app", "task": "t-1", "number": 1,
 "head": "9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b",
 "criteria": [{"id": "D1", "kind": "score", "judge": "pending", "score": null, "evidence": "nontest_lines: 140"}]}`

const judgedBrief = "Score D1 against docs/quality.md.\n" +
	`guildhall-fake: output=judgement.json:{"merge_as_is": "no", "criteria": [{"id": "D1", "score": 2, "evidence": "140 non-test lines"}]}` + "\n"

// shortCopy is newCopy in a short temporary directory, so the attempts' endpoint paths fit the AF_UNIX limit.
func shortCopy(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.CopyFS(dir, os.DirFS(filepath.Dir(writeDirector(t, directorTOML)))); err != nil {
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

// TestQualityJudgeEndToEnd: over a copy, quality judge launches the judge of a delivered task's self-review through
// the owned launcher with the fake adapter, whose advance, quality judged, records the judgement and tells the
// Director; quality agreement then counts it as judged and not audited; the task stays done.
func TestQualityJudgeEndToEnd(t *testing.T) {
	t.Setenv(roleEnv, "guildhall")
	dir := shortCopy(t)
	d := runDirector{store: filepath.Join(dir, "director.store"), config: filepath.Join(dir, "config.toml"), state: filepath.Join(dir, "state")}
	t.Cleanup(func() { d.stopAll(t) })
	newTask(t, d.store, d.config, "app", "Sort the task list", "t-1")
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.SetTaskState("deliver", "app", "t-1", store.TaskDone)
	}); err != nil {
		t.Fatal(err)
	}
	reviews := filepath.Join(dir, "quality", "app", "t-1")
	if err := os.MkdirAll(reviews, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"self-review-1.json": judgedSelfReview, "self-review-1-judge.md": judgedBrief} {
		if err := os.WriteFile(filepath.Join(reviews, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if code, out, stderr := d.run(t, "quality", "judge", "--repo", "app", "--review", "x", "--copy", dir, "t-1"); code != 2 {
		t.Fatalf("a review that is not a number: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	code, out, stderr := d.run(t, "quality", "judge", "--repo", "app", "--review", "1", "--copy", dir, "t-1")
	if code != 0 {
		log, _ := os.ReadFile(filepath.Join(d.state, "app", "t-1", workflow.JudgeStep+"-1", "launcher.log"))
		t.Fatalf("quality judge: exit %d, stdout %q, stderr %q; launcher log:\n%s", code, out, stderr, log)
	}
	started := decodeJSON[quality.Started](t, out)
	if started.Attempt != 1 || started.Review != 1 || started.Judge.Adapter != "fake" || started.Judge.Profile != "" {
		t.Errorf("quality judge printed %+v, want attempt 1 on the default fake adapter", started)
	}

	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(2*time.Minute))
	defer cancel()
	for {
		docs, err := withStore(d.store, true, func(s *store.Store) ([]store.DocumentRef, error) { return s.Documents("app", "t-1") })
		if err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(docs, func(doc store.DocumentRef) bool { return doc.Name == workflow.JudgeStep+"-1-judgement" }) {
			break
		}
		select {
		case <-ctx.Done():
			log, _ := os.ReadFile(filepath.Join(d.state, "app", "t-1", workflow.JudgeStep+"-1", "launcher.log"))
			t.Fatalf("no judgement recorded; documents %+v; launcher log:\n%s", docs, log)
		case <-time.After(50 * time.Millisecond):
		}
	}
	msgs, err := withStore(d.store, true, func(s *store.Store) ([]store.Message, error) {
		m, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
		return m, err
	})
	if err != nil || !slices.ContainsFunc(msgs, func(m store.Message) bool {
		return m.Type == "judgement" && strings.Contains(m.Text, "would merge it as is: no")
	}) {
		t.Errorf("Director messages %+v, %v; want a judgement saying no", msgs, err)
	}
	task, err := withStore(d.store, true, func(s *store.Store) (store.Task, error) { return s.Task("app", "t-1") })
	if err != nil || task.State != store.TaskDone {
		t.Errorf("task %+v, %v; want it still done", task, err)
	}

	code, out, stderr = d.run(t, "quality", "agreement", "--copy", dir)
	if code != 0 {
		t.Fatalf("quality agreement: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if rep := decodeJSON[quality.Report](t, out); rep.Judged != 1 || rep.Audited != 0 || len(rep.Pairs) != 0 {
		t.Errorf("quality agreement printed %+v, want one judged task and no pair", rep)
	}
}
