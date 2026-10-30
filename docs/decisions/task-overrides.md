# A task override gives one task its own value for a task-scoped setting, on the Guildmaster's word

A task could only change a setting such as `default_effort` by changing it for the whole copy in `config.toml`, so every other task took it too until someone set it back by hand.
The d69 grill settled how one task may carry its own values, and d69's first PR built the scope registry that says which settings a task may override (`config.CheckTaskOverride`, `config.WithTaskOverrides`).
This records the grill's decisions 1–5, which d86 builds on that registry: who records an override, when it applies, how it is stored, which layer wins and which keys are refused.

## Decisions

1. **Only the Guildmaster sets an override, through the Director, with their words recorded.**
   The Director runs `guildhall task override set` only when the Guildmaster asks, quoting them in `--text`; an Artisan cannot, since its settings deny `Bash(guildhall ...)`, and no Director sets one on its own judgement.
   As for [task grants](task-grants.md), that is the Director's conduct under its role, not an enforced boundary.
2. **An override may be set at any time and applies from the task's next stage; it never re-runs a finished one.**
   Recording or removing one launches, re-runs and invalidates nothing, as [task context](../tasks.md#task-context) does.
3. **Each override is an append-only store record with the key, the value and the Guildmaster's words; the latest record per key wins, and removing an override is its own record.**
   A per-repository `task_overrides` bucket, created lazily, keeps `TaskOverride` records numbered per task, `op` `set` or `unset` sharing one sequence, modelled on [task links](../tasks.md#task-links).
   `prior`, how many records the task already has for the key, is part of the request and of the mutation `task-override:<repo>:<task>:<op>:<key>:<prior>`: a rerun of the same command replays its receipt, a command that did not see a record made meanwhile is refused, and setting `high`, `low`, then `high` again with the same words records three times.
4. **Precedence is task override, then local, then managed; a managed key not marked `overridable = true` wins over a task override too.**
   That is the layer order `WithTaskOverrides` applies, so `set` refuses a pinned key, and an override a later managed pin overrules is skipped, not fatal.
5. **Only task-scoped settings may be overridden; machine-wide settings, `[repository.merge]`, `[director]` and repository identity are refused with a reason.**
   `set` loads the copy's config and prints `CheckTaskOverride`'s reason unchanged, writing nothing, so the registry is the one place that classifies a key.
   The store has no config, so it refuses only what no setting could be: an empty key, one with whitespace, and a step key (`step.`), which decision 5 leaves out.
   `unset` reads no config, so an override that has since stopped applying can still be removed.

## Consequences

- Each record keeps the Guildmaster's words beside the value, so `task override list`, the task's history and `store doctor` show what was asked and when.
- Every advance reads the task's overrides once, under its lock, and applies them before it builds the engine (`interp.ApplyTaskOverrides`), so the engine, the launcher's stall and each Artisan's effort read the task's effective config while machine-wide values keep the copy's.
  Each Artisan binding records the overrides applied and skipped; staleness ignores both, so decision 2 holds.
- An attempt relaunched after a crash runs with the overrides current at the relaunch, not those its binding recorded: the binding is the record of what the stage was first given, and the relaunch is the same stage started again.
- A merge-requires override (d76) only makes a task's merge requirements stricter. It is applied at one seam, `run.repository()`, to a copy of the task's repository, so every gate agrees and a sibling task advanced in the same process keeps the shared config. Deliver checks for an existing authorization before it evaluates the requirements, so an override added after a head was authorized never retracts that authorization.
