package claude_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
)

// TestSettingsReachClaude: a spec's model and effort reach Claude Code as --model and --effort, through the
// wrapper's command and its Claude arguments, and a spec with neither passes neither.
func TestSettingsReachClaude(t *testing.T) {
	spec := testSpec(t)
	spec.Settings = adapter.Settings{Model: "opus", Effort: "high"}
	program := filepath.Join(t.TempDir(), "claude")
	cmd, err := claude.Adapter{Executable: "/bin/guildhall", Claude: program}.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/guildhall", claude.Subcommand, "--task", "task-1", "--attempt", "attempt-1", "--prompt", spec.PromptFile, "--result", spec.ResultFile,
		"--model", "opus", "--effort", "high", "--claude", program}
	if !slices.Equal(cmd.Argv, want) {
		t.Errorf("Command argv = %q\nwant %q", cmd.Argv, want)
	}
	parsed, err := claude.ParseArgs(cmd.Argv[2:])
	if err != nil || parsed.Settings != spec.Settings {
		t.Fatalf("ParseArgs(Command) = %+v, %v; want settings %+v", parsed, err, spec.Settings)
	}
	got := claude.ClaudeArgs(parsed)
	if i := slices.Index(got, "--model"); i < 0 || i+1 >= len(got) || got[i+1] != "opus" {
		t.Errorf("ClaudeArgs = %q; want --model opus", got)
	}
	if i := slices.Index(got, "--effort"); i < 0 || i+1 >= len(got) || got[i+1] != "high" {
		t.Errorf("ClaudeArgs = %q; want --effort high", got)
	}
	spec.Settings = adapter.Settings{}
	cmd, err = claude.Adapter{Executable: "/bin/guildhall"}.Command(spec)
	if err != nil || slices.Contains(cmd.Argv, "--model") || slices.Contains(cmd.Argv, "--effort") {
		t.Errorf("Command without settings = %q, %v; want neither --model nor --effort", cmd.Argv, err)
	}
	if got := claude.ClaudeArgs(args(spec)); slices.Contains(got, "--model") || slices.Contains(got, "--effort") {
		t.Errorf("ClaudeArgs without settings = %q; want neither --model nor --effort", got)
	}
}

// TestSettingsReachTheStub: the wrapper runs the stub Claude Code with exactly the spec's model and effort.
func TestSettingsReachTheStub(t *testing.T) {
	claudetest.Install(t)
	for name, s := range map[string]adapter.Settings{"none": {}, "model": {Model: "claude-opus-5-5"}, "both": {Model: "sonnet", Effort: "xhigh"}} {
		t.Run(name, func(t *testing.T) {
			spec := testSpec(t)
			a := args(spec)
			a.Settings = s
			if err := claude.Run(context.Background(), a, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if got, err := claudetest.Settings(spec.ResultFile); err != nil || got != s {
				t.Errorf("stub settings = %+v, %v; want %+v", got, err, s)
			}
		})
	}
}

// TestClaudeRefusesSettingsItCannotHonour: an effort Claude Code does not take, or a model that is not one
// argument, is refused before any command is built, with adapter.ErrUnsupportedSetting; the wrapper's arguments
// refuse the same.
func TestClaudeRefusesSettingsItCannotHonour(t *testing.T) {
	for _, effort := range claude.Efforts {
		if err := (claude.Adapter{}).CheckSettings(adapter.Settings{Model: "opus", Effort: effort}); err != nil {
			t.Errorf("CheckSettings refused effort %q: %v", effort, err)
		}
	}
	if err := (claude.Adapter{}).CheckSettings(adapter.Settings{}); err != nil {
		t.Errorf("CheckSettings refused the defaults: %v", err)
	}
	for name, s := range map[string]adapter.Settings{
		"unknown effort":  {Effort: "extreme"},
		"effort case":     {Effort: "High"},
		"model flag":      {Model: "--dangerously-skip-permissions"},
		"model with gap":  {Model: "opus high"},
		"model control":   {Model: "opus\n"},
		"blank model":     {Model: " "},
		"effort and flag": {Model: "-m", Effort: "low"},
	} {
		if err := (claude.Adapter{}).CheckSettings(s); !errors.Is(err, adapter.ErrUnsupportedSetting) {
			t.Errorf("%s: CheckSettings(%+v) = %v, want ErrUnsupportedSetting", name, s, err)
		}
		spec := testSpec(t)
		spec.Settings = s
		if cmd, err := (claude.Adapter{Executable: "/bin/guildhall"}).Command(spec); !errors.Is(err, adapter.ErrUnsupportedSetting) || len(cmd.Argv) != 0 {
			t.Errorf("%s: Command = %+v, %v; want ErrUnsupportedSetting and no command", name, cmd, err)
		}
		argv := []string{"--task", "task-1", "--attempt", "attempt-1", "--prompt", spec.PromptFile, "--result", spec.ResultFile, "--model", s.Model, "--effort", s.Effort}
		if _, err := claude.ParseArgs(argv); err == nil {
			t.Errorf("%s: ParseArgs(%q) accepted", name, argv)
		}
	}
}
