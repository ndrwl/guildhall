# `internal/forge`

## Contract

The boundary to the host of a repository's pull requests, called by the interpreter's [publish and deliver](../interpreter.md#publish-and-deliver) and [validate](../interpreter.md#validate) through its `interp.Forge` seam.
Guildhall prescribes only the edges: it pushes only a task branch, never the base; it merges only the head its merge decision or recorded merge authorization (`deliver-<k>-authorized`) is bound to, with a squash merge; and it never bypasses the forge's branch protection, required checks or review rules ([decision](../decisions/0060-delivery-stage.md), [merge requirements](../decisions/merge-requires.md)).
Credentials stay in the forge program's own sign-in store: nothing here reads, stores or passes one (R1.10, R1.28).

`Publish(ctx, Change)` pushes `Change.Head` to `refs/heads/<Change.Branch>` on `Change.Remote`, replacing whatever that task branch held, and returns the branch's open pull request, opening one against the remote's default branch when none is open.
`Change.Title` and `Change.Body` describe the pull request: publishing sets them on a new one and on the branch's open one, overwriting any edit made to the title or body on the forge; an open pull request that already reads them is not edited, so a replay costs no call.
It refuses, pushing nothing:

- a change missing its repository, remote or title, with a title over `MaxTitle` (256) characters or a body over `MaxBody` (65,536) bytes, or naming an invalid branch (`ValidBranch`) or a head that is not a full commit id: `ErrInvalid`;
- a branch that is the remote's default branch: `ErrInvalid`;
- a head that is not a commit of `Change.Repository`: `ErrUnknownHead`.

Repeating a change returns the same pull request, and a new head on the same branch, even one that rewrites it, updates the open pull request, its title and its body rather than opening another.
The title and body are not part of `PullRequest`, so they stay out of every recorded document.
The returned pull request is at the head just published, because a forge may report its head a moment after the push.

`Merge(ctx, Merge)` squash-merges pull request `Merge.Number` only while it is at `Merge.Head`, and returns it merged with its merge commit.
A pull request already merged at that head is returned as it is, so repeating a merge is safe.
A forge refusal (required checks, protection, a branch behind its base, a pull request no longer at the head), a closed pull request, one merged at another head, or one not merged after the forge answered, is `ErrRefused` with the forge's reason in the error text; nothing was merged.
A merge the forge fails is checked by viewing the pull request again: merged at the head, it is returned; still open, closed or at another head, the failure is the forge's refusal, `ErrRefused` with its reason.
A malformed merge is `ErrInvalid`; a failure that leaves the forge's answer unknown, such as a missing program, a failed sign-in, or a view that fails after the merge failed, is returned as a plain error and is not `ErrRefused`.
`Checks(ctx, Commit)` returns every check and commit status the forge reports for `Commit.Head`, sorted by name, URL and then id, each `pending`, `passed` or `failed` with its details URL when the forge gives one and `ID`, the forge's id of that run of the check, which a re-run replaces ([decision](../decisions/0066-validate-stage.md)); `forge.GitHub` reports a check run's or commit status's GraphQL node id.
It reads the checks of that commit, not of whatever a pull request is at now, so a later push cannot answer for an earlier head.
No checks is an empty list.
A malformed commit is `ErrInvalid`; a head the remote does not hold is `ErrUnknownHead`.
A check also carries `Job`, the GitHub Actions job id of a check run the `github-actions` app produced, and empty for any other check run or commit status; it is tagged `json:"-"`, so a recorded check, and with it every recorded `checks` list and `checks.json`, is byte-identical to before.
`Close(ctx, Close)` closes pull request `Close.Number` without merging it, posting `Close.Comment`, and returns it closed; [`task cancel`](../tasks.md#task-cancel) calls it for a cancelled task ([decision](../decisions/task-cancel.md)).
It never deletes the branch.
A pull request already closed is returned as it is, so repeating a close is safe and posts nothing; a merged one is `ErrRefused`, closing and commenting nothing.
A close missing its repository, remote or comment, or naming no pull request, is `ErrInvalid`; a close the forge fails is checked by viewing the pull request again, as a merge is: closed, it is returned, otherwise the failure is returned as a plain error.
`PullRequest(ctx, View)`, optional on the forge as `interp.PullRequestReader`, returns pull request `View.Number` as the forge reports it now, open, closed, or merged at whatever head with its merge commit, and changes nothing; the [outside-merge rule](../tasks.md#merged-outside-guildhall) reads a task's recorded pull requests through it, separately from `Merge`, which stays strict.
A view missing its repository or remote, or naming no pull request, is `ErrInvalid`.
`forge.GitHub` and `forgetest.Fake` implement it, and the contract suite reads a pull request open, merged and closed through it; the Fake's `MergeOutside` and `CloseOutside` merge or close a pull request as someone outside Guildhall would.
No operation is safe for concurrent calls on one branch; the interpreter calls them under the task's lock.

`GitHub.CheckLog(ctx, Commit, job, limit)` implements the optional `interp.CheckLogReader` ([failed job logs](../interpreter.md#validate), [decision](../decisions/failed-job-logs.md)): it reads the log of Actions job `job` and returns a `CheckLog`, its `Text`, at most `limit` bytes of the log's tail, and `Dropped`, the bytes cut from its head.
gh's stdout streams through a `TailWriter`, so memory stays within twice the limit however long the log; a log cut short starts after the first newline kept, so no line or UTF-8 rune is split, and a single line longer than the limit is cut at a rune boundary.
An empty job is `ErrLogUnavailable` ("not a GitHub Actions job") and a limit of zero or less is too ("log fetching is off"), both without running gh; a job that is not all digits is `ErrInvalid`.
Any gh failure, such as an HTTP 403, 404 or 410, gh not signed in (exit 4) or a run past its timeout, is a `LogUnavailableError`, `ErrLogUnavailable` with gh's message as its reason (`LogReason`); an HTTP 404 or 410 is also `ErrLogExpired`.
A cancelled context is returned as it is.
`Forge` itself does not change: `forgetest.Fake` and `forge.GitHub` implement `CheckLog`, and a forge without it gives no logs.

`GitHub` is the implementation for `github.com`: git, found on `PATH` unless `GitHub.Git` names it, pushes with `git -C <repository> push --porcelain <remote> +<head>:refs/heads/<branch>`, and gh, found on `PATH` unless `GitHub.GH` names it, reads the default branch as described below, lists the branch's open pull request with its title and body, creates one with `--head`, `--title` and `--body` (no `--base`, so the default branch), edits an open one whose title or body differs with `gh pr edit <n> --title <title> --body <body>`, views it and merges it with `gh pr merge <n> --squash --match-head-commit <head>`, and closes it with `gh pr close <n> --repo <owner/repo> --comment <comment>`.
Checks are read in one call, `gh api graphql --hostname github.com` with the owner, name and head as `-f` fields, of the commit's `statusCheckRollup`, reading each check run's `databaseId` and `checkSuite { app { slug } }` for its `Job`: a check run queued, in progress, waiting, pending or requested is pending; completed, its conclusion success, neutral or skipped is passed and failure, cancelled, timed out, action required, stale or startup failure is failed; a commit status, named by its context, is pending when expected or pending, passed on success and failed on failure or error.
An unknown kind, status, conclusion or state, a missing repository, or more than the one page of 100 contexts the query reads is a plain error rather than a partial or guessed answer; a missing object, or one that is not a commit, is `ErrUnknownHead`.
A job's log is read with `gh api repos/<owner>/<repo>/actions/jobs/<job>/logs --hostname github.com --allow-escape-sequences`, the REST endpoint that answers with a redirect to the plain-text log, which gh's HTTP client follows, bounded by `LogTimeout` (2 minutes), or a shorter `GitHub.Timeout`.
A job log holds the ANSI colour codes its steps printed, and gh refuses to print escape sequences without `--allow-escape-sequences`; the log is kept as GitHub serves it, a UTF-8 byte order mark first and CRLF line ends included.
The default-branch lookup names the repository positionally as `gh repo view github.com/<owner>/<repo> --json defaultBranchRef`; `repo view` does not accept `--repo`.
Every pull-request gh call names `--repo github.com/<owner>/<repo>`, which `GitHubRepository` derives from an `https://`, `ssh://git@` or scp-like `git@github.com:` remote, with or without `.git`; any other remote, including GitHub Enterprise hosts and local paths, is `ErrUnsupportedRemote` before git or gh runs.
Programs run with argument lists, never through a shell, with `GH_PROMPT_DISABLED=1` and `GIT_TERMINAL_PROMPT=0` so neither waits for input, and each run is bounded by `GitHub.Timeout`, `DefaultTimeout` (5 minutes) when zero: a run past it is ended and is a plain error naming the program and the timeout, never `ErrRefused`, and the operation returns without viewing the pull request again, so nothing is recorded and the next advance asks again; a merge whose gh call exits unsuccessfully and whose pull request gh then views still unmerged is `ErrRefused` with gh's standard error, while gh's exit 4, a missing or failed sign-in, and a failing view are returned as they are.
Platform differences: none in the contract.

`GitHub.Mergeability` observes an open PR at the requested full head using the command-specific `pr view` JSON flags, with the PR number positional and repository selected by `--repo`.
It returns URL, head, base tip, conflicting or strict-protection-behind state, sorted unique conflict paths and a behind count.
The `pr view` fields are `url,state,headRefOid,baseRefName,mergeable,mergeStateStatus`; GitHub's recorded base commit goes stale, so it is not read.
The base tip is the base branch's commit on the remote when measured, and is empty when no obstruction is reported.
Unknown mergeability and generic protection `BLOCKED` are not guessed to be conflicts or strict-behind states.
A moved PR is `ErrRefused`; malformed requests are `ErrInvalid`, and unsupported remotes and operational errors remain errors.
For a blocked observation it reads the base tip with `git ls-remote <remote> refs/heads/<baseRefName>`; a missing, ambiguous or invalid tip is an error, not a guessed state.
It fetches that exact tip without updating branches or `FETCH_HEAD`, counts base-only commits and reads `merge-tree --write-tree --name-only -z`, including stdout on conflict exit 1.
It never modifies a worktree or rebases; concurrent observations only write git objects.
The base tip it returns is the one the interpreter's [tiered rebase](../interpreter.md#tiered-rebase) replays onto, through [`internal/git`](git.md#rebase), so the measurement and the rebase see the same base; `Publish` then force-pushes the rebased head to the same pull request.

## Tests

`TestGitHubMergeabilityEvidence` crosses real git and the strict command-specific gh stand-in: a genuinely divergent base produces conflict paths and a behind count without changing HEAD or worktree; conflicting, strict-behind, unknown, generic-blocked and moved-head cases are checked.
The mergeability command shape also crosses installed gh's offline help parser when available.
These forge tests were added after implementation as integration evidence, not claimed as red-first history.


[`internal/forge/forgetest`](forge-forgetest.md) defines `RunForgeContract` and the double `Fake`.
`forge_test.go` runs the suite against `Fake` and against `GitHub` with real git pushing to a local bare repository, which the test's own git configuration maps to `https://github.com/acme/widgets.git` with `insteadOf`, and the test binary acting as gh over a state file, reading branch heads from the bare repository; the fake gh refuses any command or flag it does not know, so a change in what `GitHub` asks for fails.
`TestGHCommandShapes` exercises each gh call shape and rejects unsupported flags, missing flag values and extra positional arguments before the stand-in can mutate state.
The strict stand-in reproduced `repo view --repo` through `TestGitHubArguments` before the positional-argument fix ([red run](../evidence/forge-gh-syntax-2026-10-26-red.txt), [green run](../evidence/forge-gh-syntax-2026-10-26-green.txt)); the new command-shape and installed-help tests passed on their first run.
The audit and its limits are recorded in the [command-syntax decision](../decisions/forge-gh-command-syntax.md).
`TestInstalledGHFlags` additionally checks those flags, and `TestGitHubArguments` checks its recorded calls, against an installed gh with `--help`, without credentials or network; this checks flag parsing, not positional arity or server semantics.
The installed-gh checks skip when gh is not on `PATH`; the stand-in checks still run.
`TestGitHubArguments` pins the repository arguments described above, the checks query's `--hostname github.com`, owner, name and head, `--head`, `--title` and `--body` on create, the number, `--title` and `--body` on edit, `--squash` and `--match-head-commit <head>` on merge, and the absence of `--admin`, `--auto` and `--base`.
`TestGitHubCloseArguments` pins `pr close` of the number with `--repo` and `--comment`, the absence of `--delete-branch` and `-d`, and that a repeated close runs no second `pr close`; `TestGHCommandShapes` makes the stand-in refuse `pr close` with `--delete-branch` or `-d`.
The close cases were added after the implementation; their [failure check](../evidence/task-cancel-2026-10-29-red.txt) ran them against stubs.
`TestGitHubMergeSignedOut` pins that a merge the fake gh refuses with exit 4, signed out, is a plain error and not `ErrRefused`, and `TestGitHubMergeUnreachable` that a merge failing with exit 1 whose follow-up view fails is a plain error too.
`TestGitHubMergeTimeout`, red first against the `GitHub` without a timeout ([red run](../evidence/h10-exit-forge-2026-10-22-red.txt)), runs the fake gh hanging on merge past a 500 ms `Timeout`: the merge returns a plain error naming the program and the timeout, not `ErrRefused`, and the remote's `main` is unmoved.
`TestGitHubCheckStates` pins the mapping of every check run status and conclusion and commit status state, the sorting, and the errors for an unknown state or kind and for a second page.
`TestGitHubRemotes` pins the remote forms and that an unsupported remote runs nothing.
`TestValidBranch` pins the branch-name rule.
`TestGitHubLive` runs the suite, without the refusal case, against a real sandbox repository only when `GUILDHALL_LIVE_FORGE_REMOTE` names one; CI never sets it, and it has not been run.
The contract and `GitHub` suites were written red first against stubs returning zero values ([red run](../evidence/delivery-stage-2026-10-22-red.txt)): every contract case of both the fake and GitHub, `TestGitHubArguments` and `TestGitHubRemotes` failed; `TestValidBranch` was written with `ValidBranch` and was not red.
`TestGitHubCheckStates` also pins that a check run's and a commit status's node id is the check's `ID`; the run ids, the contract's re-run case included, were added after the implementation, extending its cases, and were not run red.
`TestGitHubCheckStates` also pins that only a `github-actions` check run has a `Job`, from its `databaseId`, and that a check's JSON holds none.
`TestTailWriter` pins the tail under, at and over the cap, a cut mid-line, small streamed writes, cuts mid-rune, and a byte order mark and CRLF line ends, kept under the cap and cut after a line's LF over it; `TestGitHubCheckLog` pins the logs call through the stand-in, which answers `api repos/<owner>/<repo>/actions/jobs/<job>/logs --hostname github.com --allow-escape-sequences` with the log as gh prints it once it has followed the redirect, and, like gh, refuses a log holding an escape sequence without that flag; it pins a log with a byte order mark, CRLF and colour codes read byte for byte, and the unavailable cases: an empty job running no gh, an unknown or expired job (404, 410, also `ErrLogExpired`), a forbidden one (403), gh signed out, a hung read past a 500 ms `Timeout`, and a cancelled context returned unwrapped; `TestFakeCheckLog` pins the double's same cap and call count, and `TestGitHubArguments` the exact logs call.
These cases were written after the implementation; they were then run red against `CheckLog` and the job mapping stubbed to zero values ([red run](../evidence/failed-job-logs-2026-10-29-red.txt)), and are not claimed as red-first history.
The redirect itself is gh's: its `api` command follows the endpoint's 302 to the log's storage URL, and the stand-in models gh's output after the redirect.
It was confirmed against a live failed job on 2026-10-30 ([evidence](../evidence/failed-job-logs-redirect-2026-10-30.txt)): with `--allow-escape-sequences` gh printed the plain-text log, a byte order mark first, CRLF line ends and timestamped `##[group]Run` and `##[error]` lines, and exited 0; without the flag it refused the log's escape sequences and exited 1.
gh's message for a log past retention was not gathered there; the 404 and 410 cases rest on gh's usual `(HTTP <code>)` error form.
The checks cases, `TestGitHubCheckStates` and the checks call in `TestGitHubArguments` were written red first against `Checks` stubs returning no checks and no error ([red run](../evidence/validate-stage-2026-10-22-red.txt)); the no-checks case passed against the stubs and pins rather than proves the empty answer.
