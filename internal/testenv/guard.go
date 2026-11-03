package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// guard is a program on PATH that a test must never reach for real: a model runtime whose run would make a model
// call.
type guard struct {
	// program is the name looked up on PATH, such as "claude".
	program string
	// env names the variable holding the directory of the installed guard.
	env string
	// message starts the line the guard writes to standard error, followed by its arguments.
	message string
	// exit is the guard's exit status.
	exit int
}

// install makes g.program on PATH a guard that writes g.message and its arguments to standard error and exits
// g.exit. It writes the guard, a shell script on Unix and <program>.cmd on Windows, to a new temporary directory,
// removes from PATH every directory holding another g.program executable, and appends the guard's directory, so a
// stub a test prepends later still wins. A process that inherited an installed guard (g.env) installs nothing and
// its remove does nothing.
func (g guard) install() (remove func(), err error) {
	if dir := os.Getenv(g.env); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, g.fileName())); err == nil {
			return func() {}, nil
		}
	}
	dir, err := os.MkdirTemp("", "guildhall-"+g.program+"-guard-")
	if err != nil {
		return nil, fmt.Errorf("%s guard: %w", g.program, err)
	}
	remove = func() { _ = os.RemoveAll(dir) }
	script := "#!/bin/sh\necho \"" + g.message + " $*\" >&2\nexit " + fmt.Sprint(g.exit) + "\n"
	if runtime.GOOS == "windows" {
		script = "@echo " + g.message + " %* 1>&2\r\n@exit /b " + fmt.Sprint(g.exit) + "\r\n"
	}
	if err := os.WriteFile(filepath.Join(dir, g.fileName()), []byte(script), 0o755); err != nil {
		remove()
		return nil, fmt.Errorf("%s guard: %w", g.program, err)
	}
	var kept []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if !holds(d, g.program) {
			kept = append(kept, d)
		}
	}
	kept = append(kept, dir)
	if err := os.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator))); err != nil {
		remove()
		return nil, err
	}
	if err := os.Setenv(g.env, dir); err != nil {
		remove()
		return nil, err
	}
	return remove, nil
}

func (g guard) fileName() string {
	if runtime.GOOS == "windows" {
		return g.program + ".cmd"
	}
	return g.program
}

// RunWithGuards runs m with the claude and codex guards installed (InstallClaudeGuard, InstallCodexGuard), removes
// them, and returns m's exit code, or 2 when a guard cannot be installed; TestMain passes the code to os.Exit.
func RunWithGuards(m *testing.M) int {
	for _, install := range []func() (func(), error){InstallClaudeGuard, InstallCodexGuard} {
		remove, err := install()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		defer remove()
	}
	return m.Run()
}

// holds reports whether directory dir holds a program called name that exec.LookPath would find: on Unix an
// executable file named name, on Windows name with any PATHEXT extension.
func holds(dir, name string) bool {
	if dir == "" {
		return false
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, name))
		return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
	}
	exts := filepath.SplitList(os.Getenv("PATHEXT"))
	if len(exts) == 0 {
		exts = []string{".com", ".exe", ".bat", ".cmd"}
	}
	for _, ext := range exts {
		if info, err := os.Stat(filepath.Join(dir, name+strings.ToLower(ext))); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}
