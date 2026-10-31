---
status: accepted
date: 2026-10-30
topic: cli
amends: director-status-feed.md, director-reload-and-notes.md, task-grants.md
---

# A command finds its copy by `--copy` or by a copy marker above the current directory, and no environment variable selects a copy

## Context

The Guildmaster restarts a Director by stopping it and typing `guildhall director start` in the Director's Herdr tab.
That needed `--copy <dir>` every time, because the only other way to name a copy was `GUILDHALL_COPY`, which the session set for its own children and a Guildmaster's shell never has.
The variable also leaked: attempt launchers start with the supervisor's environment, so an Artisan launched from a Director session inherited it.
The tab's shell already runs in the copy directory, so the current directory can say which copy is meant.

## Decision

- A copy is marked by `.guildhall-copy` at its root, one JSON object naming its schema, version and copy name ([the copy marker](../running.md#the-copy-marker)).
  A command run with `--copy <dir>` writes it exclusively when missing and never rewrites it; an unwritable copy gets one warning and the command goes on; an unreadable or unknown marker is an error naming the file, not skipped.
- Precedence ([copies](../running.md#copies)): `--copy` as before; then explicit `--store`, `--config` or `--state`, which bypass discovery so a child given paths keeps them; then copy discovery, walking up from the cleaned absolute current directory to the first directory holding the marker.
  With none, every command prints one line, `no Guildhall copy found from <cwd>; pass --copy <dir> or cd into a copy`, with its old missing-copy exit status.
- A start directory inside the found copy's own `state/` finds no copy and the search stops, so an Artisan working directory never reaches the Director's store with a bare `guildhall` command; it compares with that copy's `state/` as `samePath` does, not with any directory named `state`.
- `GUILDHALL_COPY` is gone, set and read; a value left in a shell is ignored.
  The Director's session directory lies under its copy (`<copy>/director/claude`), a rule of the adapter contract the conformance kits assert, so the session's bare commands find the copy.
- The Claude hooks and status line name the copy as `--copy '<copy>'`, quoted for a POSIX shell, reversing [handoff and reload](director-reload-and-notes.md)'s reason for naming no path: a failed `turn-ended` or `wait-wake` stalls the supervisor or stops wakes, so they must not depend on the directory a hook runs in once the Director has `cd`'d into a repository.
- [The status feed](director-status-feed.md)'s `needs_me` commands stay written without `--copy`, and `director status --json` prints the absolute `copy`; the [Artisan allow rules](task-grants.md) live in the config of the copy `--copy` names or discovery finds.
- Other users are redone: Lanterns reads its Calm preference path from a rendered `calm.json`; `messages send` tells this copy's Director from another's by the copy discovery finds, `unknown` under `state/`; the Claude adapter no longer sets the variable for Artisans, so validate records `guildhall ci log` with `--copy '<copy>'`.

## Alternatives rejected

- Guessing a copy from files such as `config.toml`: repositories hold them too, so a marker makes discovery an explicit claim.
- Skipping a broken marker to search further up: a broken marker means a copy was meant there.
- Skipping every directory named `state`: only the found copy's own `state/` is the Artisans'.

## Consequences

The Director's skills say bare commands work because the session runs inside its copy, and use `git -C` rather than `cd` for repositories.
`store init <path>` with no `--copy` writes no marker, since it cannot tell it is making a copy.
A bare restart typed in the tab recorded no Herdr pane, an accepted limitation; a later change adopts the pane from Herdr's environment instead ([the Director's tab](director-tab.md)).
