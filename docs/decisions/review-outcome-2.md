---
status: accepted
date: 2026-10-29
topic: review
amends: delivery-stage.md, actor-as-a-step-setting.md, loops-and-review-gate.md, director-rulings.md, review-skill.md
formerly: review-verdict-routes.md
---

# A review outcome is guildhall.review-outcome/2, and a changes_requested verdict always loops back

## Context

In `guildhall.review-outcome/1` a suggestion was a finding without a `rule`, in the same `findings` list as the blockers.
`Blocking` ignored rule-less findings, but the [`nothing_open`](delivery-stage.md) merge requirement counted every finding, so a clean approval that carried only suggestions held the merge for the Guildmaster (d22 `deliver-2`: "review-5-outcome lists 2 finding(s)"; d4 `deliver-1`: 4).
Later, task d16's review-1 recorded `changes_requested`, yet the task went on to publish and opened its merge decision.
The engine routed on `Blocking`, which held only for `changes_requested` with at least one rule-cited finding, and the rendered output instructions' own `changes_requested` example listed a finding with no rule.

## Decision

Every writer, human and automated review alike, writes `guildhall.review-outcome/2`, so one outcome contract is still shared by both (R1.60); readers still accept /1.
The [review outcome contract](../configuration.md#review-outcome) holds the shape and how /1 is read.

- **Blockers and suggestions apart.**
  Every finding cites a `rule`, and a rule-less one is refused at intake with a message to move it to `suggestions`; a suggestion never blocks, never counts for `nothing_open` and needs no disposition in a later round.
- **A summary and three disposition states.**
  A required one-paragraph `summary` holds the overall verdict, and an earlier finding ends `resolved`, `unresolved` or `disputed`, the last with a required `reason`.
- **/1 is read, never rewritten.**
  A /1 document is decoded with its own shape and given the /2 meaning in memory only, so stored bytes and digests never change; store projections with /1 state names are accepted through `DispositionState.Normalized`.
  A reviewer that still writes /1 is accepted at intake, and a /2 reviewer whose `previous` input is /1 dispositions only its rule-cited findings.
- **Blocking is the verdict.**
  An outcome blocks exactly when its verdict is `changes_requested`, whatever its findings cite, so one rule drives the review route, round counting against `max`, the stall check and the review gate.
  A reviewer's verdict is what they decided, and the engine does not second-guess it from the findings' shape.
- **Publish runs the review gate too**, bound to the head it would publish, before it pushes anything: a failing gate is recorded as `<publish round>-refused`, and under a required `artisan_approval` nothing is pushed and the advance fails with `ErrReviewGate`; otherwise publish goes on, as deliver does.
  With routing on the verdict it should never fire: it catches a routing regression before anything leaves the machine.
- **Disputes and rulings.**
  A `disputed` finding in an `approved` outcome holds `nothing_open` until the Director rules on it; any uncontested ruling releases it, even after deliver has fallen back to its merge decision, and without `guildmaster_approval` the next advance then merges by itself.
  That release is the one exception to "a ruling never resolves a finding".
  Rulings may name a suggestion, and `defer` applies to suggestions, so it stays reachable.
- **Accepted findings still hold the merge.**
  Findings a loop-exit `accept` keeps as `unresolved` hold `nothing_open`: the Guildmaster accepted them for the loop, not the merge, and the merge question lists them again with every suggestion.

## Alternatives rejected

- Keep `Blocking` as "`changes_requested` with a rule-cited finding": /2 alone left the gap for /1 documents, whose rule-less findings read as suggestions, which was d16's case.
- Refuse a /1 `changes_requested` outcome with no rule-cited finding at intake: existing tasks' histories would become unreadable.
- Rewrite stored /1 outcomes as /2: their digests are bound into attempts and decisions.

## Consequences

An older Guildhall binary refuses a store that holds /2 outcomes with `ErrNewerSchema`, and there is no downgrade path.
An open task whose latest review is a rule-less `changes_requested` blocks on its next advance: it starts a build round, or, past publish, fails the review gate.
Loop-exit ids `<step>-x<k>` count blocking results, so later rounds of an open task with a mixed history can renumber; done tasks and past records, such as d16's publish-1 and deliver-1, are unchanged.
The pull request body is unchanged: suggestions appear in the merge decision and in an unattended merge's authorization, as "N suggestion(s), not counted", and nothing turns them into follow-up tasks.

## History

- 2026-10-29, review-outcome-2.md: introduced /2 with separate `suggestions`, the `resolved`/`unresolved`/`disputed` states and a required `summary`, closing the follow-up the [review skill](review-skill.md) left open, so clean approvals with suggestions merge unattended.
  It kept `Blocking` as "`changes_requested` with a rule-cited finding" and made a disputed finding with a standing ruling release `nothing_open`.
  It left the pull request body unchanged and suggestions as the Director's judgement rather than follow-up tasks.
- 2026-10-29, review-verdict-routes.md: after d16's `changes_requested` review completed its step and reached publish, `Blocking` became the verdict alone, reversing "a finding without a cited rule never blocks", and publish gained the review gate.
  The review standard, the review skill and the configuration, contract and interpreter docs were reworded to say so.
  Past `max` the loop exit still opens the Guildmaster's continue, accept or reject decision and never falls through to publish.
  Rejected then: an intake refusal of rule-less /1 `changes_requested` outcomes, rewriting past records, and any change to Director rulings, so only a contested ruling on a rule-cited finding is a dispute.
