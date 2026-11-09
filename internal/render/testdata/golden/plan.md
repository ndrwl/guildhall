# Guildhall attempt

Repository: cli
Task: t7
Step: plan (plan version 1)
Attempt: 2
Profile: planner

Task title:

```text
Sort the task list by priority
```

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

Strict sections: Guildhall parses these sections of `spec.md` itself, and `guildhall result submit` and intake refuse a result that breaks one with the error shown, so copy the valid shape exactly.

**`## Summary`.** Write its parts as list items, Goal, Size, Slices, Changed since the grill and Open questions, in at most 4 KiB. Never write them as `##` headings: the next `##` heading ends the block, so a block that starts with one is empty and refused.

Valid:

```text
## Summary

- **Goal:** An Artisan fixes a rejected result in the same attempt.
- **Size:** 2 slices, about 1,100 lines in 25 files.
- **Slices:** (1) Result check command; (2) Prompts submit.
- **Changed since the grill:** Nothing; the plan follows the grill's agreed change.
- **Open questions:** None.
```

Invalid:

```text
## Summary

## Goal

An Artisan fixes a rejected result in the same attempt.
```

It gets: `spec has no summary: the spec has no ## Summary block, or it is empty or over 4096 bytes`

**`## Slices`.** Write a numbered list `1.`, `2.`, … with no gaps, each entry starting with a bold title, then what that pull request delivers and its expected size, and no two titles alike. No text may come before entry 1, and an entry without a `**<title>**` is refused.

Valid:

```text
## Slices

1. **Result check command**: adds the check and submit commands; about 700 lines in 12 files.
2. **Prompts submit**: the prompts tell the Artisan to submit; about 400 lines in 13 files.
```

Invalid:

```text
## Slices

1. Result check command: adds the check and submit commands; about 700 lines in 12 files.
2. **Prompts submit**: the prompts tell the Artisan to submit; about 400 lines in 13 files.
```

It gets: `spec slices refused: slice 1 (line 3) has no bold title: start it with **<title>**`

**`## Open questions`.** Write exactly "None", or questions each starting on a line `**Qn. <title>**`, numbered from Q1 with no gaps, each with an `Options: (a) …; (b) ….` line and a `Recommended: (<letter>). <why>` line whose letter is one of its options. Nothing else may come before the first `**Q1.` line: no sentence, no `### Q1.` heading, no list marker such as `- **Q1.`, and no explanation after "None".

Valid:

```text
## Open questions

**Q1. Where does the Artisan write its draft?**
A rejected draft must never be published.
Options: (a) a draft file that only submit copies; (b) the result file itself.
Recommended: (a). Only an accepted draft is then published.
```

Invalid:

```text
## Open questions

One question is still open.

**Q1. Where does the Artisan write its draft?**
Options: (a) a draft file that only submit copies; (b) the result file itself.
Recommended: (a). Only an accepted draft is then published.
```

It gets: `open questions malformed: ## Open questions line 3 is neither "None" nor a **Qn. title** question`

## Result template

After writing the required files (or committing the code head), write this JSON shape to the draft file your runtime names and submit it with `guildhall result submit <draft file>`, which runs the checks Guildhall runs on your result when you finish. If it prints `rejected: <reason>`, fix what the reason names, in your files or the draft, and submit again; you are done when it prints `ok`, or when it says no resubmissions are left. If your runtime names a result file and no draft file, write this JSON shape to that result file instead, once, as your last action. Keep schema, version, task_id and attempt_id unchanged. Use status `succeeded` only when the work and required outputs are complete; otherwise use `failed` and explain why in summary (failed results may use an empty outputs list).

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

### Already delivered or nothing to do

When a merged pull request already shipped everything this stage asks for, or the task needs no change at all, say so with an `outcome` field beside the fields above: use status `failed` and `outputs: []`, commit nothing, start the summary as the stage guide says and give the evidence there. Guildhall reads the outcome rather than your summary; nothing closes until the Director or the Guildmaster confirms it. Leave the field out otherwise. For a change a merged pull request of this repository already shipped:

```json
"outcome": {"kind": "already-delivered", "by_pr": "https://github.com/owner/repo/pull/123"}
```

For a task that needs no change, with why in `reason`:

```json
"outcome": {"kind": "nothing-to-do", "reason": "Why the task needs no change."}
```
