# Guildhall attempt

Repository: cli
Task: t7
Step: review-astra (review version 1)
Attempt: 2
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

Format: one strict JSON `guildhall.review-outcome/1` object, not Markdown or a prose verdict. Use reviewer `automated`; verdict is `approved`, `changes_requested`, or `rejected` (the latter two require findings). Findings have `id` and `summary`, and may cite `rule`, `file`, and `line`. For every previous finding, include a disposition with `finding` and `state` (`resolved`, `still-open`, or `withdrawn`); still-open findings must also remain in findings.

The revision values below are illustrative: replace them with the exact reviewed input head/document digest, accepted spec digest (for code review), and standards revision. Never submit the illustrative zero hashes.

Minimal approval example (choose your actual verdict and findings):

```json
{
  "schema": "guildhall.review-outcome/1",
  "reviewer": "automated",
  "verdict": "approved",
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
