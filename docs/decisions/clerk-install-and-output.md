# Clerk ships with the Director and echoes fixed output

## Decision

Ship [Clerk](../clerk.md), the Guildmaster's read-only interaction plugin, as its own embedded plugin in `internal/clerk`, separate from [Lanterns](lanterns-and-core-context.md).
It is installed exactly as Lanterns is: the Claude adapter's `SessionFiles` writes it into the Director's stable workspace, at `<copy>/director/claude/.claude/plugins/clerk`, on every `director start`, and the Director's session loads it with a second `--plugin-dir` after Lanterns'.
The Guildmaster's own sessions load the same directory with `claude --plugin-dir`; there is no `guildhall clerk install`, no `<copy>/clerk/` directory and no write into the copy root.

Each slash command is a markdown command in the `/version` style: it may run only its own `guildhall clerk <name>` subcommand, runs it once with the command's arguments, and has the model reply with the output verbatim.
All formatting lives in Go, in `guildhall clerk`, so the command files hold no logic and an upgrade changes behaviour through the binary alone.

## Why

The grill settled that Clerk is shipped the way Lanterns is.
Lanterns reaches only the Director's workspace, so the plan's options were project commands in the copy root, an installer, or the Director's workspace; the Guildmaster chose the Director's workspace for both sessions, which needs no new write path and is refreshed from the running binary on every start, so command files never go stale after an upgrade.
The cost is that a fresh copy has no Clerk directory until its Director has been started once with this release; [the user doc](../clerk.md#loading-it) says so.

The Guildmaster accepted one model turn per command that echoes the output verbatim, in place of zero model tokens.
The zero-token routes are not pursued: function hooks are early access and off unless `CLAUDE_CODE_ENABLE_FUNCTION_HOOKS=1` is set, so Clerk would silently not exist for most sessions, and a `UserPromptSubmit` hook that blocks with the formatted text as its reason is unproven, both in how Claude Code shows a block and whether an unknown `/guild` reaches the hook.
Either can replace the echo later without changing `guildhall clerk`.

## Consequences

- Clerk is strictly read-only: it opens the store read-only, writes no receipt, never fetches and never writes the copy marker, so `--copy` of an unmarked directory is refused; the one file it may create is an `attempt.lock`, as `director status` does.
- Checks are local only, from the store; no forge or network call is made.
- `claude --help` confirms `--plugin-dir` is repeatable, so the Director's session takes both plugins in one launch.
- Everything else in the Director's session, its settings, hooks, permissions, `/version`, Lanterns and `calm.json`, is unchanged.
