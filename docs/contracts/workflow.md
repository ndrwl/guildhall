# `internal/workflow`

## Contract

`Parse`, `Validate`, the step registry with each kind's allowed actors, `Step.Actor`, actor overrides and digests, and the review outcome shared by every review and document review, bound to its subject (R1.60, `docs/decisions/0026-actor-as-a-step-setting.md`).

`JudgeStep`, `quality-judge`, is reserved for a delivered task's [judge attempts](../decisions/quality-judge.md): `Validate` refuses it as a step id.
The `profile` and `skills` settings and their `Step` accessors follow the per-kind support described in [configuration](../configuration.md#built-in-step-kinds).
Invalid types, invalid names, duplicate skills and undeclared settings are refused during validation; omitted settings remain valid.
Grill steps accept optional `skills` with the shared distinct-name-list validation for either allowed actor, without adding `profile` or `instructions` ([decision](../decisions/0029-grill-step-skills.md)).
The release skills for a grill follow its actor: `guildhall-grill` with `guildhall-domain-model` for the Director, `guildhall-grill-unattended` for an Artisan ([decision](../decisions/0061-grill-live-interview.md)); the registry does not bind skills to actors, so a step lists the ones it renders, and the shipped default's Director grill lists `guildhall-grill` and `guildhall-domain-model` (`TestDefaultGrillIsTheDirectorsLiveGrill`, red first against the default with no skills, [red run](../evidence/director-stages-2026-10-22-red.txt)).
`ValidateStepSettings` checks an isolated step's kind, version and settings with the same actor, allowed-key, required-setting, type, name and duplicate rules as `Validate`, without requiring the surrounding workflow's input and loop wiring.

Review outcome findings carry a required stable `id` and an optional cited `rule`, and outcomes carry `dispositions` of the previous round's findings; `ReviewOutcome.Blocking` is true only for `changes_requested` with a rule-cited finding.
`DispositionState.Valid` is the one disposition-state rule, used by `ReviewOutcome.Validate` and the store's review projection; their `unknown state` refusal tests (`review_test.go`, `internal/store/rulings_test.go`) pin it.

`Weakenings`, `FloorProblems` and `Authority.Admit` are the workflow-edit authority policy at task creation ([configuration](../configuration.md#enforcement-at-task-creation)): a conservative classifier of a revision against a baseline, the review floor, and the admission decision over the shipped definitions, the caller's approved digests and candidate baselines; they read and record nothing.

## Refutation contract

The [built-in registry](../../internal/workflow/registry.go) owns `refute` and `guildmaster-approval` ports, settings, actors and loop outcomes.
Missing required inputs, mismatched artifact types and unsupported actors/settings are refused by the existing validator.
See [configuration](../configuration.md#opt-in-plan-refutation) for opting in, the [baseline stage guide](../../baseline/stages/refute.md) for critique instructions, and the [interpreter contract](../interpreter.md#plan-refutation) for intake and acceptance semantics.

## Tests

`workflow_test.go`, `strict_test.go`, `review_test.go`, `actor_test.go`, `profile_test.go`, `grill_test.go`

`TestResearchAcceptsOnlyProfile` covers omitted and valid profiles, invalid profile types and names, and refusal of skills both alone and alongside a profile, through full-workflow and isolated-step validation.
The original broader research settings tests captured [red](../evidence/research-profile-2026-10-23-red.txt) and [green](../evidence/research-profile-2026-10-23-green.txt) runs before the scope was narrowed to profile only; those runs are historical evidence, not verification of skills refusal.

`authority_test.go` was written red first against stub bodies ([red run](../evidence/s10-approval-enforcement-2026-10-21-red.txt)): which kinds are guarded, each weakening and non-weakening edit of the shipped default, the floor on the shipped and example workflows and on edits below it, and admission and refusal for each basis.
The reserved-id case of `TestValidationRefusals` was written red first ([red run](../evidence/quality-judge-2026-10-23-red.txt)).
