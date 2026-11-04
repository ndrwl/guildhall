# `internal/adapter/codex/codextest`

## Contract

A stand-in Codex CLI with no model calls: `Install` puts the test binary on `PATH` as `codex` for a test and its children, names it in `interp.CodexStubEnv` so the interpreter's test-binary guard admits the codex adapter, and points `CODEX_HOME` at an empty directory so no user configuration is read; `RunIfStub`, first in `TestMain`, makes a binary started under that name act as the stub and exit.
It answers `--version` with `Version`, `codex-cli 0.163.0`, or `VersionEnv`'s line; `login status` with `Logged in using ChatGPT`, or `Not logged in` and exit 1 under `LoggedOutEnv`; and `exec --help` with the flags codex-cli lists, less those `HelpDropEnv` names.
`exec` refuses (exit 2) a command line without `--json`, `--ephemeral`, `--skip-git-repo-check`, `--sandbox read-only`, `codex.Switches` in order, `-C` naming its working directory, an `--output-schema` that is not a JSON file, or an instruction without its `Prompt file:` line.
It records its arguments as `ArgsFile` beside the prompt, which `RecordedArgs` reads back, and plays the prompt's `codex-stub: <behaviour>` line (`Prompt` writes one; none means succeed) as a `--json` stream shaped as codex-cli 0.163.0's: an agent message, a command that exits 0 and one that exits 1, then the final message, then a completed turn with `StreamUsage`, which the wrapper records as `Usage`.
A succeeded answer judges each pending criterion of `inputs/self-review.json` beside the prompt, a score 2 and a gate a pass; failed returns a failed message with a null answer; wrong-identity adds a criterion that self-review does not have pending; malformed returns a cut-off message; no-result returns no agent message at all; exit-error exits 1 after a succeeded answer; hang works until interrupted, its parent exits or `HangLimit`.
Its own behaviours are `TurnFailed`, an error event and a failed turn with `QuotaMessage` and exit 1, `NoUsage`, a completed turn without usage, `NoAnswer`, a succeeded message with a null answer, and `Fenced`, a succeeded message in a ```` ```json ```` fence.
`Attempt` lays out a judge attempt as `quality.Judge` does, for task-1's `quality-judge-1`: the prompt in `prompt/`, `SelfReview` in `prompt/inputs/self-review.json`, an empty `work/` and the result file beside them.

## Tests

It is test support exercised by the Codex adapter's conformance run and wrapper tests.
