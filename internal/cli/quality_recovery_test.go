package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestDirectorStatusRecoversDeliveredJudges(t *testing.T) {
	killed := exitedLauncher(t)
	for _, task := range []string{"t-1", "t-" + strings.Repeat("a", 61)} {
		t.Run(task, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "director.store")
			state := filepath.Join(dir, "state")
			s, err := store.Open(path, store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			def, err := workflow.Default()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateTask("create", store.Task{Repository: "app", ID: task, Title: "Delivered", Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("a", 64)}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetTaskState("done", "app", task, store.TaskDone); err != nil {
				t.Fatal(err)
			}
			if _, err := s.StartOwnedAttempt("orphan", store.Ownership{Repository: "app", TaskID: task, Step: workflow.JudgeStep, LauncherIdentity: killed}); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			read := func() map[int]needsMeItem {
				t.Helper()
				got, err := readDirectorStatus(context.Background(), path, state, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				if len(got.Tasks) != 1 || got.Tasks[0].State != store.TaskDone {
					t.Fatalf("task changed: %+v", got.Tasks)
				}
				items := map[int]needsMeItem{}
				for _, item := range got.NeedsMe {
					if item.Step == workflow.JudgeStep {
						items[item.Attempt] = item
					}
				}
				return items
			}
			items := read()
			want := fmt.Sprintf("guildhall artisan resume --repo app --task %s --step quality-judge --attempt 1", task)
			if len(items) != 1 || items[1].Command != want || !items[1].Resume {
				t.Fatalf("orphan recovery = %+v", items)
			}

			attemptDir := (artisan.AttemptKey{Repository: "app", TaskID: task, Step: workflow.JudgeStep, Attempt: 1}).Dir(state)
			if err := os.MkdirAll(attemptDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, outcome := range []string{"finished", "failed", "cancelled"} {
				writeFile(t, filepath.Join(attemptDir, artisan.OutcomeFile), fmt.Sprintf(`{"schema":%q,"state":%q,"detail":"launcher interrupted before settlement"}`, artisan.OutcomeSchema, outcome))
				items := read()
				if len(items) != 1 || items[1].Command != want || !items[1].Resume {
					t.Fatalf("published %s recovery = %+v", outcome, items)
				}
			}

			s, err = store.Open(path, store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			for i, outcome := range []store.AttemptOutcome{store.AttemptSucceeded, store.AttemptFailed, store.AttemptStopped, store.AttemptSucceeded} {
				n := i + 1
				if n > 1 {
					if _, err := s.StartAttempt(fmt.Sprintf("start-%d", n), "app", task, workflow.JudgeStep); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.FinishAttempt(fmt.Sprintf("finish-%d", n), "app", task, workflow.JudgeStep, n, outcome); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			items = read()
			if len(items) != 4 {
				t.Fatalf("pending intakes = %+v", items)
			}
			for n := 1; n <= 4; n++ {
				want := fmt.Sprintf("guildhall quality judged --repo app --task %s --attempt %d", task, n)
				if items[n].Command != want || items[n].Kind != "advance" {
					t.Fatalf("intake %d = %+v", n, items[n])
				}
			}

			s, err = store.Open(path, store.Options{})
			if err != nil {
				t.Fatal(err)
			}
			for i, typ := range []string{store.MessageJudgement, store.MessageJudgeFailed, store.MessageJudgeFailed, store.MessageJudgementRefused} {
				id := quality.MessageID("app", task, i+1, typ)
				if _, err := s.SendMessage(store.Message{ID: id, Recipient: store.RecipientDirector, Repository: "app", TaskID: task, Type: typ, Text: "Judge intake complete."}); err != nil {
					t.Fatal(err)
				}
				if _, err := s.AckMessage(store.Recipient{Role: store.RecipientDirector}, id); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if items := read(); len(items) != 0 {
				t.Fatalf("completed intakes still need recovery: %+v", items)
			}
		})
	}
}
