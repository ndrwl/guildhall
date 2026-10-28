package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ClaudeGuardEnv names the environment variable holding the directory of the installed claude guard, so a test
// binary's children, which inherit it with PATH, do not install another.
const ClaudeGuardEnv = "GUILDHALL_TEST_CLAUDE_GUARD"

// ClaudeGuardExit is the exit status of the claude guard.
const ClaudeGuardExit = 97

// ClaudeGuardMessage starts the line the claude guard writes to standard error, followed by its arguments.
const ClaudeGuardMessage = "guildhall test: real claude invoked:"

// InstallClaudeGuard makes `claude` on PATH a guard that writes ClaudeGuardMessage and its arguments to standard
// error and exits ClaudeGuardExit, so a test that reaches for a real Claude Code fails loudly instead of making a
// model call. It writes the guard, a shell script on Unix and claude.cmd on Windows, to a new temporary directory,
// removes from PATH every directory holding another claude executable, and appends the guard's directory, so a stub
// claudetest.Install prepends later still wins. TestMain calls it before m.Run and calls remove after; a process
// that inherited an installed guard (ClaudeGuardEnv) installs nothing and its remove does nothing.
// See docs/contracts/testenv.md.
func InstallClaudeGuard() (remove func(), err error) {
	if dir := os.Getenv(ClaudeGuardEnv); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, claudeGuardName())); err == nil {
			return func() {}, nil
		}
	}
	dir, err := os.MkdirTemp("", "guildhall-claude-guard-")
	if err != nil {
		return nil, fmt.Errorf("claude guard: %w", err)
	}
	remove = func() { _ = os.RemoveAll(dir) }
	script := "#!/bin/sh\necho \"" + ClaudeGuardMessage + " $*\" >&2\nexit " + fmt.Sprint(ClaudeGuardExit) + "\n"
	if runtime.GOOS == "windows" {
		script = "@echo " + ClaudeGuardMessage + " %* 1>&2\r\n@exit /b " + fmt.Sprint(ClaudeGuardExit) + "\r\n"
	}
	if err := os.WriteFile(filepath.Join(dir, claudeGuardName()), []byte(script), 0o755); err != nil {
		remove()
		return nil, fmt.Errorf("claude guard: %w", err)
	}
	var kept []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if !holdsClaude(d) {
			kept = append(kept, d)
		}
	}
	kept = append(kept, dir)
	if err := os.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator))); err != nil {
		remove()
		return nil, err
	}
	if err := os.Setenv(ClaudeGuardEnv, dir); err != nil {
		remove()
		return nil, err
	}
	return remove, nil
}

// RunWithClaudeGuard runs m with the claude guard installed (InstallClaudeGuard), removes it, and returns m's exit
// code, or 2 when the guard cannot be installed; TestMain passes the code to os.Exit.
func RunWithClaudeGuard(m *testing.M) int {
	remove, err := InstallClaudeGuard()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer remove()
	return m.Run()
}

func claudeGuardName() string {
	if runtime.GOOS == "windows" {
		return "claude.cmd"
	}
	return "claude"
}

// holdsClaude reports whether directory dir holds a claude program exec.LookPath would find: on Unix an executable
// file named claude, on Windows claude with any PATHEXT extension.
func holdsClaude(dir string) bool {
	if dir == "" {
		return false
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "claude"))
		return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
	}
	exts := filepath.SplitList(os.Getenv("PATHEXT"))
	if len(exts) == 0 {
		exts = []string{".com", ".exe", ".bat", ".cmd"}
	}
	for _, ext := range exts {
		if info, err := os.Stat(filepath.Join(dir, "claude"+strings.ToLower(ext))); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}
