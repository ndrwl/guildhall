---
status: accepted
date: 2026-11-03
topic: steward
tasks: d96, d124
amends: steward-fixed-remote-commands.md
---

# Steward collection adds three fixed commands and marks items through an scp receipt

## Context

Directors file lessons and maintenance incidents locally ([director modes](../director-modes.md#lessons), [messaging](../messaging.md#maintenance-tickets-to-the-steward)).
d96 Slice 3 has the Steward collect them from enrolled copies into the private config for the Guildmaster's triage, without any Director reaching the Steward or the private repository (R1.8) and without widening the [fixed remote commands](steward-fixed-remote-commands.md) beyond their one-variable shape.

## Decision

The allow-list gains three entries of the existing shape, `<guildhall> <command> --copy <copy_dir>`, with the copy directory the only variable:

- `lesson list --json --pending --copy` and `incident list --json --pending --copy`, which only read;
- `director collected --copy`, which marks items collected.

Free text never goes on a command line.
Before `director collected`, the Steward copies a receipt to the fixed path `state/steward/collect-receipt.json` in the copy with `scp`; it lists each item's kind, origin, id, content digest and the private-config commit holding it.
`director collected` marks exactly the items whose origin and digest are the copy's, reports every other item and leaves it pending, and deletes the file.
`CheckCommand` is unchanged, so the Windows restricted character set holds, and rollout plan digests do not change, since plans record only `config reload`.

A collection takes the private config's `.rollout.lock`, so a rollout and a collection never run at once, and commits only its own `lessons/` and `incidents/` files with a private index, never sweeping in the Guildmaster's uncommitted edits.

The scheduled round, `[steward] collect_every`, wakes the Steward through its inbox: per the d124 accept, the Steward inbox's sender list gains the `director` tag, which only the Steward's own supervisor sends with for the round's `triage-opened` report; `messages send` to the Steward stays the Guildmaster's only.

## Alternatives rejected

- A typed `director collected --receipt sha256:<hex>` argument: it would widen the argument rule of every remote command for one command.
- A wake through the session waiter with no inbox message: the Guildmaster chose a director sender tag so the report is a durable, readable message.
- Committing with `git add` and `git commit` in the checkout: it would sweep in staged edits to `roster.toml` and `managed.toml`.

## Consequences

A Director copy whose `guildhall` predates these commands is reported per copy and collected once it is upgraded.
The receipt directory is created by the pending JSON lists, since `scp` creates no directory.
An item stays pending on its Director until a mark succeeds, and an item already committed with the same digest only has its mark sent again, which makes collection idempotent.
