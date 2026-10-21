# `internal/workflow`

## Contract

`Parse`, `Validate`, the step registry with each kind's allowed actors, `Step.Actor`, actor overrides and digests, and the review outcome shared by every review and document review, bound to its subject (R1.60, `docs/decisions/actor-as-a-step-setting.md`).

The `profile` and `skills` settings and their `Step` accessors.
Grill steps accept optional `skills` with the shared distinct-name-list validation for either allowed actor, without adding `profile` or `instructions` ([decision](../decisions/grill-step-skills.md)).
`ValidateStepSettings` checks an isolated step's kind, version and settings with the same actor, allowed-key, required-setting, type, name and duplicate rules as `Validate`, without requiring the surrounding workflow's input and loop wiring.

Review outcome findings carry an optional stable `id` and cited `rule`, and outcomes carry `dispositions` of the previous round's findings; `ReviewOutcome.Blocking` is true only for `changes_requested` with a rule-cited finding.

## Tests

`workflow_test.go`, `strict_test.go`, `review_test.go`, `actor_test.go`, `profile_test.go`, `grill_test.go`
