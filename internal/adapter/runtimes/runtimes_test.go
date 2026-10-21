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
	if got, want := runtimes.Names(), []string{runtimes.Claude, runtimes.Fake}; !slices.Equal(got, want) {
		t.Fatalf("Names() = %q, want %q", got, want)
	}
	exe := filepath.Join(t.TempDir(), "guildhall")
	dir := t.TempDir()
	spec := adapter.Spec{Identity: adapter.Identity{TaskID: "t-1", AttemptID: "build-1"}, WorkDir: dir,
		PromptFile: filepath.Join(dir, "prompt.md"), ResultFile: filepath.Join(dir, "result.json")}
	for name, subcommand := range map[string]string{runtimes.Fake: fake.Subcommand, runtimes.Claude: claude.Subcommand} {
		r, err := runtimes.Lookup(name, exe)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		if r.Capabilities().Any() {
			t.Errorf("%s declares capabilities %+v", name, r.Capabilities())
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
