# Guildhall attempt

Repository: cli
Task: t7
Step: review-astra (review version 1)
Attempt: 2
Review base: unavailable
Profile: reviewer-astra

## Role

You are a fresh reviewer: you did not write the change you review.
Cite the standard behind every finding.

## Stage guide

Review the head against the accepted specification and the standards below.
Write a review outcome bound to the head, the spec digest and the standards revision.

## Instructions

Check the JSON output on Windows too.
Cite S1 or S2.

## Inputs

The inputs directory beside this prompt holds a verified copy of each input document.

- spec: inputs/tasks/t7/spec.md (sha256:242df7188ef036fb7f2376eda94382b23f989a04a61db84ae59acba315fc21be)

## Standards

### standards/review.md

# Review standards

- S1. Every behaviour change has a test that failed before it.
- S2. Windows paths are handled.

## Skills

None.

## Required outputs

A succeeded result must provide every required output below. A verdict in the result summary is not an output file. Paths are relative to your current working directory, not the directory containing this prompt.

### Output port `outcome`

Write `outcome.json` and list it in result outputs.

Format: one strict JSON `guildhall.review-outcome/2` object, not Markdown or a prose verdict. Use reviewer `automated`; verdict is `approved`, `changes_requested`, or `rejected` (the latter two require findings). `summary` is required: your overall verdict in one paragraph, with no blank line. Findings are blocking: each has `id`, `summary` and the `rule` it breaks, and may give `file` and `line`. Anything that cites no rule is a suggestion, in `suggestions`, with `id` and `summary` and optional `file` and `line`; suggestions never block and need no disposition later. An `id` is lowercase letters, digits and hyphens, starting with a letter, at most 63 characters (e.g. `f1`, `must-1`), used once across findings and suggestions; keep a finding's id in later rounds. For every previous finding, include a disposition with `finding` and `state`: `resolved` (no longer listed), `unresolved` (still listed in findings), or `disputed` (the builder argued against it and you still hold it: still listed in findings, with a `reason`).

The revision values below are illustrative: replace them with the exact reviewed input head/document digest, accepted spec digest (for code review), and standards revision. Never submit the illustrative zero hashes.

Approval example (choose your actual verdict, summary, findings and suggestions):

```json
{
  "schema": "guildhall.review-outcome/2",
  "reviewer": "automated",
  "verdict": "approved",
  "summary": "The change does what the spec asks and its tests cover it; one optional improvement is suggested.",
  "suggestions": [
    {
      "id": "s1",
      "file": "path/to/file.go",
      "line": 12,
      "summary": "An optional improvement; it never blocks."
    }
  ],
  "revisions": {
    "subject": {
      "code_head": "0000000000000000000000000000000000000000"
    },
    "spec_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
    "standards_revision": "example-standards-revision"
  }
}
```

Example requesting changes, with previous findings f0, f1 and f2:

```json
{
  "schema": "guildhall.review-outcome/2",
  "reviewer": "automated",
  "verdict": "changes_requested",
  "summary": "Two findings from the previous round still stand, one of them disputed by the builder; the third was fixed.",
  "findings": [
    {
      "id": "f1",
      "rule": "R1.2",
      "file": "path/to/file.go",
      "line": 30,
      "summary": "What is wrong and what to change."
    },
    {
      "id": "f2",
      "rule": "S4",
      "summary": "A finding the builder argued against and you still hold."
    }
  ],
  "suggestions": [
    {
      "id": "s1",
      "file": "path/to/file.go",
      "line": 12,
      "summary": "An optional improvement; it never blocks."
    }
  ],
  "dispositions": [
    {
      "finding": "f0",
      "state": "resolved"
    },
    {
      "finding": "f1",
      "state": "unresolved"
    },
    {
      "finding": "f2",
      "state": "disputed",
      "reason": "Why the builder's argument does not settle it."
    }
  ],
  "revisions": {
    "subject": {
      "code_head": "0000000000000000000000000000000000000000"
    },
    "spec_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
    "standards_revision": "example-standards-revision"
  }
}
```

## Result template

After writing the required files (or committing the code head), write this JSON shape to the result file your runtime names, once, as your last action. Keep schema, version, task_id and attempt_id unchanged. Use status `succeeded` only when the work and required outputs are complete; otherwise use `failed` and explain why in summary (failed results may use an empty outputs list).

```json
{
  "schema": "guildhall.artisan-result",
  "version": 1,
  "task_id": "t7",
  "attempt_id": "review-astra-2",
  "status": "succeeded",
  "summary": "What was done, in one or more sentences.",
  "outputs": [
    {
      "path": "outcome.json"
    }
  ]
}
```
