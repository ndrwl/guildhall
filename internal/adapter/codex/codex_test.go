package codex_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/codex"
)

func spec(t *testing.T) adapter.Spec {
	dir := t.TempDir()
	return adapter.Spec{Identity: adapter.Identity{TaskID: "t-1", AttemptID: "quality-judge-1"}, WorkDir: dir,
		PromptFile: filepath.Join(dir, "prompt.md"), ResultFile: filepath.Join(dir, "model", "result.json"), Env: []string{"A=1"}}
}

// TestCommand: the wrapper runs as `guildhall codex-artisan` in the working directory with the spec's environment,
// with the identity, files, profile reference and each setting only when set.
func TestCommand(t *testing.T) {
	s := spec(t)
	a := codex.Adapter{Executable: "/bin/guildhall"}
	cmd, err := a.Command(s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/guildhall", codex.Subcommand, "--task", "t-1", "--attempt", "quality-judge-1", "--prompt", s.PromptFile, "--result", s.ResultFile}
	if !slices.Equal(cmd.Argv, want) || cmd.Dir != s.WorkDir || !slices.Equal(cmd.Env, s.Env) {
		t.Errorf("command = %+v, want argv %q in %s", cmd, want, s.WorkDir)
	}
	s.Repository, s.Settings = "site", adapter.Settings{Model: "gpt-5.5", Effort: "high"}
	a.Codex = "/opt/codex"
	cmd, err = a.Command(s)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, "--repo", "site", "--model", "gpt-5.5", "--effort", "high", "--codex", "/opt/codex")
	if !slices.Equal(cmd.Argv, want) {
		t.Errorf("argv = %q, want %q", cmd.Argv, want)
	}
}

func TestCommandRefusals(t *testing.T) {
	if _, err := (codex.Adapter{Executable: "/bin/guildhall"}).Command(adapter.Spec{}); err == nil {
		t.Error("built a command for an invalid spec")
	}
	if _, err := (codex.Adapter{}).Command(spec(t)); err == nil || !strings.Contains(err.Error(), "executable") {
		t.Errorf("no executable: %v", err)
	}
	s := spec(t)
	s.Effort = "extreme"
	if _, err := (codex.Adapter{Executable: "/bin/guildhall"}).Command(s); !errors.Is(err, adapter.ErrUnsupportedSetting) {
		t.Errorf("bad effort: %v", err)
	}
}

// TestCheckSettings: every effort Codex takes and a one-word model are honoured; anything else is refused naming it.
func TestCheckSettings(t *testing.T) {
	a := codex.Adapter{}
	for _, e := range codex.Efforts {
		if err := a.CheckSettings(adapter.Settings{Model: "gpt-5.5", Effort: e}); err != nil {
			t.Errorf("effort %s: %v", e, err)
		}
	}
	for _, s := range []adapter.Settings{{Effort: "extreme"}, {Effort: "High"}, {Model: "-c"}, {Model: "gpt 5"}, {Model: "gpt\n5"}} {
		err := a.CheckSettings(s)
		if !errors.Is(err, adapter.ErrUnsupportedSetting) || !strings.Contains(err.Error(), "codex adapter") {
			t.Errorf("%+v: %v, want ErrUnsupportedSetting", s, err)
		}
	}
	if (codex.Adapter{}).Capabilities().Any() {
		t.Error("codex declares an optional capability")
	}
}
