# Review outcome /2: suggestions apart, three disposition states and a summary

A review outcome is now `guildhall.review-outcome/2` ([contract](../configuration.md#review-outcome)).
Every writer writes /2, human and automated review alike, so one outcome contract is still shared by both (R1.60); readers still accept /1.

## Why

In /1 a suggestion was a finding without a `rule`, in the same `findings` list as the blockers.
`Blocking` already ignored rule-less findings, but the [`nothing_open`](merge-requires.md) merge requirement counted every finding, so a clean approval that carried only suggestions held the merge for the Guildmaster (d22 `deliver-2`: "review-5-outcome lists 2 finding(s)"; d4 `deliver-1`: 4).
/2 keeps blockers and suggestions in separate lists, records how each earlier finding ended up, and adds a one-paragraph summary, so `nothing_open` counts only what is actually open and clean work with suggestions merges unattended.

## The /2 shape

- `summary` is required: the reviewer's overall verdict in one paragraph, not blank, with no blank line, within the store's text limit.
- `findings[]` are blockers only, and each must cite a `rule`; a rule-less finding is refused at intake with a message to move it to `suggestions`.
- `suggestions[]` are `{id, summary, file?, line?}`; they never block, never count for `nothing_open` and need no disposition in a later round. Ids are unique across both lists.
- A disposition is `resolved` (listed in neither list), `unresolved` (listed again in `findings`) or `disputed` (listed again, with a required `reason`).
- `approved` may still carry findings, as a loop-exit accept does; `Blocking` keeps its meaning, `changes_requested` with a rule-cited finding.

## Reading /1

A /1 document is decoded with its own shape, so a /2-only key (`summary`, `suggestions`, `reason`) or a /2 state name in it is refused.
It is returned with the /2 meaning, in memory only; stored bytes and digests are never rewritten:

| /1 | /2 meaning |
|---|---|
| a finding with a `rule` | a finding |
| a finding without a `rule` | a suggestion |
| `still-open` | `unresolved` |
| `withdrawn` | `resolved` |
| `resolved` | `resolved` |

Its schema stays /1 and its summary empty, so a caller can tell an old document; the summary requirement applies only to /2.
`Blocking` gives the same answer for every /1 document as before.
A /2 reviewer whose `previous` input is a /1 outcome dispositions only its rule-cited findings, and a reviewer that still writes /1 is accepted at intake.
Store projections recorded before /2 may hold the /1 state names; the store accepts them through `DispositionState.Normalized`.

## Disputed findings and rulings

`disputed` is the reviewer saying "the builder argued against this; I find it plausible, but I don't agree", and the finding stays in `findings`.
With `changes_requested` the loop goes on as before.
With `approved` the dispute reaches deliver and holds `nothing_open` until the Director rules on it: any uncontested ruling closes it, `out-of-scope` or `duplicate`, since `defer` stays refused for rule-cited items.
A ruling made after deliver has already fallen back to its merge decision still releases the dispute: without `guildmaster_approval`, the next advance finds every requirement holding, merges by itself and records that its authorization supersedes the unanswered decision.
A ruling still never resolves a finding; releasing a disputed one for `nothing_open` is the one exception.
A review attempt contests a ruling when it keeps the ruled finding open, `unresolved` or `disputed`.
Rulings may name a suggestion, and `defer` applies to suggestions (and to rule-less findings of old /1 projections), so it stays reachable.

Findings an approval from a loop-exit `accept` keeps as `unresolved` still hold the merge: the Guildmaster accepted them for the loop, not for the merge, and sees them again in the merge decision.
That decision's question lists each finding of the latest outcomes with its state and each suggestion, and a holding `nothing_open` records "N suggestion(s), not counted" in the authorization of an unattended merge.

## Upgrading

An older Guildhall binary cannot read a store that holds /2 outcomes: it refuses them as `ErrNewerSchema`.
There is no downgrade path.

## Left out

- The pull request body is unchanged; suggestions appear in the merge decision and the authorization record instead.
- Nothing turns suggestions into follow-up tasks; that stays the Director's judgement.
