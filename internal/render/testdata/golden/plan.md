# Guildhall attempt

Repository: cli
Task: t7
Step: plan (plan version 1)
Attempt: 2
Profile: planner

## Role

You are an Artisan working on one stage of one Guildhall task.
Work only in the worktree you were given and write your result file when you finish.

## Stage guide

Write a specification the Guildmaster can accept: scope, contract, tests and size.

## Inputs

The inputs directory beside this prompt holds a verified copy of each input document.

- context: inputs/tasks/t7/grill-notes.md (sha256:841ffd59700d20f04c07a415bdb7a72ff3746ed32cb18cc43c9de0732ad9c0ed)

## Standards

None.

## Skills

The skills directory beside this prompt holds a copy of each skill.

- guildhall-contributing: skills/guildhall-contributing (sha256:0c385355800932af2d91f9f247c393032ab5909542166fa7c75a3812c9364039)

## Required outputs

A succeeded result must provide every required output below. A verdict in the result summary is not an output file. Paths are relative to your current working directory, not the directory containing this prompt.

### Output port `spec`

Write `spec.md` and list it in result outputs.

Format: a UTF-8 Markdown document. Minimal example:

```text
# Notes

The requested work is recorded here.
```

## Result template

After writing the required files (or committing the code head), write this JSON shape to the result file your runtime names, once, as your last action. Keep schema, version, task_id and attempt_id unchanged. Use status `succeeded` only when the work and required outputs are complete; otherwise use `failed` and explain why in summary (failed results may use an empty outputs list).

```json
{
  "schema": "guildhall.artisan-result",
  "version": 1,
  "task_id": "t7",
  "attempt_id": "plan-2",
  "status": "succeeded",
  "summary": "What was done, in one or more sentences.",
  "outputs": [
    {
      "path": "spec.md"
    }
  ]
}
```
