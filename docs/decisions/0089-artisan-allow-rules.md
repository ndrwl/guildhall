# D89. Each Director grants its Claude Artisans narrow tool permissions per profile

The Guildmaster accepted that Guildhall should pass narrow permissions to the Claude builder from a per-profile config setting, empty by default, with a documented suggested set, and added that the permission set should vary by Director.
[D93](0093-shipped-default-grants.md) amends "empty by default": a well-known claude profile that sets no list gets a shipped default grant, and a repository can add to it; every other guarantee here holds.
This supersedes, for this narrow case, [D55](0055-claude-adapter-wrapper.md)'s assignment of widening the Artisan's permissions to the Director's managed settings: a standalone Director can now grant them before managed mode exists.

## The setting

A profile's `allowed_tools` ([profiles](../configuration.md#profiles)) lists Claude Code permission rules, such as `Bash(go test:*)`, that the stage's print-mode session is granted beyond `acceptEdits`.
It is empty by default, and empty grants nothing through Guildhall; Claude Code's own settings still apply, as described under [Artisan permissions](../adapters.md#artisan-permissions).
`config.Parse` refuses a rule that is not a tool name with an optional parenthesised specifier, a duplicate, and a rule allowing every shell command (`Bash`, `Bash(*)`, `Bash(:*)`), so a grant always names its commands.
The suggested set for a Go builder, `claude.SuggestedGoAllowedTools`, is `go test`, `go vet`, `git add` and `git commit`, plus `git status` and `git diff`, deliberately included as read-only helpers ([Artisan permissions](../adapters.md#artisan-permissions)).

## Whose setting it is

The rules belong to the Director: they live in the `config.toml` of the copy `--copy` or `GUILDHALL_COPY` selects, so two Directors on one machine grant different sets.
They are never read from a machine-wide or shared file, and `managed.toml` holds only key and value settings, so a Steward cannot distribute them; a Steward-managed grant is later work, if wanted, under managed mode.
The Director's own session may not edit its copy's `config.toml`.
Its [session settings](../director.md#start) also deny direct calls to `guildhall claude-artisan`, `guildhall fake-artisan`, `guildhall fake-director` and `guildhall artisan start`, with or without arguments.
These denies override the broad `guildhall` allow, so the Director cannot invoke launch plumbing with another copy or profile; legitimate orchestration commands such as `task start`, `step done` and `artisan stop/status/resume` remain allowed.

This is a Director command-policy boundary, not an OS sandbox: processes running as the same OS user are not isolated from each other.
The claim is that a Director cannot widen its builders' grant through Guildhall's internal launch commands under these session settings, not that it is sandboxed from another local Director.
The trusted launcher still invokes the wrappers on the Director's behalf, and the wrappers load the supplied copy/profile reference without a new caller-identity mechanism.
The `director start` preflight's `artisans` line names the Director, its config file and each claude profile's grant, so what is granted, and by whom, is visible before any work starts.

## How it reaches the session

Grants travel by reference only: the interpreter puts the Director copy and profile name on `interp.Spec`, mapped unchanged to `adapter.Spec`; `launch.Process`, `artisan start` and the Claude adapter forward `--copy` and `--profile` to `guildhall claude-artisan`.
The wrapper calls `config.Load` on that copy's `config.toml`, refuses an undeclared profile and writes exactly its `allowed_tools` as `permissions.allow` in a settings file passed with `--settings`.
Without both copy and profile it grants nothing.
Neither public entry path accepts `--allow-tool`, and no argv or environment rule list adds a grant.
A settings file, rather than `--allowedTools`, keeps the rules off Claude Code's command line, unaffected by Claude Code's comma and space splitting or by a Windows command shim's quoting, and the file is in the attempt directory, outside every directory Claude may write, so the session cannot change its own grant.
The rules widen Claude Code's own settings and never narrow them: a machine's or repository's settings still apply, and their `deny` rules still win.
Only the claude adapter grants them; the fake loads the referenced profile and refuses one granting any, and the preflight refuses `allowed_tools` on a profile of another adapter rather than silently granting nothing.

CI checks the whole path over the stub Claude, which records the rules its settings file granted; `TestLiveClaudeBuildPermissions` gains a mode, `GUILDHALL_CLAUDE_LIVE_GRANT=configured`, that grants the suggested set to a real session, and has not been run yet.
