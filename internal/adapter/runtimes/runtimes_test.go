package runtimes_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/adapter/runtimes"
)

func TestLookupConstructsEachRuntime(t *testing.T) {
	if got, want := runtimes.Names(), []string{adapter.ClaudeName, adapter.FakeName}; !slices.Equal(got, want) {
		t.Fatalf("Names() = %q, want %q", got, want)
	}
	exe := filepath.Join(t.TempDir(), "guildhall")
	dir := t.TempDir()
	spec := adapter.Spec{Identity: adapter.Identity{TaskID: "t-1", AttemptID: "build-1"}, WorkDir: dir,
		PromptFile: filepath.Join(dir, "prompt.md"), ResultFile: filepath.Join(dir, "result.json")}
	for name, subcommand := range map[string]string{adapter.FakeName: fake.Subcommand, adapter.ClaudeName: claude.Subcommand} {
		r, err := runtimes.Lookup(name, exe)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		cmd, err := r.Command(spec)
		if err != nil || len(cmd.Argv) < 2 || cmd.Argv[0] != exe || cmd.Argv[1] != subcommand || cmd.Dir != dir {
			t.Errorf("%s command = %+v, %v; want %s %s in %s", name, cmd, err, exe, subcommand, dir)
		}
		if _, err := r.Command(adapter.Spec{}); err == nil {
			t.Errorf("%s built a command for an invalid spec", name)
		}
	}
}

func TestLookupRefusesAnUnknownName(t *testing.T) {
	for _, name := range []string{"", "codex", "mock", "Fake"} {
		r, err := runtimes.Lookup(name, "/bin/guildhall")
		if r != nil || !errors.Is(err, runtimes.ErrUnknown) {
			t.Errorf("Lookup(%q) = %v, %v; want ErrUnknown", name, r, err)
			continue
		}
		if !strings.Contains(err.Error(), "claude, fake") || name != "" && !strings.Contains(err.Error(), name) {
			t.Errorf("Lookup(%q) error %q does not name it and list claude, fake", name, err)
		}
	}
}

func TestLookupSessionConstructsEachRuntime(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "guildhall")
	dir := t.TempDir()
	spec := adapter.SessionSpec{Dir: dir, Copy: dir, RolePrompt: filepath.Join(dir, "role.md"), FirstMessage: "Start."}
	for name, program := range map[string]string{adapter.FakeName: "", adapter.ClaudeName: claude.Program} {
		r, err := runtimes.LookupSession(name, exe)
		if err != nil {
			t.Fatalf("LookupSession(%q): %v", name, err)
		}
		if r.Program() != program {
			t.Errorf("%s Program() = %q, want %q", name, r.Program(), program)
		}
		if cmd, err := r.Session(spec); err != nil || cmd.Dir != dir || cmd.Argv[len(cmd.Argv)-1] != "Start." {
			t.Errorf("%s session = %+v, %v", name, cmd, err)
		}
	}
	if r, err := runtimes.LookupSession("codex", exe); r != nil || !errors.Is(err, runtimes.ErrUnknown) || !strings.Contains(err.Error(), "claude, fake") {
		t.Errorf("LookupSession(codex) = %v, %v", r, err)
	}
}

// TestEachRuntimeChecksSettings: every runtime honours the zero Settings, the runtime's defaults, and refuses in
// Command whatever its CheckSettings refuses; the Claude runtime refuses an effort Claude Code does not take.
func TestEachRuntimeChecksSettings(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "guildhall")
	dir := t.TempDir()
	spec := adapter.Spec{Identity: adapter.Identity{TaskID: "t-1", AttemptID: "build-1"}, WorkDir: dir,
		PromptFile: filepath.Join(dir, "prompt.md"), ResultFile: filepath.Join(dir, "result.json")}
	for _, name := range runtimes.Names() {
		r, err := runtimes.Lookup(name, exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.CheckSettings(adapter.Settings{}); err != nil {
			t.Errorf("%s refused the defaults: %v", name, err)
		}
		bad := adapter.Settings{Effort: "extreme"}
		checked := r.CheckSettings(bad)
		spec.Settings = bad
		_, built := r.Command(spec)
		if (checked == nil) != (built == nil) || checked != nil && !errors.Is(built, adapter.ErrUnsupportedSetting) {
			t.Errorf("%s: CheckSettings = %v but Command = %v; want the same refusal", name, checked, built)
		}
	}
	r, _ := runtimes.Lookup(adapter.ClaudeName, exe)
	if err := r.CheckSettings(adapter.Settings{Effort: "extreme"}); !errors.Is(err, adapter.ErrUnsupportedSetting) || !strings.Contains(err.Error(), "extreme") {
		t.Errorf("claude CheckSettings = %v; want ErrUnsupportedSetting naming the effort", err)
	}
}
