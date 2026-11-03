package testenv_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/testenv"
)

// TestClaudeGuard: with a recording claude earlier on PATH, as a developer's real Claude Code would be, the
// installed guard is the claude exec.LookPath finds, and running it exits ClaudeGuardExit naming its arguments
// without reaching the other claude; a stub prepended afterwards, as claudetest.Install does, still wins; and a
// process that inherited the guard installs no second one.
func TestClaudeGuard(t *testing.T) {
	testGuard(t, "claude", testenv.ClaudeGuardEnv, testenv.ClaudeGuardMessage, testenv.ClaudeGuardExit, testenv.InstallClaudeGuard)
}

// TestCodexGuard: the codex guard keeps a developer's real Codex CLI out of a guarded test in the same way.
func TestCodexGuard(t *testing.T) {
	testGuard(t, "codex", testenv.CodexGuardEnv, testenv.CodexGuardMessage, testenv.CodexGuardExit, testenv.InstallCodexGuard)
}

func testGuard(t *testing.T, program, env, message string, exitCode int, install func() (func(), error)) {
	real := t.TempDir()
	marker := filepath.Join(t.TempDir(), "called")
	name, script := program, "#!/bin/sh\necho called > '"+marker+"'\n"
	if runtime.GOOS == "windows" {
		name, script = program+".cmd", "@echo called> \""+marker+"\"\r\n"
	}
	if err := os.WriteFile(filepath.Join(real, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", real+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(env, "")

	remove, err := install()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	guard := os.Getenv(env)
	found, err := exec.LookPath(program)
	if err != nil || guard == "" || filepath.Dir(found) != guard {
		t.Fatalf("LookPath(%s) = %q, %v; want the guard in %q", program, found, err, guard)
	}
	if strings.Contains(os.Getenv("PATH"), real) {
		t.Errorf("PATH %q still holds the other %s's directory %s", os.Getenv("PATH"), program, real)
	}
	out, err := exec.Command(found, "-p", "hello").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != exitCode || !strings.Contains(string(out), message+" -p hello") {
		t.Errorf("guard: %v, output %q; want exit %d with %q", err, out, exitCode, message+" -p hello")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the other %s ran: %v", program, err)
	}

	again, err := install()
	if err != nil {
		t.Fatal(err)
	}
	again()
	if os.Getenv(env) != guard || strings.Count(os.Getenv("PATH"), guard) != 1 {
		t.Errorf("second install: guard %q, PATH %q; want the first guard kept, once", os.Getenv(env), os.Getenv("PATH"))
	}
	if _, err := os.Stat(found); err != nil {
		t.Errorf("an inherited guard's remove removed it: %v", err)
	}

	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got, err := exec.LookPath(program); err != nil || filepath.Dir(got) != stub {
		t.Errorf("LookPath(%s) with a stub prepended = %q, %v; want the stub", program, got, err)
	}
}
