# Delivery pushes a task branch, opens a pull request and merges it under the merge policy

The engine now runs `publish` and the merge half of `deliver` (requirements R1.12, R1.36 and R1.63) through a forge boundary, [`internal/forge`](../contracts/forge.md), consumed as `interp.Forge` ([publish and deliver](../interpreter.md#publish-and-deliver)).
The Guildmaster chose each product question below from the recommended options on 2026-10-21, on the principle that Guildhall prescribes only the edges of delivery (the push target, the pull request, merging under the policy, never force-pushing the base and never bypassing protection), repository configuration sets defaults, and agents use git freely inside their task copy and decide recovery.

- **Shape.** Delivery uses the workflow kinds the registry already declares: `publish` pushes the head and opens or updates the pull request and never merges, and `deliver` merges after a `merge` answer; `hold` waits for the Director.
  Folding the push into `deliver` would have contradicted the registry and the configuration page.
- **Branch and head.** The head is pushed from the repository's configured `path` to `guildhall/<task>` on its `remote`; a head that is not a commit of that repository is refused, because no worktree custody exists yet to say where else it could live.
  The pull request targets the remote's default branch, and a branch that is the default branch is refused, so the base is never force-pushed.
- **Merge method.** Squash, fixed for now: no configuration key chooses another method until a repository needs one.
- **A refused merge.** The merge is bound to the decision's head with `--match-head-commit` and never uses `--admin` or `--auto`; when the forge refuses it, for example because required checks fail or the branch is behind its base, the refusal is recorded as a task document, `<decision>-refused` and then `-refused-2` and on, once per reason, the Director is told the forge's reason and the task waits for the Director, who decides the recovery.
  There is no automatic rebase or retry; a later advance asks the forge again.
- **The `auto` policy** stays refused with `ErrUnsupported`: R1.36's "within accepted scope" is not yet defined and automatic merging needs validation results.
- **Validation (`validate`)** is out of this change and stays refused, so a workflow with `validate`, including the shipped default, still stops there; a follow-up defines it.
- **Forges and credentials.** GitHub on `github.com` is the only implementation, over the `gh` program, and any other remote is refused naming that limit; credentials stay in `gh`'s own sign-in store and Guildhall reads, stores and passes none (R1.10, R1.28).

A delivered task is closed: the engine records the merged pull request as `<decision>-merge` and marks the task `done`, for which `interp.Records` gains the store's existing `SetTaskState`, and the advance returns the new wait `done`.
The `gh` program is driven through its command line, like Herdr and git, rather than through a GitHub API client, so no new module dependency is added (S7) and sign-in is `gh`'s own.
CI exercises `forge.GitHub` with real git against a local bare repository and the test binary acting as `gh`; an opt-in live test exists for a sandbox repository but has not been run, so live GitHub behaviour is not claimed (S4).
