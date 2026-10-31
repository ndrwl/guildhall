---
status: accepted
date: 2026-10-22
topic: engine
formerly: D79, 0079-attempt-spend.md
---

# Each attempt records the tokens and cost its runtime reports, marked measured, estimated or unknown

## Context

The Guildmaster asked to log or estimate each attempt's token use and cost alongside the [attempt caps](guard-rails.md).

## Decision

Guildhall records, on every owned attempt's store record, what the attempt's runtime reported it spent, and `director status` sums it per task and per day beside the caps ([spend](../director.md#spend)).

- An adapter that can report usage writes `usage.json` beside the attempt's result file ([usage file](../adapters.md#usage-file)): input, output, cache creation and cache read tokens and a cost in US dollars, each group marked `measured`, `estimated` or `unknown`.
- The adapter writes the file, not the model, because the model never sees its own usage; the Claude wrapper reads it from Claude Code's output.
- The launcher records it with `store.RecordAttemptSpend` just before the attempt's finish, in the same store open, because every owned attempt ends there, whether it finished, failed or was stopped; `artisan resume` does the same when it settles an exited launcher's attempt, so a launcher killed before its finish loses nothing.
- The first record stands, as the first finish does: an equal second record is a replay and a different one is refused.
- The [Claude adapter](../adapters.md#claude-adapter)'s token counts are the API's own, so they are `measured`; `total_cost_usd` is Claude Code's price for those tokens, not a bill, so the cost is `estimated`.
- A runtime that reports nothing writes no file, and a file that cannot be read is not trusted: both record `unknown` with zero numbers, never a guess presented as measured.
- `store.SpendTotal` sums finished attempts, counting those with unknown tokens or cost rather than adding a guess, and is `estimated` when any summed number is; the day's total covers the attempts started that UTC day, the same attempts `attempts_today` counts.
- The caps still meter attempts, not tokens or cost.

## Alternatives rejected

- Carrying usage in the result file: its format is the model's to write and is version 1, and an attempt that ends without a result, such as the fake's `fault=no-result` or a Claude session that wrote no draft, still spent tokens.
- Recording it in the interpreter's result intake: that sees only finished attempts.
- A token or cost budget in place of attempt caps: it would need every runtime to report usage, and it could refuse only the next attempt after one had already spent past it.

## Consequences

A stopped Claude session prints no result message, so its spend is unknown even though it spent tokens; Director stages, run in the Director's own session, have no attempt spend and count as unknown.
A running attempt is not counted until it reports, and a long attempt's spend lands on the day the cap counted it.
The fake adapter and the CI stub `claude` report fixed numbers, so the tests of what is recorded are deterministic and make no model call.
A token or cost budget, spend for the Director's own session and spend for the Herdr tab mode, which has no wrapper to read usage from, are follow-ups.
