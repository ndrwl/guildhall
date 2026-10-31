---
status: accepted
date: 2026-10-31
topic: tasks
tasks: d125
---

# A task's dependencies change after creation through append-only records, folded into one effective list

## Context

A task's dependencies were fixed by `task new --depends-on`.
When d102 was cancelled and replaced by d123, its dependent d103 could not follow, so it was cancelled and recreated as d124.
The Director needs to add or remove a dependency of an open task, with a reason, so a dependent can follow a replaced task ([changing dependencies](../tasks.md#changing-dependencies)).

## Decision

1. **The task record is not rewritten; each change is its own append-only record.**
   `Task.DependsOn` stays the list the task was created with, so `task new`'s replay and conflict rules are unchanged.
   A per-repository `task_dependencies` bucket, created lazily, holds `TaskDependency` records, numbered per task, `op` `add` or `remove` sharing one sequence, each with its reason.
   They are modelled on [task links](../tasks.md#task-links), including the `prior` count in the request and in the mutation `task-dependency:<repo>:<task>:<op>:<dep repo>:<dep task>:<prior>`.
2. **One fold, `store.EffectiveDependencies`, defines a task's dependencies.**
   It applies the records in number order to the creation list, so the latest record per dependency wins.
   Every reader uses it: the gate, `--ignore-deps`, `task cancel`'s message to dependents, `prune`, the clerk, and `task show` and `task list`.
   The engine reads the records through `interp.Records.TaskDependencies`, which all three implementations provide under `RunRecordsContract`.
3. **The store checks each change inside its write transaction.**
   It refuses the task itself, a cancelled dependency on `add`, and a remove of a dependency that is not effective.
   It also refuses an `add` that would make a cycle, found by walking effective dependencies from the new dependency across all repositories in the same transaction.
   Cycles were impossible while dependencies had to exist before their dependent; now they would deadlock both tasks at the gate.
   A `done` dependency may be added and is satisfied at once.
4. **A change applies at the task's next advance through the existing gate, and nothing else changes in the engine.**
   It never stops or restarts running work.
   A held task is released by the watcher's next tick once its last unmet dependency is removed.
5. **An add that holds nothing is recorded and noted, not refused.**
   An add to a task past the gate, or with a dependency override, which lasts for the task's life, holds nothing.
   The command records it and its receipt carries a `note`, which keeps the command uniform and matches "applies at the next advance".
   Refusing instead is a one-line change.
6. **The Director changes dependencies on the Guildmaster's word, or when it replaces a task that has dependents, and tells the Guildmaster; no decision is opened.**

## Consequences

- The d102/d103 case is two commands on the dependent, `task depends remove` for the old task and `task depends add` for the replacement, with no cancel-and-recreate.
- The gate reads one more thing, the task's dependency records, usually none, before looking up any dependency; a task past the gate reads nothing.
- `task timeline` keeps the creation list in `task` and lists every change under `dependencies`.
- Repointing dependents automatically on cancel-and-replace, such as `task cancel --replaced-by`, is left for a later task.
