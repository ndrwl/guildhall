# `internal/adapter/claude/claudetest`

## Contract

A stand-in Claude Code with no model calls: `Install` puts the test binary on `PATH` as `claude` for a test and its children and names it in `interp.ClaudeStubEnv`, so the interpreter's test-binary guard admits the claude adapter, and `RunIfStub`, first in `TestMain`, makes a binary started under that name act as the stub and exit.
The stub refuses (exit 2) arguments that are not `--print`, an instruction, `--output-format stream-json`, `--verbose` and `--permission-mode acceptEdits`, an instruction without its `Prompt file:` and `Result file:` lines or result object, or files outside the `--add-dir` directories.
Started with the [Director session command](../director.md#start), it is an interactive Director session instead (the stub also accepts that shape without `--settings`): it refuses (exit 2) any other shape, relative paths, a `GUILDHALL_COPY` that is not the added copy, or a session directory without `.claude/settings.json`, records the model and effort as `fake.SettingsFile` in its working directory, then plays the role prompt's `claude-stub:` lines with `fake.RunScript`, which counts its turns as the fake Director's do (`fake.TurnLogEnv`); `Script` writes those lines.
For focused trust tests, `GUILDHALL_TEST_CLAUDE_TRUSTED_DIR` opts into a saved-folder gate: any other process working directory prints the folder-trust question and exits 1 before consuming the role script.
This is a deterministic model of one prior native approval, not a production trust mechanism; existing tests without the variable retain their ordinary stub behavior.
`TestDirectorClaudeReusesTrustedWorkspace` failed at that gate before workspace publication was implemented ([red run](../evidence/director-trust-2026-10-23-red.txt)); the reload test exercises the same gate across a real supervisor handoff/reload.
It reads the prompt's `claude-stub: <behaviour>` line (`Prompt` writes one; none means succeed), an `adaptertest.Behaviour`, and writes that draft, with summary `Summary`: succeeded, failed, for another attempt, malformed, none, succeeded then exit 1, or none while it works until interrupted, its parent exits or `HangLimit`; its own behaviour `Denied` succeeds and reports one denied `Bash` command, `go test ./...`, as the message's `permission_denials`.
In print mode it also takes at most one `--model` and one `--effort`, refusing an effort Claude Code's `--effort` does not take (`claude.Efforts`), and records them as `SettingsFile` in the model directory, the zero `adapter.Settings` when given neither, which `Settings` reads back for a result file.
In print mode it refuses (exit 2) a `--settings` file that is relative, given twice, inside an `--add-dir` directory, unreadable, contains unknown fields, or lacks a non-null `permissions.allow` list; an empty list is valid.
It accepts the skill-policy control fields described in the [policy contract](config-claude-skills.md), without simulating their runtime enforcement, and records the rules it was granted, none without `--settings`, as `GrantedFile` in the model directory, which `Granted` reads back for a result file.
`--version` reports the adapter's tested skill-policy baseline without entering a session.
Whatever it plays, except a hang, it prints `Output` as it exits: a Claude Code result message whose `result` is `Summary` and whose usage and `total_cost_usd` are `Usage`'s fixed numbers.

## Tests

It is test support exercised by the Claude adapter's conformance runs.
The adapter's `TestSessionStubRequiresMessageSeparator` starts the stub directly as a real subprocess with and without `--model`: the delimited message is accepted and the old unseparated shape exits 2 naming `--`.
All four cases failed before the stub was tightened and pass afterward ([decision and evidence](../decisions/director-startup-admission.md)).
