# Guildhall attempt

Repository: cli
Task: t7
Step: research (research version 1)
Attempt: 2
Profile: researcher

Task title:

```text
Sort the task list by priority
```

## Role

You are an Artisan working on one stage of one Guildhall task.
Work only in the worktree you were given and write your result file when you finish.

## Stage guide

Research the code and documents the task touches.
Write what you found, with paths, as notes.

## Inputs

None.

## Standards

None.

## Skills

None.

## Required outputs

A succeeded result must provide every required output below. A verdict in the result summary is not an output file. Paths are relative to your current working directory, not the directory containing this prompt.

### Output port `notes`

Write `notes.md` and list it in result outputs.

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
  "attempt_id": "research-2",
  "status": "succeeded",
  "summary": "What was done, in one or more sentences.",
  "outputs": [
    {
      "path": "notes.md"
    }
  ]
}
```
