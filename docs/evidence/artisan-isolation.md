# Artisan sessions stop inheriting user and project settings, hooks, MCP servers and skills (d191, W2 of d170)

Source: [token usage audit](../audits/token-usage-2026-11.md) section 7 row W2 and section 9 item 2.

## Change

- `ClaudeArgs` adds `--setting-sources "" --strict-mcp-config --mcp-config '{"mcpServers":{}}'` after `--settings <attempt dir>/claude-settings.json --add-dir <model dir>`.
  Claude Code 2.1.318 accepts the empty `--setting-sources` value (live run below), so the spec's value stands.
- The generated settings (`ArtisanSettings`, and so the launch settings and the Director preflight rendering, `ArtisanPolicySettings`) keep `permissions.allow` and the steering PostToolUse hook (`guildhall messages hook`, added on main by the steering work after the spec), and add `"autoMemoryEnabled": false`.
- **Two departures from the spec, both forced by what the rebase and the live run showed:**
  - The spec's `"disableAllHooks": true` is **not** set. Since the spec was written, main's generated settings carry the steering hook, and `disableAllHooks` would switch it off too. User, project and local hooks stay out because their settings files no longer load: the live run shows no hook events after the change. Only managed-policy hooks could still run.
  - `"autoMemoryEnabled": false` is **added**. The live run showed that, with user settings no longer loading, Claude Code turns auto memory on for the Artisan. Its init event gains `memory_paths.auto` (`~/.claude/projects/<cwd>/memory/`), the first-call prompt grows by about 675 tokens, and the Artisan would read and could write a memory directory outside its worktree. This machine's user settings evidently keep auto memory off today, since the "before" run has no `memory_paths`. The key keeps Artisans as they were.
- `ENABLE_CLAUDEAI_MCP_SERVERS=false` is **not** added (spec step 3): the one live run where the claude.ai "Claude Docs" connector connected in the "before" session shows `--strict-mcp-config` drops it ("after" lists no MCP server and no `mcp__` tool).
- `MinimumPolicyVersion` stays at 2.1.310: every flag and key is accepted by 2.1.318, the machine's version, and none is known to be newer than the floor.

## Live runs

All three ran on 2026-11-04, Claude Code 2.1.318, from the d191 build-3 attempt, using the `-claude-live` and `-claude-program` test flags (`-claude-live-grant` for the configured mode):

```sh
go test ./internal/adapter/claude -run TestLiveClaudeIsolation -count=1 -v -timeout 10m -claude-live -claude-program "$(command -v claude)"
go test ./internal/adapter/claude -run TestLiveClaudeBuildPermissions -count=1 -v -timeout 15m -claude-live -claude-live-grant configured -claude-program "$(command -v claude)"
go test ./internal/adapter/claude -run TestLiveClaudeBuildPermissions -count=1 -v -timeout 15m -claude-live -claude-program "$(command -v claude)"
```

`TestLiveClaudeIsolation` runs the same trivial print session twice in an empty directory holding a canary project `CLAUDE.md`: "before" is `ClaudeArgs` without the three new flags (as an Artisan launched before d191, with today's `--disallowedTools`), "after" is `ClaudeArgs` as shipped.
It passes `--include-hook-events`, so a hook that runs shows up as an event.
The test binary's guard rewrites `PATH`, so in "before" the user's three SessionStart hooks ran but failed with `not found`. Their real output, 13.4 KB persisted with a 2 KB preview in a normal Artisan, was not in the measured prompt, so the measured delta understates the saving.

| Item | Before | After |
|---|---|---|
| `--setting-sources ""` accepted | n/a | yes |
| SessionStart hook runs | 3 (`chrome-devtools-axi`, `gh-axi`, `lavish-axi`; 6 hook events) | none (0 hook events) |
| MCP servers | `claude.ai Claude Docs` (source `claudeai`, 8 `mcp__claude_ai_Claude_Docs__*` tools) in the first run; not connected in later runs | none, no `mcp__` tool |
| Skills | `axi`, `no-mistakes`, `dataviz`, `doctor`, `claude-api`, `run` | `dataviz`, `doctor`, `claude-api`, `run` (bundled only) |
| Agents | `claude`, `Explore`, `general-purpose`, `Plan`, `statusline-setup`, `web-fetch` | the same: all are built in to 2.1.318, and `~/.claude/agents` holds none of them |
| Project `CLAUDE.md` (canary) | loaded (reply ended with the canary in 3 of 4 runs) | not loaded (0 of 4) |
| Auto memory (`memory_paths` in init) | off | off with `autoMemoryEnabled: false`; **on** without it |
| First-call prompt tokens (input + cache creation + cache read) | 13,083 with the connector; 11,541 to 11,544 without | 10,928 |

The measured first-call prompt deltas, before minus after:

- with the claude.ai connector connected in "before", and before `autoMemoryEnabled` was added: 13,083 - 11,605 = **1,478**;
- without the connector, with `autoMemoryEnabled: false`: 11,541 - 10,928 = **613**;
- without the connector and without `autoMemoryEnabled`: 11,544 - 11,605 = **-61**, because auto memory's prompt cost more than the dropped user skills saved.

So with the connector and auto memory off, the after prompt is about 13,083 - 10,928 = 2,155 tokens smaller, before the hook output a real session would also drop. That is in line with the audit's 2.25k estimate against its 15,241 short-attempt median.

`TestLiveClaudeBuildPermissions`:

- `configured` (the builder's shipped default, `SuggestedGoAllowedTools` and `Bash(sh check.sh)`): **passes**. The check ran and the commit was made with only the generated grants. One earlier run failed only on the test's "no denial at all" condition: the model chose a heredoc `cat > draft.json <<'EOF' ... && cd ... && guildhall result submit` compound command, and Claude Code's built-in "expansion obfuscation" check refused it. The check and the commit had already succeeded, and no grant was missing. The rerun passed.
- no grant: **passes**. `sh check.sh` is denied and does not run, so the machine's own settings no longer allow it.

In both modes `guildhall` was not on the test's `PATH`, so the model wrote `result.json` directly, as its instruction allows.

## User-level rules the change drops

`~/.claude/settings.json` still could not be read from the build session, so its allow and deny rules are not listed here.
The configured live run shows the generated grants alone are enough for the build stage's own check-and-commit loop.
Before merging, the Guildmaster compares the user file's `permissions.allow` with the `artisans` line of `guildhall director start --check`.
For each user rule the effective grants do not cover, either judge it unneeded or add it as a repository grant in the copy's `config.toml` (the Guildmaster's file, not this PR).
For this Go repository the likely gaps are toolchain commands outside `SuggestedGoAllowedTools` (`go test`, `go vet`), such as `go build` and `gofmt`.
Any user deny rule listed there stops applying to Artisans (Q1, option a): only managed-policy denies and the narrow allow list remain. A deny rule that matters becomes a follow-up task.
