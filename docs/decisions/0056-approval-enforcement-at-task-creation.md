# D56. Approval enforcement at task creation

`task new` admits a repository's effective workflow only when its digest is shipped with the release, approved by the Guildmaster in the Director's store, or a change that meets the review floor and weakens nothing in an authorized baseline ([configuration](../configuration.md#enforcement-at-task-creation)).
The policy is `workflow.Authority.Admit`, a pure function over definitions: the store supplies approved digests and the CLI supplies the shipped definitions and the repository's file, so the policy is tested without a store and the store keeps only approval records ([D22](0022-workflow-approvals-per-director-keyed-by.md)).

The baselines are the shipped definitions and the repository's workflow file before its overrides, because those are the only authorized definitions whose content Guildhall holds: an approval records a digest, not a definition.
A non-weakening change from some other approved revision therefore needs its own approval; storing approved definitions so any of them can be a baseline was left out to keep the store unchanged.

The classifier follows the Steward's managed-setting classification ([rollout approval](../steward.md#approval)): a fixed list of changes that need approval, and everything it cannot prove harmless counts as weakening.
It treats any reorder of shared steps and any setting change on a guarded step as weakening rather than reasoning about which are safe.
It sees definitions only, so a changed role or skill file that a profile or skill name points to is invisible to it; binding those files to a task is the pinning work, not this decision.

The review floor, one non-author review and one validation before delivery, is checked here rather than in `workflow.Validate`, so shipped and approved workflows below it, such as a Director-local test workflow, stay valid and the Guildmaster's approval of the exact digest is what lowers it.
The design called for the floor to come from a managed policy key; the floor is fixed in this release until that key is designed.

Approving a repository's effective workflow needed a command, because an overridden definition exists in no file: `workflow approve --config --repo` resolves it exactly as `task new` does.
