# `internal/workflow`

## Contract

`Parse`, `Validate`, the step registry with each kind's allowed actors, `Step.Actor`, actor overrides and digests, and the review outcome shared by every review and document review, bound to its subject (R1.60, `docs/decisions/actor-as-a-step-setting.md`).

The `profile` and `skills` settings and their `Step` accessors.
`ValidateStepSettings` checks an isolated step's kind, version and settings with the same actor, allowed-key, required-setting, type, name and duplicate rules as `Validate`, without requiring the surrounding workflow's input and loop wiring.

## Tests

`workflow_test.go`, `strict_test.go`, `review_test.go`, `actor_test.go`, `profile_test.go`
