package testenv

// CodexGuardEnv names the environment variable holding the directory of the installed codex guard, so a test
// binary's children, which inherit it with PATH, do not install another.
const CodexGuardEnv = "GUILDHALL_TEST_CODEX_GUARD"

// CodexGuardExit is the exit status of the codex guard.
const CodexGuardExit = 97

// CodexGuardMessage starts the line the codex guard writes to standard error, followed by its arguments.
const CodexGuardMessage = "guildhall test: real codex invoked:"

// InstallCodexGuard makes `codex` on PATH a guard that writes CodexGuardMessage and its arguments to standard error
// and exits CodexGuardExit, as InstallClaudeGuard does for claude, so a test that reaches for a real Codex CLI fails
// loudly instead of making a model call. A stub a test prepends to PATH later still wins; a process that inherited
// an installed guard (CodexGuardEnv) installs nothing and its remove does nothing. See docs/contracts/testenv.md.
func InstallCodexGuard() (remove func(), err error) {
	return guard{program: "codex", env: CodexGuardEnv, message: CodexGuardMessage, exit: CodexGuardExit}.install()
}
