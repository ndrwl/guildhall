---
status: accepted
date: 2026-10-20
topic: store
formerly: D10, 0010-store-single-writer-lock.md
---

# The store's single-writer rule uses bbolt's own file lock

## Context

The store needs one writer per database file across processes, with the lock released by the kernel when the process holding it dies.
The landed gomate `internal/lock` package offered a separate lock file over `github.com/gofrs/flock`.

## Decision

The single-writer rule uses bbolt's own file lock, with no separate lock file or locking package.
A writer takes an exclusive lock on the database file and a read-only open takes a shared lock, each with a bounded wait.
That already gives the contract the store needs: one writer per file, released by the kernel when the process dies.

## Alternatives rejected

- Copying gomate's `internal/lock` over `github.com/gofrs/flock` for a separate lock file: it would add a dependency and a second lock without a behaviour the store needs.

## Consequences

No gomate code is copied and no notice is added.
Later store records rely on this lock rather than adding their own, such as [attempt ownership](store-attempt-ownership.md).
