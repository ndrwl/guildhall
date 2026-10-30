---
name: guildhall-review
description: Use in a review stage to review one code head against its accepted spec and the inlined standards on two separate axes, and write a review outcome with rule-cited findings and dispositions of the previous round.
---

Adapted from `code-review` in mattpocock/skills (MIT, commit 5beb35b); see CREDITS.md.

# Review

A review stage judges one code head on two axes and writes one review outcome.
Nobody answers you during the stage: the inputs, the standards in the prompt and the repository are your whole brief.
Review alone and start no sub-agents, because a workflow that wants several reviewers runs this skill in several attempts.

## What you review

- The head is the commit the `head` input names, and the outcome binds to it.
- The base is the exact commit in the prompt's `Review base` field, taken from the attempt's recorded range; never substitute the current default branch.
- If `Review base` is `unavailable`, the attempt has no recorded range: report the missing base as a blocker rather than guessing the range.
- Read the change once with `git diff <base>...<head>` and `git log --oneline <base>..<head>`, and review only what it changes.
- With a `resolution` input the round re-reviews a rebase: review that resolution diff, and carry earlier findings by the round rules below.
- The spec is the `spec` input under `inputs/`, at the digest the prompt lists; it is never missing and you never look for another.
- The standards are the ones inlined in the prompt; for the Guildhall repository they are rules S1 to S11 of `docs/review.md` and the requirements in `docs/requirements.md`.
- An empty diff is not a question: write `rejected` with one finding, citing S1, that says the range changes nothing.

## Two axes

The axes are reviewed and reported separately, because a change can pass one and fail the other.
Never merge them or rerank across them: give each finding an id starting `spec-` or `std-`, and suggestions an id starting `smell-`.

**Spec.**
Report what the spec asked for that is missing or partial, what looks implemented but is wrong, and behaviour it did not ask for.
Scope creep is a finding (S1).
Quote the spec line each finding rests on in its summary.

**Standards.**
Report every place the diff breaks an inlined standard, and cite the rule id, such as `S4` or `R1.27`.
Skip anything tooling enforces, such as formatting, vet and lint that CI runs.
Then match the diff against the smell baseline in `smells.md`.
A smell is always a labelled judgement call ("possible Feature Envy") and a suggestion, never a blocker: a smell never makes the verdict `changes_requested`.
A written standard overrides the baseline: where one endorses the pattern, drop the smell, and where one forbids it, as S6 and S11 forbid speculative abstractions, report a finding citing that rule instead.

## Findings and suggestions

The outcome keeps blockers and suggestions in two lists.

`findings` are the blockers.
Each names its `id`, `rule`, `file` and `line`, and its summary states a concrete failure: the input or state and the wrong result.
A finding must cite a rule; intake refuses one that does not.

`suggestions` are everything with no rule behind it, and every `smell-` item.
Each names its `id` and `summary`, and may give `file` and `line`.
A suggestion never blocks, never holds the merge and needs no disposition in a later round.
The verdict, not the lists, routes the task: `changes_requested` always sends the build back, so suggestions alone go in an `approved` outcome.

An id is lowercase letters, digits and hyphens, names one problem, is used once across both lists, and keeps that name in every later round.

## Summary

Give the outcome a `summary`: one paragraph, with no blank line, saying your overall verdict and why.
Name the worst finding on each axis, if there is one, and say whether only suggestions remain.

## Rounds

When the inputs include `previous`, disposition every finding of that outcome before you write new ones, so the Director can see a stall.
Disposition only previous *findings*; never disposition a suggestion.

- `resolved`: this head fixes it, or you no longer hold it; never list its id again, in either `findings` or `suggestions`.
  Downgrading a finding to a suggestion under the same id also lists it again: if the point should live on as a suggestion, give it a new id.
- `unresolved`: it stands; list it again under the same id.
- `disputed`: the builder argued against it without fixing it, and you find the argument plausible but still hold the finding; list it again under the same id, and give the disposition a `reason` saying what the builder argued and why the rule still applies.

A disputed finding stays open.
With `changes_requested` the loop goes on; with `approved` it holds the merge until the Director rules on it.

With a `rulings` input, keeping a ruled finding open, `unresolved` or `disputed`, contests the Director's ruling and stops the loop for the Guildmaster, so do that only when the cited rule still plainly applies, and say why.

## Verdict and result

- `approved`: no finding stands that you hold as a blocker; suggestions, smells included, may remain.
- `changes_requested`: at least one finding stands; it always sends the build back, so never use it for suggestions alone.
- `rejected`: the change should not go on in this form, such as an empty range or work that contradicts the spec throughout; at least one finding says why.

Bind the outcome to the head, the spec input's digest and the standards revision the prompt gives, with reviewer `automated`.
Write it as `review-outcome.json`, schema `guildhall.review-outcome/2`, and list it as the first output of your result file.

Before you write the result file, check the outcome as intake will:

```text
guildhall review check review-outcome.json
```

When there is a `previous` input, add `--previous <the previous input's path>`, so the check also finds a previous finding with no disposition.
Fix every error it reports, and run it again until it prints `ok`.
Only then write the result file.
If the command cannot be run, because permission is denied or it is not found, say so in the result summary; never skip the self-check silently.

The result's summary is one line: the findings on each axis and the worst one within each axis, never one winner across both.

```json
{
  "schema": "guildhall.review-outcome/2",
  "reviewer": "automated",
  "verdict": "changes_requested",
  "summary": "The --json output is still a table for two tasks (spec-json-output), and the new test has no red run (std-no-red-run), which the builder disputes; the poll race is fixed and one smell is suggested.",
  "findings": [
    {"id": "spec-json-output", "rule": "S1", "file": "internal/cli/status.go", "line": 42,
     "summary": "Spec: \"status --json prints one object per task\"; with two tasks the head prints a table, so a script decoding the output fails."},
    {"id": "std-no-red-run", "rule": "S2", "file": "internal/cli/status_test.go", "line": 18,
     "summary": "The --json test was added with the code and the description shows no red run, so nothing shows it failed before the change."}
  ],
  "suggestions": [
    {"id": "smell-data-clump", "file": "internal/cli/status.go", "line": 60,
     "summary": "Possible Data Clumps: repository, task and step travel together through four functions; one small type would carry them."}
  ],
  "dispositions": [
    {"finding": "spec-json-output", "state": "unresolved"},
    {"finding": "std-no-red-run", "state": "disputed",
     "reason": "The builder says the test is obviously new; S2 still asks for the red run to be shown."},
    {"finding": "std-poll-race", "state": "resolved"}
  ],
  "revisions": {
    "subject": {"code_head": "0123456789abcdef0123456789abcdef01234567"},
    "spec_digest": "sha256:242df7188ef036fb7f2376eda94382b23f989a04a61db84ae59acba315fc21be",
    "standards_revision": "standards-1"
  }
}
```

The wrong way: a reviewer dispositions `suggest-4`, a suggestion of the previous outcome, as resolved and lists it again in `suggestions`.

```text
"suggestions": [
  {"id": "suggest-4", "summary": "A shorter name would read better."}
],
"dispositions": [
  {"finding": "suggest-4", "state": "resolved"}
]
```

Intake refuses the outcome, and the whole review round is lost:

```text
resolved finding "suggest-4" is still listed in findings or suggestions
```

To fix it, drop the disposition, since a suggestion is never dispositioned, and either keep the suggestion or remove it.
