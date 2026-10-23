# Director startup admission and message boundary

## Decision

Always terminate Claude Code session options with `--` before the first message, including when a model is configured.
Claude Code's `--add-dir` is variadic, so an unseparated first message can be consumed as a directory when no later option terminates it.
The interactive Claude stub requires this delimiter and refuses the previous shape with exit 2, with or without a model.
The print-mode Artisan argument contract is unchanged.

Require `guildhall` on `PATH` during Director preflight, after the runtime check and before opening the store or installing session material.
Session hooks, the status line and Director commands use that bare name even when the supervisor was started by absolute path.
Use the existing `App.LookPath` process boundary and refuse with a `guildhall` check line and advice to add the executable's directory to `PATH`.
This checks availability, not that the resolved executable is the same release as the supervisor.
No additional process abstraction is needed.

## Evidence and limits

Before implementation, `TestSessionCommand` failed for the missing delimiter both with and without a model.
`TestSessionStubRequiresMessageSeparator` failed because the stub accepted the old shape and rejected the delimited shape in both cases.
`TestDirectorStartRequiresGuildhallOnPATH` failed because start and `--check` succeeded without the bare executable; start also wrote the store and session directory.
All pass after the fixes.
The existing shared session contract exercises the corrected adapter over a real stub subprocess with no model configured.
`TestDirectorStartClaudeWithoutModel`, added after implementation, passed on its first run and pins the complete startup turn reaching `director ready` over the stub.
These tests make no model calls and do not claim live runtime acceptance.

The empty-cgroup recovery issue after a superseded session remains a separate follow-up.
