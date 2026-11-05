---
status: accepted
date: 2026-11-05
topic: delivery
tasks: d219, d187
amends: multi-pr-tasks.md
---

# A one-PR task whose change another merged pull request shipped closes done through `slice delivered`

## Context

`slice delivered` closed only a slice of a multi-PR task ([multi-PR tasks](multi-pr-tasks.md)), so a one-PR task whose build found its change already merged could only be cancelled.
guildhall/d187 was cancelled after guildhall/d206 (PR #351) shipped its change, which records finished work as abandoned.

## Decision

A one-PR task is slice 1 of 1, and `slice delivered --slice 1 --by-pr <url>` closes it `done` ([a slice already delivered](../tasks.md#a-slice-already-delivered)).
It is the same event, check (a pull request of the task's repository the forge reports merged), record (`delivered-1-delivery`), `slice-delivered` message and dependency release as a multi-PR task's delivered last slice, with one-PR wording, and it counts toward no hallmark.
`--by-slice` is refused for a one-PR task with an error naming `--by-pr`.
`director status` shows the done task's `delivered_by`, so a delivered close is told apart from a merge without reading documents.
Cancel stays for abandoned work.

## Alternatives rejected

- A new `task delivered` command: a second route and message type for the same check, record and close.
- Leaving cancel as the only way out: it records shipped work as abandoned and tells dependants it can never merge.

## Consequences

A forge with no pull request reader cannot check the evidence, so there a one-PR task still has only cancel.
Every reader of the delivery record handles slice 1 of a one-PR task as well as a slice; status reads a done one-PR task's documents once more to find it.
