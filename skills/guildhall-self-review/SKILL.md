---
name: guildhall-self-review
description: Use as the Director after a task's change merges, when director status shows quality.self_review on, to record the task's script facts and rubric evidence as a self-review document without changing the task.
---

# Self-review

A self-review records how good a delivered task's work was against the rubric in Guildhall's `docs/quality.md`, so the Director learns from measured quality rather than felt progress.
It is for learning and for workflow-change proposals: it never re-gates, reopens or changes a merged task.
Scripts and recorded checks decide facts, a separate judge session grades what needs judgement, and you never grade your own task.

## When

Run it once per task after its `merged` message, only when `guildhall director status --json` shows `"quality": {"self_review": true, ...}`.
When `self_review` is false or `quality` is absent, do nothing: the practice is off for this copy.
The Guildmaster may also ask for one on a merged task, whatever the setting.

## Steps

1. Read the task's records, none of which change anything:

   ```sh
   guildhall task timeline --repo app t-12 --json
   guildhall task files --repo app t-12 --json
   ```

   From them take the head that merged, the pull request in the `publish-<k>-pr` document, the review outcomes, the validation results, and each attempt's `duration_seconds` and spend.
2. Choose the next number `n`: one more than the `self-review-<n>.json` files already under `$GUILDHALL_COPY/quality/<repo>/<task>/`, starting at 1.
   Write every file of this review into that directory.
3. Write the merged change's diff against the base it merged onto, from the repository's path in the config:

   ```sh
   git -C <repo-path> diff --no-color --no-ext-diff <base>...<head> > self-review-1.diff
   ```

4. Run the facts script on it with the size budget from `quality.size_budget_lines` and `quality.size_budget_files`, using the twin for your shell, and keep its output:

   ```sh
   sh <skill-dir>/scripts/facts.sh self-review-1.diff 400 15 > self-review-1.facts
   pwsh -NoProfile -File <skill-dir>/scripts/facts.ps1 self-review-1.diff 400 15 > self-review-1.facts
   ```

   The script makes no model call and needs no tool but the shell.
   It prints counts, `size_over: yes` when the change is over either budget, and one line per item to read: `secret`, `assertion-removed`, `sleep-added`, `skip-added`, `pull-request-target`, `write-permission` and `unpinned-action`, each with its `file:line`.
   Exit status 2 is a usage error, never a finding.
5. Run the report-only tools that are already installed on the changed packages, such as `gocognit -over 15`, `dupl` and `govulncheck`, and record each one's output.
   Install nothing: a tool that is missing is recorded as `not run: not installed`, which is missing evidence, never a pass.
6. Record the gates from the records: E1 from the validation result for the exact head, A1, B1, D4, D5, F1 to F3, G1 and G2 from the review outcomes and the facts.
   A gate the records cannot show is `pass: null` with what is missing, never a guess.
7. Record the script-judged scores and every trend number: D1 is 3 by `script` only when `size_over` is `no`, and the H1, E3 and E5 numbers come from the timeline.
8. Leave every model-judged score `pending` with `score: null`, for a judge session that is neither you nor the task's builder; D1 over budget is one of them, with the size facts as its evidence, since the judge reads whether the pull request says why it could not split.
   Write the judge's brief beside the document as `self-review-<n>-judge.md`: the criteria to score, the anchors from `docs/quality.md`, and the files it reads.
9. Write `self-review-<n>.json` in the shape below, then tell the Guildmaster in one or two sentences: the lowest pillar, any flag, and any gate the records could not show.

## The document

```json
{
  "schema": "guildhall.self-review/1",
  "repository": "app",
  "task": "t-12",
  "number": 1,
  "base": "4f1c2a9e0b7d6c5a4f3e2d1c0b9a8f7e6d5c4b3a",
  "head": "9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b",
  "pull_request": "https://github.com/example/app/pull/7",
  "settings": {"size_budget_lines": 400, "size_budget_files": 15},
  "facts": {
    "files": 6, "test_files": 2, "nontest_lines": 140, "size_over": false,
    "sleeps_added": 0, "sleeps_removed": 1, "skips_added": 0, "skips_removed": 0,
    "assertions_removed": 0, "secrets": 0, "workflow_problems": 0,
    "items": []
  },
  "tools": {"gocognit": "not run: not installed", "dupl": "not run: not installed", "govulncheck": "No vulnerabilities found."},
  "criteria": [
    {"id": "E1", "kind": "gate", "judge": "script", "pass": true, "evidence": "validate-1-result: all checks passed for head 9a8b7c6"},
    {"id": "G2", "kind": "gate", "judge": "script", "pass": true, "evidence": "assertions_removed: 0"},
    {"id": "D1", "kind": "score", "judge": "script", "score": 3, "evidence": "140 non-test lines in 6 files, under 400 and 15"},
    {"id": "B2", "kind": "score", "judge": "pending", "score": null, "evidence": ""},
    {"id": "H1", "kind": "trend", "judge": "script", "value": 1, "evidence": "one review round, no fix round"},
    {"id": "E5", "kind": "trend", "judge": "script", "value": 0.42, "evidence": "task spend: cost measured, 0.42 USD"}
  ],
  "flags": [],
  "lowest_pillar": "B"
}
```

`criteria` holds one entry per criterion recorded, by its id in `docs/quality.md`: a gate has `pass`, a score has `score` from 0 to 3, and a trend has `value`.
`judge` is `script`, `model`, `guildmaster` or `pending`, and `evidence` quotes the record, `file:line` or command output the entry rests on.
`flags` lists what the Guildmaster should see, such as a size over budget, a secret item or a gate that failed; a flag never blocks anything.
`lowest_pillar` is the letter of the pillar with the lowest recorded score, or empty when no score is recorded yet.

## Rules

- Change nothing in the task or the repository: no edits, pushes, retries, decisions or rulings.
- Never score a model-judged criterion yourself, D1 over budget included, and never turn a trend into a target for the next task.
- Read the ledger of self-reviews at hallmark time and in a handoff note: the criteria lowest over the window are what a workflow-change proposal must target.

The review is complete when the document records every gate, the facts and tools, the script-judged scores and trends, a pending entry with a judge brief for each model-judged score, and the Guildmaster has the summary.
