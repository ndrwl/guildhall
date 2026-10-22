# `internal/forge`

## Contract

The boundary to the host of a repository's pull requests, called by the interpreter's [publish and deliver](../interpreter.md#publish-and-deliver) through its `interp.Forge` seam.
Guildhall prescribes only the edges: it pushes only a task branch, never the base; it merges only the head the Guildmaster's merge decision was bound to, with a squash merge; and it never bypasses the forge's branch protection, required checks or review rules ([decision](../decisions/delivery-stage.md)).
Credentials stay in the forge program's own sign-in store: nothing here reads, stores or passes one (R1.10, R1.28).

`Publish(ctx, Change)` pushes `Change.Head` to `refs/heads/<Change.Branch>` on `Change.Remote`, replacing whatever that task branch held, and returns the branch's open pull request, opening one against the remote's default branch when none is open.
It refuses, pushing nothing:

- a change missing its repository, remote or title, or naming an invalid branch (`ValidBranch`) or a head that is not a full commit id: `ErrInvalid`;
- a branch that is the remote's default branch: `ErrInvalid`;
- a head that is not a commit of `Change.Repository`: `ErrUnknownHead`.

Repeating a change returns the same pull request, and a new head on the same branch, even one that rewrites it, updates the open pull request rather than opening another.
The returned pull request is at the head just published, because a forge may report its head a moment after the push.

`Merge(ctx, Merge)` squash-merges pull request `Merge.Number` only while it is at `Merge.Head`, and returns it merged with its merge commit.
A pull request already merged at that head is returned as it is, so repeating a merge is safe.
A forge refusal (required checks, protection, a branch behind its base, a pull request no longer at the head), a closed pull request, one merged at another head, or one not merged after the forge answered, is `ErrRefused` with the forge's reason in the error text; nothing was merged.
A malformed merge is `ErrInvalid`; an error reaching the forge, such as a missing program or a failed sign-in, is returned as it is and is not `ErrRefused`.
Neither operation is safe for concurrent calls on one branch; the interpreter calls them under the task's lock.

`GitHub` is the implementation for `github.com`: git, found on `PATH` unless `GitHub.Git` names it, pushes with `git -C <repository> push --porcelain <remote> +<head>:refs/heads/<branch>`, and gh, found on `PATH` unless `GitHub.GH` names it, reads the default branch (`gh repo view --json defaultBranchRef`), lists the branch's open pull request, creates one with `--head`, `--title` and `--body` (no `--base`, so the default branch), views it and merges it with `gh pr merge <n> --squash --match-head-commit <head>`.
Every gh call names `--repo github.com/<owner>/<repo>`, which `GitHubRepository` derives from an `https://`, `ssh://git@` or scp-like `git@github.com:` remote, with or without `.git`; any other remote, including GitHub Enterprise hosts and local paths, is `ErrUnsupportedRemote` before git or gh runs.
Programs run with argument lists, never through a shell, with `GH_PROMPT_DISABLED=1` and `GIT_TERMINAL_PROMPT=0` so neither waits for input; a merge whose gh call exits unsuccessfully is `ErrRefused` with gh's standard error.
Platform differences: none in the contract.

## Tests

[`internal/forge/forgetest`](forge-forgetest.md) defines `RunForgeContract` and the double `Fake`.
`forge_test.go` runs the suite against `Fake` and against `GitHub` with real git pushing to a local bare repository, which the test's own git configuration maps to `https://github.com/acme/widgets.git` with `insteadOf`, and the test binary acting as gh over a state file, reading branch heads from the bare repository; the fake gh refuses any command or flag it does not know, so a change in what `GitHub` asks for fails.
`TestGitHubArguments` pins the gh calls: `--repo` on every call, `--head`, `--title` and `--body` on create, `--squash` and `--match-head-commit <head>` on merge, and never `--admin`, `--auto` or `--base`.
`TestGitHubRemotes` pins the remote forms and that an unsupported remote runs nothing.
`TestValidBranch` pins the branch-name rule.
`TestGitHubLive` runs the suite, without the refusal case, against a real sandbox repository only when `GUILDHALL_LIVE_FORGE_REMOTE` names one; CI never sets it, and it has not been run.
The contract and `GitHub` suites were written red first against stubs returning zero values ([red run](../evidence/delivery-stage-2026-10-22-red.txt)): every contract case of both the fake and GitHub, `TestGitHubArguments` and `TestGitHubRemotes` failed; `TestValidBranch` was written with `ValidBranch` and was not red.
