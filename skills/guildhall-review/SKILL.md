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
- The base is the one the attempt recorded for its range; as an interim fallback until the reviewer is given the recorded base as an input, where the inputs give none, review the commits on the head that the repository's default branch does not have.
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
A smell is always a labelled judgement call ("possible Feature Envy") and a suggestion, never a blocker.
A written standard overrides the baseline: where one endorses the pattern, drop the smell, and where one forbids it, as S6 and S11 forbid speculative abstractions, report a finding citing that rule instead.

## Findings

Each finding names its `id`, `rule`, `file` and `line`, and its summary states a concrete failure: the input or state and the wrong result.
A finding with no rule behind it is a suggestion: leave `rule` out, and it never blocks.
An id is lowercase letters, digits and hyphens, names one problem, and keeps that name in every later round.

## Rounds

When the inputs include `previous`, disposition every finding of that outcome before you write new ones, so the Director can see a stall.

- `resolved`: this head fixes it; do not list it again.
- `still-open`: it stands, including when the builder disputed it without fixing it; list it again under the same id.
- `withdrawn`: you no longer hold it, for example because the dispute showed it wrong; do not list it again.

With a `rulings` input, keeping a ruled finding `still-open` contests the Director's ruling and stops the loop for the Guildmaster, so do that only when the cited rule still plainly applies, and say why.

## Verdict and result

- `approved`: no rule-cited finding stands; suggestions may remain.
- `changes_requested`: at least one rule-cited finding stands.
- `rejected`: the change should not go on in this form, such as an empty range or work that contradicts the spec throughout; at least one finding says why.

Bind the outcome to the head, the spec input's digest and the standards revision the prompt gives, with reviewer `automated`.
Write it as `review-outcome.json` and list it as the first output of your result file.
The result's summary is one line: the findings on each axis and the worst one within each axis, never one winner across both.

```json
{
  "schema": "guildhall.review-outcome/1",
  "reviewer": "automated",
  "verdict": "changes_requested",
  "findings": [
    {"id": "spec-json-output", "rule": "S1", "file": "internal/cli/status.go", "line": 42,
     "summary": "Spec: \"status --json prints one object per task\"; with two tasks the head prints a table, so a script decoding the output fails."},
    {"id": "std-no-red-run", "rule": "S2", "file": "internal/cli/status_test.go", "line": 18,
     "summary": "The --json test was added with the code and the description shows no red run, so nothing shows it failed before the change."},
    {"id": "smell-data-clump", "file": "internal/cli/status.go", "line": 60,
     "summary": "Possible Data Clumps: repository, task and step travel together through four functions; one small type would carry them."}
  ],
  "dispositions": [
    {"finding": "spec-json-output", "state": "still-open"},
    {"finding": "std-poll-race", "state": "resolved"}
  ],
  "revisions": {
    "subject": {"code_head": "0123456789abcdef0123456789abcdef01234567"},
    "spec_digest": "sha256:242df7188ef036fb7f2376eda94382b23f989a04a61db84ae59acba315fc21be",
    "standards_revision": "standards-1"
  }
}
```
