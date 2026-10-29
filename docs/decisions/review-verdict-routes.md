# A changes_requested review always loops back

This reverses the rule that a finding without a cited rule never blocks.
`ReviewOutcome.Blocking` is now exactly `verdict == changes_requested`, whatever the findings cite.

## Why

Task d16's review-1 recorded `changes_requested`, yet the task went on to publish and opened its merge decision.
The engine routed on `Blocking`, which held only for `changes_requested` with at least one rule-cited finding, so an outcome requesting changes with rule-less findings completed the review step.
Every routing path read it: completing the review, counting loop rounds against `max`, stall signals and the review gate.
The rendered review output instructions made the case easy to hit: their own `changes_requested` example listed a finding with no rule.

A reviewer's verdict is what they decided; the engine should not second-guess it from the shape of the findings.
A reviewer with suggestions only records `approved` and lists them under `suggestions`, which the contract already allowed.

Since [review outcome /2](review-outcome-2.md) a /2 finding must cite a rule, so a /2 `changes_requested` always carried a rule-cited finding and blocked already.
The gap remained for /1 documents, which are read with their rule-less findings as suggestions: a /1 `changes_requested` with only those, d16's case, completed the step.
With `Blocking` on the verdict it blocks.

## Rules

- **Blocking is the verdict.** One change in `Blocking` fixes the review route, round counting, `max`, the stall check and the deliver review gate together.
- **Publish runs the review gate too.** Before it pushes anything, publish checks the review steps before it, bound to the head it would publish.
  A failing gate is recorded as `<publish round>-refused`; under a required `artisan_approval` nothing is pushed and the advance fails with `ErrReviewGate`, and otherwise the reason is recorded and publish goes on, as at deliver.
  With routing on the verdict this guard should never fire: it catches a routing regression before anything leaves the machine.
- **Past `max` nothing changes.** The loop exit opens the Guildmaster's continue, accept or reject decision and tells the Director; it never falls through to publish.
- **Wording follows the rule.** The review standard, the review skill, the configuration, contract and interpreter docs all say that `changes_requested` blocks and suggestions go in an `approved` outcome.

## Left out

- **No intake refusal.** A /1 `changes_requested` outcome with no rule-cited finding is still a valid outcome, so recorded outcomes still read; refusing it would make existing tasks' histories unreadable.
- **Director rulings are unchanged.** Only a contested ruling on a rule-cited finding is a dispute.
- **Past records are not rewritten.** d16's publish-1 and deliver-1 stay as recorded.

## Upgrade effect

Any open task whose latest review is a rule-less `changes_requested` sees that outcome as blocking on its next advance: it starts a build round with the outcome as feedback, or, past publish, fails the review gate.
Because loop-exit ids `<step>-x<k>` count blocking results, outcomes that newly block can renumber later rounds of an open task with a mixed history.
Done tasks are unaffected.
