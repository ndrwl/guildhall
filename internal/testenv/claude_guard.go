package testenv

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
// claudetest.Install prepends later still wins. TestMain calls it, through RunWithGuards, before m.Run and calls
// remove after; a process that inherited an installed guard (ClaudeGuardEnv) installs nothing and its remove does
// nothing. See docs/contracts/testenv.md.
func InstallClaudeGuard() (remove func(), err error) {
	return guard{program: "claude", env: ClaudeGuardEnv, message: ClaudeGuardMessage, exit: ClaudeGuardExit}.install()
}
