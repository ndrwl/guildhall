# Guard rails are enforced by the binary, with attempt caps counted inside the start

A Director's guard rails are three limits the binary enforces whatever the Director asks ([guard rails](../director.md#guard-rails)): the repositories it may act on, each repository's merge rule, and caps on the Artisan attempts it starts.
`config.GuardRails` returns all three in one place, and the caps are settings read through `Config.Setting`, so a Steward's managed value is read-only to the Director and a standalone copy sets them under `[local]`.

## Repositories

Every command that acts on a repository finds it through `config.ListedRepository`, which refuses one the config does not list with `config.ErrRepositoryNotListed`, `guard rail repositories: the config does not list repository "<id>"`.
`task start`, `step done`, `step finish` and `step retry` check it before reading a result file, locking the task or touching the store.
`messages send` checks the envelope's repository against the copy's config, or one given with `--config`; a send over `--store` alone has no config to check and is unchanged, because the store already refuses a message about a task it does not hold and `task new` refuses an unlisted repository.

## Merge rule

The merge rule is the repository's `merge_policy`, which `deliver` already enforces; this change only reports it with the other rails.

## Attempt caps

`max_running_attempts` (default 4) and `max_attempts_per_day` (default 40) are per Director, across every repository.
They meter attempts, not tokens or cost, because no adapter reports cost to the binary yet.

- The count and the start are one store mutation, `store.StartCappedAttempt`, under the store's single-writer lock, so two advances in different processes can never both take the last slot.
  A check before the start would have left that race open.
- Running is every attempt started and not finished.
  The plan counted unreleased ownership records; an attempt is recorded before its launcher takes ownership, so counting ownership records would miss launches in flight.
  Every unreleased ownership record belongs to an unfinished attempt, so nothing the plan counted is missed.
- The day is the UTC day of the store's clock, the clock that stamps the receipts it counts, so the count never mixes two clocks.
- The caps are not part of the start's request, so replaying a start returns its receipt whatever the caps are now: an attempt recorded before a crash is launched again, never refused.
- A refusal records and launches nothing and fails the advance with exit 1, `guard rail max_running_attempts: ...` or `guard rail max_attempts_per_day: ...`.
  The engine sends the Director one `budget-refused` message per task and UTC day, `<repo>.<task>.budget-refused.<YYYY-MM-DD>`; a later refusal that day conflicts with it and sends nothing, so a Director retrying in a loop is told once.
- Nothing relaunches a refused task by itself.
  A launcher's advance moves only its own task, so a refused task starts at the Director's next `task start`, or `step retry` for a refused retry, once capacity returns.
  An automatic retry would need a scheduler the binary does not have; the message names the command instead.

`director status` shows the rails and the counts under `session.guard_rails`, the counts read in the same transaction as the rest of the feed.
