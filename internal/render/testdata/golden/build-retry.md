# Guildhall attempt

Repository: cli
Task: t7
Step: build (build version 1)
Attempt: 2
Profile: builder

Task title:

```text
Sort the task list by priority
```

## Why this is a retry

This is a retry of attempt 1, which failed.

Director's reason:

```text
The flaky test is fixed on main; try again.
```

Intake rejection:

```text
output spec.md: no such file
```

Previous summary:

````text
Tests failed:
```
FAIL TestSort
```
````

Address this in your attempt; the stage guide and required outputs still apply.

## Role

You are an Artisan working on one stage of one Guildhall task.
Work only in the worktree you were given and write your result file when you finish.

## Stage guide

Implement the accepted specification contract first: tests red, then the implementation.
Commit your work and report the head.

## Inputs

The inputs directory beside this prompt holds a verified copy of each input document.

- spec: inputs/tasks/t7/spec.md (sha256:242df7188ef036fb7f2376eda94382b23f989a04a61db84ae59acba315fc21be)

## Standards

None.

## Skills

The skills directory beside this prompt holds a copy of each skill.

- go-tdd: skills/go-tdd (sha256:5771b0a0a8393288c7caeea797aea949d98ec66b2193390a826a6e9f6be1b33b)
- guildhall-contributing: skills/guildhall-contributing (sha256:0c385355800932af2d91f9f247c393032ab5909542166fa7c75a3812c9364039)

## Required outputs

A succeeded result must provide every required output below. A verdict in the result summary is not an output file. Paths are relative to your current working directory, not the directory containing this prompt.

### Output port `head`

Commit on the task branch `guildhall/t7` in your working directory; Guildhall refuses a HEAD that is not that branch's tip or does not descend from the attempt's base. Commit all tracked work. Guildhall takes the final Git HEAD automatically; no file is needed. Leave no uncommitted tracked changes (untracked notes alone are allowed). Do not list source files as outputs. The recorded format is a full 40- or 64-character lowercase hexadecimal commit id, for example:

```text
0000000000000000000000000000000000000000
```

## Result template

After writing the required files (or committing the code head), write this JSON shape to the result file your runtime names, once, as your last action. Keep schema, version, task_id and attempt_id unchanged. Use status `succeeded` only when the work and required outputs are complete; otherwise use `failed` and explain why in summary (failed results may use an empty outputs list).

```json
{
  "schema": "guildhall.artisan-result",
  "version": 1,
  "task_id": "t7",
  "attempt_id": "build-2",
  "status": "succeeded",
  "summary": "What was done, in one or more sentences.",
  "outputs": []
}
```
