# `internal/adapter/claude`

## Contract

The minimal Claude Code runtime ([Claude adapter](../adapters.md#claude-adapter)): no optional capabilities; `Adapter.Command` is `guildhall claude-artisan` for a valid spec in its working directory and environment, refusing an invalid spec or an unset executable with no command.
`Run` is the wrapper: it refuses a cancelled attempt (`adapter.ErrAttemptCancelled`) or an existing result (`adapter.ErrResultExists`) without starting Claude, removes a stale draft, runs `claude` (`PATH`, or an absolute `--claude`) with `ClaudeArgs` and no standard input, and once Claude exits publishes the draft's bytes at the result file by hard link, never replacing one and never after the cancel marker; no draft is `ErrNoDraft`.
The cancel marker, an interrupt or the end of its context stops Claude (interrupt on Unix, termination on Windows, a kill after `StopGrace`) and publishes nothing; the marker is not an error.
`ClaudeArgs` is print mode with `Instruction`, `PermissionMode` (`acceptEdits`) and the prompt and result directories added; `Instruction` names the prompt file and the draft (`DraftFile`) on their own lines and gives the result object for the attempt's identity.
`ParseArgs` and `Main` follow the fake's shape: exit 0, 1 on failure, 2 on refused arguments.

## Tests

`claude_test.go` was written red first against stub bodies ([red run](../evidence/claude-adapter-2026-10-21-red.txt)), every test failing: `adaptertest.RunConformance` as a direct child, on interpreter-rendered attempts and inside the Artisan launcher on this platform's unit, where a stop must end at rung 1; the command and argument parsing; `ClaudeArgs` and `Instruction`; refusals that start nothing; a stale draft never published; and a stop on the context's end.
All of them run the wrapper over [`claudetest`](adapter-claude-claudetest.md)'s stub; `TestLiveClaude` runs a real session only with `GUILDHALL_CLAUDE_LIVE=1`.
`TestArtisanStartRunsClaudeOwned` in `internal/cli` runs `artisan start --adapter claude` over the stub.
