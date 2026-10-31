---
status: accepted
date: 2026-10-31
topic: director
tasks: d97
---

# Each Director backs up its own copy, as a store export and its copy files, to its own branch of a private repository

## Context

A lost machine lost the guild's records: a Director copy's store, its notes in the store, its task documents and its attempt files existed only on that machine.
Machines planned are a WSL laptop, the same laptop's Windows side, a Linux VDS and perhaps another desktop.
No Director feature may require a Steward ([optional Steward](optional-steward.md)), and the binary bbolt store is neither diffable nor safe to copy while a writer holds it.
The Guildmaster settled the scope in d96's and d97's grills: back up the copy's own state, including attempt files and their Artisan transcripts, but not Claude Code's own session logs in `~/.claude/projects`, which are large, grow daily and may hold secrets from tool output.

## Decision

Each Director copy backs itself up, standalone or managed, with no Steward ([backups](../running.md#backups)).
The `[backup]` table names one private repository, such as `ndrwl/guildhall-backups`, and the copy's own branch `<machine>/<director>`; it is machine scope and outside the task-bound config digest, so adding it changes no open task's digest, and with no table nothing runs.

- **What:** every regular file in the copy minus a fixed exclusion list (worktrees, CI log caches, the binary store, the backup repository, the run stamp, lock files and non-regular files), so a new kind of copy file is backed up by default; and the store as `guildhall store export`'s deterministic text, one file per top-level bucket and per repository, raw bytes kept, every bucket sequence recorded, files over 32 MiB split into parts at line boundaries.
- **How:** the export is one read-only transaction waiting at most `lock_wait`; every document is checked against its sha256 before anything is committed; files are staged into a bare repository at `<copy>/backup.git` through a private index, so git's stat cache skips unchanged files and nothing is checked out; a commit is made only when the tree changed.
- **Push:** `<commit>:refs/heads/<branch>` with no `+`, so git refuses a branch that does not fast-forward; Guildhall never force-pushes and never deletes a branch, and the repository's ruleset blocks both for every client. A failed push keeps the commit for the next run.
- **When:** the session supervisor backs up at session start and every `every` (default an hour), off the decision path with no model spend and its push bounded at 10 minutes; `guildhall backup now` runs one by hand with no bound.
- **Retention:** git history; the branch tip mirrors the copy, and a pruned file stays in history.
- **Restore:** `guildhall backup restore --copy <dir> <branch|commit>` writes a stopped copy's files and rebuilds its store with `store import`, which only creates a fresh store, restores bucket sequences and ends with `Doctor`.
- **Credentials:** one SSH deploy key per Director, named by an SSH host alias, so the config holds none; keys are installed by hand until d130 distributes them.

A failed backup is a `backup` item in `director status`'s `needs_me`, read from the run stamp, because a store message must name a task and a backup belongs to none.

## Alternatives rejected

- The Steward backing up every copy: it would make a Director feature depend on a Steward.
- Committing the binary `director.store`: not diffable, grows each commit by the whole file, and is unsafe to copy under a writer.
- Re-encoding records through current structs in the export: it would drop or reorder fields of older and retired records and break mutation id replay.
- A checked-out working tree under the copy: it doubles the copy's disk use for no gain over plumbing commits.
- Byte-chunking every large file: only store export files can grow past 32 MiB; a larger copy file is left out and named instead.
- Backing up `~/.claude/projects`: outside the copy, about 2.2 GB and growing, possibly holding secrets, and not needed to restore.
- Encryption and pruning of the backup repository: not needed for a private repository; history is the retention.

## Consequences

- A copy with a `[backup]` table gets a `backup.git` and a `state/backup-last.json`; a copy without one behaves exactly as before, with the same config digest.
- Hourly backups run only while a session supervisor runs; `backup now` covers a copy with no session, whose store does not change then either.
- A restored copy continues its branch's history, and a new machine's first backup to an existing branch adopts it.
- Backing up `steward.store`, `rollouts/` and the Steward's config checkout is a follow-up.
