package quality_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// taskDocuments are documents recorded for app/t-1, by name, each with its recorded path and text.
var taskDocuments = map[string][2]string{
	"plan-1-spec":               {"tasks/app/t-1/plan-1/spec.md", "# Spec\n\nSort the list.\n"},
	"research-1-notes":          {"tasks/app/t-1/research-1/notes.md", "# Notes\n\nThe list is unsorted.\n"},
	"review-1-outcome":          {"tasks/app/t-1/review-1/outcome.json", `{"verdict": "approve"}` + "\n"},
	"quality-judge-1-judgement": {"tasks/app/t-1/quality-judge-1/judgement.json", `{"schema": "guildhall.judgement/1"}` + "\n"},
}

// TestStartGivesTheJudgeEverythingInItsInputs is the regression test of guildhall/d200's quality-judge-2, which
// refused to score because it could not read the rubric, the task documents or the changed files: after Start the
// judge's inputs hold the rubric at the head, every task document but an earlier judgement byte for byte at its
// recorded path, and each file the change adds or changes at the head, and no deleted file; the prompt names them.
func TestStartGivesTheJudgeEverythingInItsInputs(t *testing.T) {
	e := newEnv(t, judgeConfig)
	for name, d := range taskDocuments {
		e.document(name, d[0], d[1])
	}
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(e.key(1).Dir(e.judge.StateDir), "prompt")
	inputs := filepath.Join(prompt, render.InputsDir)
	want := map[string]string{
		"docs/quality.md":      rubric,
		"head/docs/quality.md": rubric,
		"head/changed.txt":     changedHead,
		"head/sub/added.txt":   addedHead,
	}
	for name, d := range taskDocuments {
		if !strings.HasPrefix(name, "quality-judge-") {
			want[d[0]] = d[1]
		}
	}
	for rel, text := range want {
		got, err := os.ReadFile(filepath.Join(inputs, filepath.FromSlash(rel)))
		if err != nil || string(got) != text {
			t.Errorf("input %s = %q, %v; want %q", rel, got, err, text)
		}
	}
	for _, rel := range []string{"head/deleted.txt", "head/same.txt", taskDocuments["quality-judge-1-judgement"][0]} {
		if _, err := os.Stat(filepath.Join(inputs, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("input %s exists (%v), want it not copied", rel, err)
		}
	}
	text, err := os.ReadFile(filepath.Join(prompt, render.PromptFile))
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for _, s := range []string{"`inputs/docs/quality.md`", "`inputs/tasks/app/t-1/plan-1/spec.md`", "`inputs/head/sub/added.txt`",
		"`deleted.txt`: deleted at the head", "`inputs/change.diff`", "nothing outside it need be read"} {
		if !strings.Contains(string(text), s) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 || strings.Contains(string(text), "facts.txt") {
		t.Errorf("prompt does not name %q, or names facts.txt that was not given:\n%s", missing, text)
	}
}

// TestStartRefusesInputsItCannotGive: a task document whose file is not its recorded content, a head the
// repository lacks and a head with no rubric each refuse Start with ErrInputs, and record and launch nothing.
func TestStartRefusesInputsItCannotGive(t *testing.T) {
	for name, arrange := range map[string]func(e *env){
		"a tampered document": func(e *env) {
			e.document("plan-1-spec", "tasks/app/t-1/plan-1/spec.md", "# Spec\n")
			e.write(filepath.Join(e.judge.DocumentDir, "tasks", "app", "t-1", "plan-1", "spec.md"), "# Another spec\n")
		},
		"a head the repository lacks": func(e *env) {
			e.write(filepath.Join(e.judge.QualityDir, "app", "t-1", "self-review-1.json"), strings.Replace(e.selfReview(), e.head, strings.Repeat("0", 40), 1))
		},
		"a head with no rubric": func(e *env) {
			e.write(filepath.Join(e.judge.QualityDir, "app", "t-1", "self-review-1.json"), strings.Replace(e.selfReview(), e.head, e.base, 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, judgeConfig)
			arrange(e)
			if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); !errors.Is(err, quality.ErrInputs) {
				t.Errorf("Start = %v, want ErrInputs", err)
			}
			if len(e.l.calls) != 0 || len(e.judgeAttempts()) != 0 {
				t.Errorf("a refused start launched %+v and recorded %+v", e.l.calls, e.judgeAttempts())
			}
		})
	}
}

// TestStartReplayGathersTheInputs: an attempt recorded but never prepared, as a start that stopped between the two
// leaves it, is prepared on the next start with the rubric, changed files and documents.
func TestStartReplayGathersTheInputs(t *testing.T) {
	e := newEnv(t, judgeConfig)
	e.document("plan-1-spec", "tasks/app/t-1/plan-1/spec.md", "# Spec\n")
	if _, err := e.s.StartAttempt("judge", "app", "t-1", workflow.JudgeStep); err != nil {
		t.Fatal(err)
	}
	got, err := e.judge.Start(context.Background(), "app", "t-1", 1)
	if err != nil || got.Attempt != 1 {
		t.Fatalf("Start = %+v, %v; want recorded attempt 1 launched", got, err)
	}
	inputs := filepath.Join(e.key(1).Dir(e.judge.StateDir), "prompt", render.InputsDir)
	for rel, text := range map[string]string{"docs/quality.md": rubric, "head/changed.txt": changedHead, "tasks/app/t-1/plan-1/spec.md": "# Spec\n"} {
		if data, err := os.ReadFile(filepath.Join(inputs, filepath.FromSlash(rel))); err != nil || string(data) != text {
			t.Errorf("input %s = %q, %v; want %q", rel, data, err, text)
		}
	}
	if len(e.judgeAttempts()) != 1 {
		t.Errorf("attempts %+v, want the recorded one only", e.judgeAttempts())
	}
}
