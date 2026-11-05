---
status: accepted
date: 2026-11-04
topic: steward
tasks: d129
amends: steward-fixed-remote-commands.md
---

# The Steward reads each enrolled Director's status to report a stuck, unhealthy or out-of-date Director

## Context

The Guildmaster asked for Steward messages "when issues are found" (d104, d129), and the [Steward message](steward-messages-directors.md) record built the first two kinds.
A stopped, crashed or out-of-date Director is the issue a Director cannot report itself, so only the Steward, which reaches every enrolled copy, can see it.
The [allow-list](steward-fixed-remote-commands.md) had no command that reads a Director's session, last turn or release outside an approved [takeover](steward-setup-plans.md), which allowed `director status --json` for its own steps only.

## Decision

The allow-list's read-only fixed command `director status --json --copy <copy_dir>`, the copy's existing status feed, is no longer for a takeover only: the Steward runs it for every enrolled copy, and the feed gains a `health` block: the newest session's supervisor liveness, its runtime's last turn signal, the messages waiting with the oldest one's arrival, and the running release beside the copy's pin ([Director health](../steward.md#director-health)).
The Steward supervisor's scheduled run reads it for each enrolled copy and sends `steward-report` kinds `director-stuck`, `director-unhealthy` and `release-behind`, deterministic code with no model call.
Each condition is recorded in the Steward store with the time it was first found and a mark naming what holds, and is one message until it clears.
`director turn-started` and `turn-ended` record the last turn in the session directory, `turn.json`, beside the context report.

## Alternatives rejected

- A new compact `director health --json` command: a second read of the same facts, and a second command for every older guildhall to lack; the feed is already read-only and bounded.
- The Steward judging staleness by its own observations alone: a stopped Director would wait a whole extra run; the copy's own times report it on the next run.
- A wake budget or a report repeated every run: the grill chose one report per condition.

## Consequences

The allow-list's shape is unchanged: fixed arguments, the copy directory as the one variable, no shell, no standard input.
A copy whose guildhall predates the health block is reported in the supervisor's log, `<state>/supervisor.log`, and sent nothing, and a copy on slice 1's guildhall refuses the three new kinds as invalid, a refusal the outbox records and never retries.
`director-stuck` compares the copy's times with the Steward's clock, so it relies on the machines' clocks agreeing to within the span.
A runtime with no turn hooks records no last turn, so its Director counts as stuck once a message has waited the span unacknowledged.
