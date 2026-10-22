# `internal/adapter/claude/claudetest`

## Contract

A stand-in Claude Code with no model calls: `Install` puts the test binary on `PATH` as `claude` for a test and its children and names it in `interp.ClaudeStubEnv`, so the interpreter's test-binary guard admits the claude adapter, and `RunIfStub`, first in `TestMain`, makes a binary started under that name act as the stub and exit.
The stub refuses (exit 2) arguments that are not `--print`, an instruction and `--permission-mode acceptEdits`, an instruction without its `Prompt file:` and `Result file:` lines or result object, or files outside the `--add-dir` directories.
Started as `claude --append-system-prompt-file <role> --add-dir <copy> [--model <m>] <first message>`, it is an interactive Director session instead: it refuses (exit 2) any other shape, relative paths, a `GUILDHALL_COPY` that is not the added copy, or a session directory without `.claude/settings.json`, then plays the role prompt's `claude-stub:` lines with `fake.RunScript`, which counts its turns as the fake Director's do (`fake.TurnLogEnv`); `Script` writes those lines.
It reads the prompt's `claude-stub: <behaviour>` line (`Prompt` writes one; none means succeed), an `adaptertest.Behaviour`, and writes that draft, with summary `Summary`: succeeded, failed, for another attempt, malformed, none, succeeded then exit 1, or none while it works until interrupted, its parent exits or `HangLimit`.

## Tests

It is test support exercised by the Claude adapter's conformance runs; it has no tests of its own.
