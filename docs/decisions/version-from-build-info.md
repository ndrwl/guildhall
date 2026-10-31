---
status: accepted
date: 2026-10-28
topic: release
tasks: d21
amends: release-builds.md
---

# `guildhall version` reads its revision from Go build info, and the Director chat shows it first

## Context

`guildhall version` printed one line, such as `guildhall v0.0.0-20261027210024-abd974855ac9 linux/amd64 go1.27.2`, which did not say which merges a binary includes.
The Guildmaster installs and restarts after batches of merges and wanted to see, in the Director chat at every session start, which revision the binary is, which pull request it ends at and how far it is behind main.

## Decision

The revision, its commit time and the modified flag come from the `vcs.*` settings Go stamps into every `go build` or `go install` from a git checkout, read with `debug.ReadBuildInfo`; the release workflow's injected tag still wins in `Version()` ([release builds](release-builds.md)).
A module-proxy build at a pseudo-version takes the abbreviated revision and commit time from that version with a small local pattern; any other build without `vcs.*` shows the release only.
`vcs.modified` is shown as Go reports it, which on Go 1.27 includes untracked files, and is worded "uncommitted or untracked files".
The repository is the copy's one repository whose `remote` names the module path; the pull request comes from the squash subject's ` (#N)`, and the task from the copy's own `pull-request.json` delivery documents.
Behind main comes from a bounded fetch (five seconds per git run) into the private ref `refs/guildhall/version/main`, recorded in `state/version-fetch.json`; `--json` and `director status` never fetch.
A Claude Code Director session's one launch message is `/version startup "<startup.md path>"`, an adapter-written `.claude/commands/version.md` that runs `guildhall version --startup`, replies with its output and then follows `startup.md`, in one turn.
`--startup` warns when the binary differs from the one the session's supervisor recorded; warnings never block startup or change the exit code.
Behaviour detail is in the [contract](../contracts/cli.md#version).

## Alternatives rejected

- Linker flags, a build script or a Makefile: build info needs none, so a plain `go install ./cmd/guildhall` keeps working, and keeping the injected tag first leaves `doctor` and the session record's `release` unchanged.
- `golang.org/x/mod` to parse pseudo-versions: a dependency for three fixed forms.
- A config key marking Guildhall's own repository: matching the module path needs none; no match or several give no repository fields.
- Naming the task from the pull request body: GitHub's squash message drops it; a `gh pr view` fallback is left for later.
- A release skill for `/version`: it would be named `/guildhall-version`, so the command is an adapter file.
- A `` !`…` `` pre-execution line: whether Claude Code substitutes arguments inside it could not be checked in the build, so the command asks for one Bash call, allowed by `allowed-tools: Bash(guildhall version:*)`, at the cost of one more tool call.

## Consequences

The status feed the Director runs at every turn stays local, and its `as_of` says how old the behind-main count is.
Pull requests this copy did not deliver show no task.
The `/version` the session runs is the `guildhall` on `PATH`, which may be newer than the supervisor after an install without a restart, hence the warning.
The fake runtime and runtimes that start in the session directory keep `render.StartupMessage`.
