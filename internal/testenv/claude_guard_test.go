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
	real := t.TempDir()
	marker := filepath.Join(t.TempDir(), "called")
	name, script := "claude", "#!/bin/sh\necho called > '"+marker+"'\n"
	if runtime.GOOS == "windows" {
		name, script = "claude.cmd", "@echo called> \""+marker+"\"\r\n"
	}
	if err := os.WriteFile(filepath.Join(real, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", real+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(testenv.ClaudeGuardEnv, "")

	remove, err := testenv.InstallClaudeGuard()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	guard := os.Getenv(testenv.ClaudeGuardEnv)
	found, err := exec.LookPath("claude")
	if err != nil || guard == "" || filepath.Dir(found) != guard {
		t.Fatalf("LookPath(claude) = %q, %v; want the guard in %q", found, err, guard)
	}
	if strings.Contains(os.Getenv("PATH"), real) {
		t.Errorf("PATH %q still holds the other claude's directory %s", os.Getenv("PATH"), real)
	}
	out, err := exec.Command(found, "-p", "hello").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != testenv.ClaudeGuardExit || !strings.Contains(string(out), testenv.ClaudeGuardMessage+" -p hello") {
		t.Errorf("guard: %v, output %q; want exit %d with %q", err, out, testenv.ClaudeGuardExit, testenv.ClaudeGuardMessage+" -p hello")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the other claude ran: %v", err)
	}

	again, err := testenv.InstallClaudeGuard()
	if err != nil {
		t.Fatal(err)
	}
	again()
	if os.Getenv(testenv.ClaudeGuardEnv) != guard || strings.Count(os.Getenv("PATH"), guard) != 1 {
		t.Errorf("second install: guard %q, PATH %q; want the first guard kept, once", os.Getenv(testenv.ClaudeGuardEnv), os.Getenv("PATH"))
	}
	if _, err := os.Stat(found); err != nil {
		t.Errorf("an inherited guard's remove removed it: %v", err)
	}

	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got, err := exec.LookPath("claude"); err != nil || filepath.Dir(got) != stub {
		t.Errorf("LookPath(claude) with a stub prepended = %q, %v; want the stub", got, err)
	}
}
