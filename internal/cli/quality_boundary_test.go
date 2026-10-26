package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/store"
)

func TestQualityJudgeRepositoryBoundary(t *testing.T) {
	t.Setenv(roleEnv, "guildhall")
	for _, command := range [][]string{
		{"quality", "judge", "--review", "1", "t-1"},
		{"quality", "judged", "--attempt", "1", "--task", "t-1"},
	} {
		t.Run(command[1], func(t *testing.T) {
			dir := shortCopy(t)
			cfg := filepath.Join(dir, "config.toml")
			newTask(t, filepath.Join(dir, "director.store"), cfg, "app", "Task", "t-1")
			data, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, []byte(strings.Replace(string(data), `id = "app"`, `id = "removed"`, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			app, _, stderr := newTestApp(nil, nil)
			app.Executable = func() (string, error) { t.Fatal("unlisted repository reached executable lookup"); return "", nil }
			app.InstallBaseline = func(string) error { t.Fatal("unlisted repository installed baseline"); return nil }
			code := app.Run(append(command, "--copy", dir, "--repo", "app"))
			if code == 0 || !strings.Contains(stderr.String(), config.ErrRepositoryNotListed.Error()) {
				t.Fatalf("exit %d, stderr %s; want repository guard rail", code, stderr)
			}
			if _, err := os.Stat(filepath.Join(dir, "state", "app", "t-1")); !os.IsNotExist(err) {
				t.Fatalf("task directory created: %v", err)
			}
		})
	}
}

func TestQualityJudgeConfiguredLockWait(t *testing.T) {
	t.Setenv(roleEnv, "guildhall")
	for _, command := range [][]string{
		{"quality", "judge", "--review", "1", "t-1"},
		{"quality", "judged", "--attempt", "1", "--task", "t-1"},
	} {
		for _, lock := range []string{"task", "store"} {
			t.Run(command[1]+"/"+lock, func(t *testing.T) {
				dir := shortCopy(t)
				d := runDirector{store: filepath.Join(dir, "director.store"), config: filepath.Join(dir, "config.toml"), state: filepath.Join(dir, "state")}
				data, err := os.ReadFile(d.config)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(d.config, []byte(strings.Replace(string(data), "[local]", "[local]\nlock_wait = \"1s\"", 1)), 0600); err != nil {
					t.Fatal(err)
				}
				want := "lock task app/t-1"
				if lock == "task" {
					taskDir := filepath.Join(d.state, "app", "t-1")
					if err := os.MkdirAll(taskDir, 0755); err != nil {
						t.Fatal(err)
					}
					release, err := artisan.LockAttempt(context.Background(), taskDir)
					if err != nil {
						t.Fatal(err)
					}
					defer release()
				} else {
					holder, err := store.Open(d.store, store.Options{})
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := holder.Close(); err != nil {
							t.Errorf("close store holder: %v", err)
						}
					}()
					want = store.ErrLocked.Error()
				}
				start := time.Now()
				code, _, stderr := d.run(t, append(command, "--copy", dir, "--repo", "app")...)
				if code == 0 || !strings.Contains(stderr, want) {
					t.Fatalf("exit %d, stderr %s; want %s", code, stderr, want)
				}
				if elapsed := time.Since(start); elapsed > time.Minute {
					t.Fatalf("lock wait %v; want configured 1s wait within 4s", elapsed)
				}
			})
		}
	}
}

func TestQualityJudgePassesLockWaitToDependencies(t *testing.T) {
	dir := shortCopy(t)
	f := advanceFlags{store: filepath.Join(dir, "director.store"), config: filepath.Join(dir, "config.toml"), state: filepath.Join(dir, "state"), repo: "app", copy: copyFlags{dir: dir}}
	data, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.config, []byte(strings.Replace(string(data), "[local]", "[local]\nlock_wait = \"2m\"", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	app, _, _ := newTestApp(nil, nil)
	app.Executable = os.Executable
	app.InstallBaseline = baseline.Install
	called := false
	err = app.withJudge(f, "t-1", func(j *quality.Judge) error {
		called = true
		records := j.Records.(storeRecords)
		launcher := j.Launcher.(*launch.Process)
		if records.wait != 2*time.Minute || launcher.LockWait != 2*time.Minute {
			t.Fatalf("records wait %v, launcher wait %v", records.wait, launcher.LockWait)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("withJudge: called %v, error %v", called, err)
	}
}
