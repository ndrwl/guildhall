# `internal/workflow`

## Contract

`Parse`, `Validate`, the step registry with each kind's allowed actors, `Step.Actor`, actor overrides and digests, and the review outcome shared by every review and document review, bound to its subject (R1.60, `docs/decisions/0026-actor-as-a-step-setting.md`).

The `profile` and `skills` settings and their `Step` accessors.
Grill steps accept optional `skills` with the shared distinct-name-list validation for either allowed actor, without adding `profile` or `instructions` ([decision](../decisions/0029-grill-step-skills.md)).
`ValidateStepSettings` checks an isolated step's kind, version and settings with the same actor, allowed-key, required-setting, type, name and duplicate rules as `Validate`, without requiring the surrounding workflow's input and loop wiring.

Review outcome findings carry a required stable `id` and an optional cited `rule`, and outcomes carry `dispositions` of the previous round's findings; `ReviewOutcome.Blocking` is true only for `changes_requested` with a rule-cited finding.
`DispositionState.Valid` is the one disposition-state rule, used by `ReviewOutcome.Validate` and the store's review projection; their `unknown state` refusal tests (`review_test.go`, `internal/store/rulings_test.go`) pin it.

`Weakenings`, `FloorProblems` and `Authority.Admit` are the workflow-edit authority policy at task creation ([configuration](../configuration.md#enforcement-at-task-creation)): a conservative classifier of a revision against a baseline, the review floor, and the admission decision over the shipped definitions, the caller's approved digests and candidate baselines; they read and record nothing.

## Tests

`workflow_test.go`, `strict_test.go`, `review_test.go`, `actor_test.go`, `profile_test.go`, `grill_test.go`

`authority_test.go` was written red first against stub bodies ([red run](../evidence/s10-approval-enforcement-2026-10-21-red.txt)): which kinds are guarded, each weakening and non-weakening edit of the shipped default, the floor on the shipped and example workflows and on edits below it, and admission and refusal for each basis.
