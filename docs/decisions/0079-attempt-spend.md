# D79. Each attempt records the tokens and cost its runtime reports, marked measured, estimated or unknown

The Guildmaster asked to log or estimate each attempt's token use and cost alongside the [attempt caps](0074-guard-rails.md).
Guildhall now records, on every owned attempt's store record, what the attempt's runtime reported it spent, and `director status` sums it per task and per day beside the caps ([spend](../director.md#spend)).

## The runtime reports, the launcher records

An adapter that can report usage writes a usage file, `usage.json`, beside the attempt's result file ([usage file](../adapters.md#usage-file)): input, output, cache creation and cache read tokens and a cost in US dollars, each group marked `measured`, `estimated` or `unknown`.
The launcher reads it when the attempt ends and records it on the attempt with `store.RecordAttemptSpend` just before the attempt's finish, in the same store open; `artisan resume` does the same when it settles an exited launcher's attempt.

- The adapter writes the file, not the model, because the model never sees its own usage; the Claude wrapper reads it from Claude Code's output.
- A file, not the result, carries it: the result file's format is the model's to write and is version 1, and an attempt that ends without a result, such as the fake's `fault=no-result` or a Claude session that wrote no draft, still spent tokens.
- The launcher records it because every owned attempt ends there, whether it finished, failed or was stopped, and the interpreter's result intake sees only finished ones.
  `artisan resume` records it too, so a launcher killed before its finish loses nothing.
- The first record stands, as the first finish does: the launcher and `artisan resume` read the same file, so an equal second record is a replay and a different one is refused.

## Measured, estimated or unknown

The Claude adapter now runs print mode with `--output-format json`, so Claude Code prints one result message when it exits, carrying its `usage` and `total_cost_usd`.
Its token counts are the API's own, so they are `measured`; `total_cost_usd` is Claude Code's price for those tokens, not a bill, so the cost is `estimated`.
The wrapper writes the session's `result` text to its output as print mode did before, so the launcher log reads the same.

A runtime that reports nothing writes no file, and a file that cannot be read is not trusted: both record `unknown` with zero numbers, never a guess presented as measured.
A stopped Claude session prints no result message, so its spend is unknown even though it spent tokens; Director stages, run in the Director's own session, have no attempt spend and count as unknown.
The fake adapter reports fixed numbers, measured tokens and an estimated cost, so the tests of what is recorded are deterministic; the CI stub `claude` prints a result message with fixed numbers too, and neither makes a model call.

## Totals

`store.SpendTotal` sums finished attempts, counting those whose tokens or cost are unknown rather than adding a guess, and is `estimated` when any summed number is.
A running attempt is not counted until it reports.
The day's total covers the attempts started that UTC day, the same attempts `attempts_today` counts, so a long attempt's spend lands on the day the cap counted it.

## Caps stay attempt-based

The caps still meter attempts, not tokens or cost.
A token or cost budget would need every runtime to report usage, and it could refuse only the next attempt after one had already spent past it; it is a follow-up, as are spend for the Director's own session and for the Herdr tab mode, which has no wrapper to read usage from.
