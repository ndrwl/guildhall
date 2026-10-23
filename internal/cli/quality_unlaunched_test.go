package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestDirectorStatusRecoversUnlaunchedJudges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		files  map[string]string
		review string
	}{
		{name: "before directory creation"},
		{name: "interrupted preparation", files: map[string]string{"prompt.partial/judge.json": `{"review":2,"judge":{"profile":"judge-fake","adapter":"fake","separate_family":false}}`}},
		{name: "missing brief", files: map[string]string{"prompt.partial/inputs/self-review.json": judgedSelfReview}},
		{name: "published preparation", review: "2", files: map[string]string{"prompt/judge.json": `{"review":2,"judge":{"profile":"judge-fake","adapter":"fake","separate_family":false}}`}},
		{name: "unreadable preparation", files: map[string]string{"prompt/judge.json": `{`}},
		{name: "missing review number", files: map[string]string{"prompt/judge.json": `{"judge":{"adapter":"fake","separate_family":false}}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			if _, err := s.CreateTask("create", store.Task{Repository: "app", ID: "t-1", Title: "Delivered", Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("a", 64)}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetTaskState("done", "app", "t-1", store.TaskDone); err != nil {
				t.Fatal(err)
			}
			if _, err := s.StartAttempt("unlaunched", "app", "t-1", workflow.JudgeStep); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			key := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: workflow.JudgeStep, Attempt: 1}
			for name, data := range tc.files {
				file := filepath.Join(key.Dir(state), filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				writeFile(t, file, data)
			}
			got, err := readDirectorStatus(context.Background(), path, state, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Tasks) != 1 || got.Tasks[0].State != store.TaskDone {
				t.Fatalf("task changed: %+v", got.Tasks)
			}
			if len(got.NeedsMe) != 1 {
				t.Fatalf("recovery items: %+v", got.NeedsMe)
			}
			item := got.NeedsMe[0]
			want := "guildhall quality judge --repo app t-1"
			if tc.review != "" {
				want = "guildhall quality judge --repo app --review " + tc.review + " t-1"
			}
			if item.Kind != "retry" || !item.Retryable || item.Resume || item.Attempt != 1 || item.Step != workflow.JudgeStep || item.Command != want {
				t.Fatalf("unlaunched recovery = %+v, want %s", item, want)
			}
			if tc.review == "" && !strings.Contains(item.Reason, "supply its self-review number with --review") {
				t.Fatalf("missing review guidance: %+v", item)
			}
		})
	}
}
