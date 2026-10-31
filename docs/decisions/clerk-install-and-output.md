---
status: accepted
date: 2026-10-31
topic: herdr
tasks: d34
---

# Clerk ships with the Director as its own embedded plugin and echoes fixed output

## Context

The grill settled that [Clerk](../clerk.md), the Guildmaster's read-only interaction plugin, is shipped the way [Lanterns](lanterns-visuals.md) is.
Lanterns reaches only the Director's workspace, so the plan's options were project commands in the copy root, an installer, or the Director's workspace.

## Decision

Clerk is its own embedded plugin in `internal/clerk`, separate from Lanterns.
It is installed exactly as Lanterns is: the Claude adapter's `SessionFiles` writes it into the Director's stable workspace, at `<copy>/director/claude/.claude/plugins/clerk`, on every `director start`, and the Director's session loads it with a second `--plugin-dir` after Lanterns'.
The Guildmaster's own sessions load the same directory with `claude --plugin-dir`; there is no `guildhall clerk install`, no `<copy>/clerk/` directory and no write into the copy root.

Each slash command is a markdown command in the `/version` style: it may run only its own `guildhall clerk <name>` subcommand, runs it once with the command's arguments, and has the model reply with the output verbatim.
All formatting lives in Go, in `guildhall clerk`, so the command files hold no logic and an upgrade changes behaviour through the binary alone.

## Alternatives rejected

- Project commands in the copy root, or an installer: the Director's workspace for both sessions needs no new write path and is refreshed from the running binary on every start, so command files never go stale after an upgrade.
- Zero model tokens through function hooks: they are early access and off unless `CLAUDE_CODE_ENABLE_FUNCTION_HOOKS=1` is set, so Clerk would silently not exist for most sessions.
- Zero model tokens through a `UserPromptSubmit` hook that blocks with the formatted text as its reason: unproven, both in how Claude Code shows a block and whether an unknown `/guild` reaches the hook.

## Consequences

A fresh copy has no Clerk directory until its Director has been started once with this release; [the user doc](../clerk.md#loading-it) says so.
Each command costs one model turn that echoes the output; either zero-token route can replace the echo later without changing `guildhall clerk`.
Clerk is strictly read-only: it opens the store read-only, writes no receipt, never fetches and never writes the copy marker, so `--copy` of an unmarked directory is refused; the one file it may create is an `attempt.lock`, as `director status` does.
Checks are local only, from the store; no forge or network call is made.
`claude --help` confirms `--plugin-dir` is repeatable, so the Director's session takes both plugins in one launch.
Everything else in the Director's session, its settings, hooks, permissions, `/version`, Lanterns and `calm.json`, is unchanged.
