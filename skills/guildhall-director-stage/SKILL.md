---
name: guildhall-director-stage
description: Use as the Director when a director-stage message hands you a stage to run, such as a grill, to read its brief, run it with the skills it lists and finish it with guildhall step finish.
---

# Director stage

Some workflow steps are yours to run in your own session rather than an Artisan's, such as the default workflow's `grill`, a live interview of the Guildmaster.
Guildhall records each as an attempt, prepares its brief and hands it to you with a `director-stage` message; the task waits with `{"wait":"director-stage",...}` until you finish it.

## Steps

1. Read the message with `guildhall messages read <id>`.
   It names the step, the attempt, the path of the stage's prompt under `<step>-<n>/prompt`, the output ports and the exact `guildhall step finish` command that ends the stage.
2. Read the prompt.
   It is your brief: your role, the stage's guide, the skills the step lists and the stage's inputs, such as the task's title and earlier stages' documents.
3. Run the stage as the brief and its skills say.
   A grill uses `guildhall-grill` to interview the Guildmaster live, in rounds of numbered questions, and `guildhall-domain-model` to settle the words you agree on.
4. Draft each output in a file under your session directory, such as `notes/app-t-12-grill-1.md`, never in a temporary directory, so the draft survives while you work.
5. When the stage's own skills say it is done, finish it with the command the message named, one `--output` per port and a one-line summary:

```sh
guildhall step finish --repo app --task t-12 --step grill --attempt 1 --summary "Agreed CSV export behind a flag, three columns, no import." --output notes=notes/app-t-12-grill-1.md
```

6. Acknowledge the message with `guildhall messages ack director <id>`.

Once a grill has finished, when the Guildmaster or you want a design before build, run `task design` with `guildhall-director-tasks`.

`guildhall step finish` copies each file into the task's working directory, records the attempt's result and advances the task, printing what it waits for next.
It is refused when the attempt is not the step's current one or is already finished, so a repeated finish never finishes the wrong attempt.

## Abandoning a stage

When the stage cannot finish, such as when the Guildmaster wants to drop the task or the brief is wrong, finish it as failed with the reason as the summary:

```sh
guildhall step finish --repo app --task t-12 --step grill --attempt 1 --summary "The Guildmaster wants to rethink the export first." --failed
```

The task then waits for you, as after a failed Artisan attempt, and `guildhall step retry` with a reason hands you the stage's next attempt when it is time.

## Rules

- A grill is never finished until the Guildmaster confirms you share an understanding; a handoff in the middle of one leaves it unfinished, and the next session restarts it.
- Read the repository only for quick facts; anything more is an Artisan's research stage.
- Write no `result.json` by hand: `guildhall step finish` is the only way a stage you run ends.
