---
status: accepted
date: 2026-10-22
topic: director
amended-by: copy-discovery.md
formerly: D65, 0065-director-status-feed.md
---

# The Director's status feed is one store read plus files, with `needs_me` in a fixed order

## Context

The Guildmaster asked to monitor the Director and to ask it "what needs me"; the Director needs the same answer at every turn and when a session starts after a crash.
Answering took `task list`, a `task timeline` per task, `decide list`, `messages list` and an `artisan status` per running attempt, each opening the store separately, so the answer could mix store states.

## Decision

`guildhall director status --json` answers in one document ([status](../director.md#status)).
It reads the store once through `store.DirectorView`, one read transaction over every task, attempt, unreleased owner, open decision, unread Director message and task receipt, labelled with the newest sequence as `task timeline` is.
Only then does it read files: each task's `next.json` and each running attempt's directory, which it classifies from the ownership record already read, with the same classifier `artisan status` uses after its own store read, so the two cannot drift.
The store stays read-only and the read waits for a writer's lock, because the Director runs status while launchers and advances write.

`next.json` is copied as JSON exactly as written, because the watcher that writes it owns its shape; the feed interprets only `wait`.
A file that is not JSON is reported beside its task, so one damaged file does not hide every other task.

`needs_me` gives each item the command the Director runs next, in a fixed order, so the same store state always lists the same items in the same order; the order and kinds are kept on [the status page](../director.md#status).
`recent` drops each receipt's request digest, which only replay checks need and which, for an owned attempt, depends on the launcher's process identity.

## Alternatives rejected

- Decoding `next.json` into a type: the watcher owns its shape.
- Failing the feed on one unreadable file: one damaged file would hide every other task.
- One item per unread message: the retry and decision items already cover the `attempt-failed` and `decision` messages the engine sends, so the others are one counted item.

## Consequences

The feed's cost grows with the store's mutation count, as `task timeline`'s does.
Its session and mode sections were present from the start, reporting `no session` and `standalone`, so consumers gained detail later without a format change.
`needs_me` commands are written without `--copy`; they first relied on the session's `GUILDHALL_COPY`, and since [copy discovery](copy-discovery.md) on the session running inside its copy.
