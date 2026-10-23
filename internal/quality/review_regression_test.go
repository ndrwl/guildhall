package quality_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
)

func TestJudgeUsesLatestSuccessfulBuildIdentity(t *testing.T) {
	e := newEnv(t, judgeConfig, func(e *env) {
		for _, b := range []struct{ step, profile string }{{"z-initial", "builder-claude"}, {"a-final", "judge-fake"}} {
			r, err := e.s.StartAttempt("start-"+b.step, "app", "t-1", b.step)
			if err != nil {
				t.Fatal(err)
			}
			k := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: b.step, Attempt: r.Attempt}
			data, err := json.Marshal(render.Manifest{Schema: render.ManifestSchema, Identity: render.Identity{Kind: "build", Profile: b.profile}})
			if err != nil {
				t.Fatal(err)
			}
			e.write(filepath.Join(k.Dir(e.judge.StateDir), "prompt", "manifest.json"), string(data))
			if _, err := e.s.FinishAttempt("finish-"+b.step, "app", "t-1", b.step, r.Attempt, store.AttemptSucceeded); err != nil {
				t.Fatal(err)
			}
		}
	})
	got, err := e.judge.Start(context.Background(), "app", "t-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Judge.Profile != "judge-claude" || got.Judge.Builder != "" || got.Judge.SeparateFamily {
		t.Fatalf("judge = %+v", got.Judge)
	}
}

func TestJudgeRefusesBuilderOnlyProfile(t *testing.T) {
	e := newEnv(t, strings.Replace(judgeConfig, `quality_judge_profiles = "judge-fake, judge-claude"`, `quality_judge_profiles = "builder-claude"`, 1))
	if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); !errors.Is(err, config.ErrNoJudge) {
		t.Fatalf("Start = %v", err)
	}
	if len(e.judgeAttempts()) != 0 || len(e.l.calls) != 0 {
		t.Fatal("ineligible judge admitted")
	}
}

func TestUnlaunchedJudgeHonorsDirectorHold(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing brief", true: "published preparation"}[prepared], func(t *testing.T) {
			e := newEnv(t, judgeConfig)
			brief := filepath.Join(e.judge.QualityDir, "app", "t-1", "self-review-1-judge.md")
			if prepared {
				e.l.fail = func(int) error { return errors.New("launch unavailable") }
			} else if err := os.Remove(brief); err != nil {
				t.Fatal(err)
			}
			if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); err == nil {
				t.Fatal("expected preparation or launch failure")
			}
			e.l.fail = nil
			e.write(brief, "Judge the pending criteria.")
			_, err := e.s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 4242, Token: "boot-1:77"}, Release: "v0.4.0", ConfigDigest: "sha256:" + strings.Repeat("c", 64), Pane: store.DirectorPane{Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p2"}})
			if err != nil {
				t.Fatal(err)
			}
			for _, state := range []store.DirectorState{store.DirectorRunning, store.DirectorPaused} {
				d, err := e.s.DirectorSession(1)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e.s.SetDirectorState(1, state, d.StateSequence); err != nil {
					t.Fatal(err)
				}
			}
			e.judge.Caps.Hold = true
			calls := len(e.l.calls)
			if _, err := e.judge.Start(context.Background(), "app", "t-1", 1); !errors.Is(err, store.ErrDirectorHeld) {
				t.Fatalf("held Start = %v", err)
			}
			if len(e.l.calls) != calls || len(e.judgeAttempts()) != 1 {
				t.Fatal("held retry launched or recorded another attempt")
			}
			d, err := e.s.DirectorSession(1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.s.SetDirectorState(1, store.DirectorRunning, d.StateSequence); err != nil {
				t.Fatal(err)
			}
			got, err := e.judge.Start(context.Background(), "app", "t-1", 1)
			if err != nil || got.Attempt != 1 || len(e.l.calls) != calls+1 {
				t.Fatalf("resumed Start = %+v, %v", got, err)
			}
		})
	}
}
