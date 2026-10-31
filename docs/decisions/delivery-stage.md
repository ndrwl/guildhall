---
status: accepted
date: 2026-10-28
topic: delivery
tasks: d13
amended-by: merge-queue-rebase.md, review-outcome-2.md
formerly: D60, 0060-delivery-stage.md, D66, 0066-validate-stage.md, merge-requires.md
---

# Delivery publishes a task branch, validates it on the forge's checks and squash-merges it under the repository's merge requirements

## Context

Requirements R1.12, R1.36, R1.58 and R1.63 need a task's work to reach the repository through a pull request without Guildhall bypassing the forge.
The Guildmaster chose the delivery questions on 2026-10-21 on the principle that Guildhall prescribes only the edges of delivery (the push target, the pull request, merging under the repository's rules, never force-pushing the base and never bypassing protection), repository configuration sets defaults, and agents use git freely inside their task copy and decide recovery.
Validation was left out of that first change and defined by a follow-up; d13 then replaced the single merge policy with merge requirements each repository chooses.

## Decision

- **Shape.**
  Delivery uses the step kinds the registry declares, all through the forge boundary [`internal/forge`](../contracts/forge.md): `publish` pushes the head and opens or updates the pull request and never merges, `validate` reads the forge's checks of the published head, and `deliver` merges ([publish and deliver](../interpreter.md#publish-and-deliver), [validate](../interpreter.md#validate)).
- **Branch and head.**
  The head is pushed from the repository's `path` to `guildhall/<task>` on its `remote`, and the pull request targets the remote's default branch; a head that is not a commit of that repository is refused, and so is a branch that is the default branch, so the base is never force-pushed.
- **Merge.**
  Squash, fixed, bound to the head with `--match-head-commit`, never with `--admin` or `--auto`.
  A forge refusal is recorded once per reason as `<merge>-refused`, the Director is told the forge's reason, and the merge is not retried automatically; a later advance asks again.
- **Validate** reads the checks of the head commit, never of whatever the pull request is at now.
  Every reported check counts; no checks is pending unless the repository sets `no_ci`; any failed check is red at once, even while others are pending; a state Guildhall does not know fails the read.
  Red takes the workflow's `failed` loop back to build with the failing checks as feedback, and the loop's exit offers `continue` and `reject`, never `accept`.
  A waiting failed head is read again, so a re-run check counts, and a round of `max` is spent only on new evidence: a failed run, by check name and run id, that the latest result does not already hold.
  Reads are paced by [`checks_refresh`](../configuration.md#settings).
- **Merge requirements.**
  Each repository lists `guildmaster_approval`, `artisan_approval`, `ci_green` and `nothing_open` under `[repository.merge.requires]`, each `true` when missing ([merge requirements](../configuration.md#merge-requirements)); unknown keys and non-booleans are refused at parse.
  A required `artisan_approval` or `ci_green` is never merged over (`ErrReviewGate`, `ErrValidationGate`), whatever `guildmaster_approval` says; the Guildmaster's `merge` overrides only `nothing_open`, whose meaning of "open" [review outcome /2](review-outcome-2.md) defines.
  With `guildmaster_approval` off and every other requirement holding, deliver records `deliver-<k>-authorized` and merges with no decision; when `nothing_open` fails it opens the merge decision instead, saying Guildhall did not merge by itself.
- **Forges and credentials.**
  GitHub on `github.com` only, driven through the `gh` program's command line like Herdr and git, so no module dependency is added (S7); credentials stay in `gh`'s own sign-in store and Guildhall reads, stores and passes none (R1.10, R1.28).
- A merged task records `<merge>-merge` and is marked `done`; authorized merges first wait in [the merge queue](merge-queue-rebase.md), which brings the front up to date.

## Alternatives rejected

- Folding the push into `deliver`: it would contradict the registry and the configuration page.
- A merge method key: none until a repository needs one.
- A GitHub API client: it would add a module dependency and a sign-in of Guildhall's own, where `gh` keeps its own.
- Counting only required checks: Guildhall cannot tell which checks a repository's protection requires on every forge, and the forge still enforces its own rules at merge.
- No checks as green: right after a push the forge may not have registered its checks, and validation must not pass on absent evidence.
- `accept` on a red loop exit: it would deliver a head nothing validated, which the review floor forbids.
- A size requirement (the engine measures no diff, and size budgets are flagged, never blocked), a CI "if present" option (`no_ci` already covers it) and a change to the workflow floor (a requirement turned off changes only what deliver demands).
- Migrating the old policy key: a config that still sets it is refused as an unknown key and must be edited.

## Consequences

Every requirement defaults to `true`, so an unattended merge is something a repository opts into explicitly.
Deliver reads the requirements from the current configuration at every advance, so `deliver-<k>-authorized` (`guildhall.merge-authorization/1`) is the only record of the values in effect, and a replay reuses it for the same head.
Pending checks never reach deliver, and red ones reach the Guildmaster through validate's loop exit, so every failure is a question or a hard error and nothing waits silently.
Deliver's validation gate repeats what validate already guarantees, deliberately, as defence in depth.
CI exercises `forge.GitHub` with real git and a stand-in `gh`; the opt-in live test has not been run, so live GitHub behaviour is not claimed (S4).

## History

- 2026-10-22, D60 (0060-delivery-stage.md): decided publish and deliver as above, with the squash merge, `--match-head-commit` and the recorded refusal, and no automatic rebase or retry.
  The `auto` merge policy stayed refused with `ErrUnsupported`, because R1.36's "within accepted scope" was undefined and automatic merging needed validation results; merge-requires.md replaced it. `validate` stayed refused until D66.
  A head outside the repository was refused because no worktree custody existed yet; [worktree custody](worktree-custody.md) then made the task's directory a worktree, so the rule stands.
- 2026-10-22, D66 (0066-validate-stage.md): defined validate as option (b) of the delivery question on red or pending CI, which let the shipped default workflow run from task creation to a merged pull request.
  The re-read of a waiting failed head was added at the Guildmaster's request after review found three states with no exit ("Yes lets check ci periodically - refresh can be set in config"), and spending a round only on new evidence followed ("the checks should know which commits have been run"), so an unchanged head no longer burnt rounds and model launches.
  Autonomous continuation moved to the Director session's [checks watcher](../director.md#checks).
  It kept the `auto` policy refused and nothing rebasing a branch behind its base; [the merge queue](merge-queue-rebase.md) later made validate rebase a conflicting pull request.
- 2026-10-28, merge-requires.md (d13): replaced the merge policy key and D60's refusal of `auto` with per-repository requirements, rewording R1.12, R1.36, R1.58 and R1.63; the Guildmaster chose every default to be `true`, which is the old `approval` policy exactly.
  A behind or conflicting pull request still recorded `pr-blocked-<head>-<state>` and waited for the Director before any decision opened, and the delivered head was always the reviewed head; [the merge queue](merge-queue-rebase.md) replaced both on 2026-10-29, opening the decision whatever the mergeability and letting a recorded rebase cover a rebased head.
  Review outcome /2 later narrowed `nothing_open` so suggestions, and disputed findings with a standing Director ruling, do not count.
