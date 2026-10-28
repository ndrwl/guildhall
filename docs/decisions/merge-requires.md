# Each repository chooses its merge requirements

This supersedes the repository's merge policy key and [D60](0060-delivery-stage.md)'s refusal of the `auto` policy (D60:13).
Requirements R1.12, R1.36, R1.58 and R1.63 now say that merge requirements are each repository's choice.

A repository lists what a task's pull request needs before Guildhall merges it, as TOML booleans under its `[[repository]]` block:

```toml
[repository.merge.requires]
guildmaster_approval = true   # the Guildmaster said "merge"
artisan_approval     = true   # the independent review Artisan approved this exact head, accepted spec and standards
ci_green             = true   # required CI passed on this head
nothing_open         = true   # no findings on the approving reviews and no unanswered decision on the task
```

A missing key is `true`, and a missing section requires everything, which is the old `approval` policy exactly: the same `deliver-<k>` decision, options and records.
Unknown keys and values that are not booleans are refused when the config is parsed, and the old policy key is now an unknown key.
There is no migration: a config that still sets the old policy key must be edited when the binary is upgraded.
The Guildmaster chose every default to be `true` so that an unattended merge is something a repository opts into explicitly.

## Rules at deliver

- **A required `artisan_approval` or `ci_green` is never merged over.** When one does not hold, deliver fails the advance with `ErrReviewGate` or `ErrValidationGate`, as before, whatever `guildmaster_approval` says, and no decision answer can merge over it.
  Only turning the requirement off in the repository's own configuration skips its gate, and the merge question still shows its state.
- **Behind and conflicting pull requests** still record `pr-blocked-<head>-<state>` and wait for the Director before any decision opens.
  Superseded by [the merge queue and tiered rebase](merge-queue-rebase.md): the decision opens, or the authorization is recorded, whatever the mergeability, and the merge queue rebases the front.
- **The Guildmaster's `merge` overrides only `nothing_open`.** With `guildmaster_approval` on, the merge question lists each requirement as `holds`, `fails: <reason>` or `not required`.
- **Merging without the Guildmaster.** With `guildmaster_approval` off and every other required requirement holding, deliver records `deliver-<k>-authorized` (`guildhall.merge-authorization/1`: the head, the pull request, the effective requirements, the mergeability state and each condition with its evidence) and merges without a decision.
  The merged message says "merged by Guildhall, deliver-<k>-authorized", so `task timeline` shows what allowed the merge.
  Deliver reads the requirements from the current configuration at every advance, so this document is the only record of the values in effect; a replay reuses it for the same head.
- **Falling back.** With `guildmaster_approval` off and `nothing_open` failing, deliver opens `deliver-<k>` as if the Guildmaster's approval were required, and the question begins "Guildhall did not merge by itself: <failed requirements>".

Pending checks never reach deliver: validate owns them.
Red checks reach the Guildmaster through validate's failed loop and its loop exit, so every failure is either a question or a hard error, and nothing waits silently.

## Left out

- No size requirement: the engine measures no diff, and the quality size budgets are flagged, never blocked.
- No head option: the delivered head is always the reviewed head, and a rebase gets a new review round ([D42](0042-rebase-classification.md)).
  Amended by [the merge queue and tiered rebase](merge-queue-rebase.md): the delivered head may also be one Guildhall rebased from the reviewed head, covered by its recorded rebase, and `[repository.merge]` gains `reapprove_after_rebase`, `minor_conflict_files` and `minor_conflict_lines` beside `requires`.
  A rebuilt build head still gets a new review round.
- No CI "if present" option: `no_ci` already passes a head with no checks.
- No merge method option: squash stays fixed ([D60](0060-delivery-stage.md)).
- No change to the workflow floor: turning a requirement off changes only what deliver demands, not which steps a workflow must have.
