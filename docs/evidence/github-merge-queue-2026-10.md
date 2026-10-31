# Should Guildhall use a GitHub merge queue? (d24, 2026-10)

Task d24, build-1, 2026-10-29. Research for the plan in d24 plan-1. This document changes nothing; any adoption is a follow-up task the Director opens.

## 1. Question and answer up front

**Question.** Should Guildhall land its pull requests through a GitHub merge queue instead of today's synchronous `gh pr merge <n> --repo <owner/name> --squash --match-head-commit <head>` (`internal/forge/github.go:121`), compared with the status quo plus d23 (auto-rebase when a PR is only behind)?

**Answer: adopt later.** Keep the synchronous merge and do d23 now. Reconsider a queue only when both of these hold: `ndrwl/guildhall` is owned by an account type and plan on which GitHub confirms merge queues are available, and bursts of ready PRs are frequent enough that d23's serial landing (about one CI run per PR, §5) is a measured bottleneck.

Reasons:

- **Availability is probably a blocker, and is unconfirmed.** From memory, merge queues need an organization-owned repository. `ndrwl/guildhall` looks like a personal-account repository. Neither fact could be checked in this build (§3, §4).
- **A queue is not a configuration switch for Guildhall.** Guildhall's merge is synchronous. Turning a queue on without code changes would make every enqueued PR look like a refusal (§7).
- **d23 already removes the main cost of today's flow**: the Director recovering every `BEHIND` PR by hand. A queue's remaining gain is latency on bursts (about T instead of about 3T for three PRs, §5). That matters little at Guildhall's current PR rate.
- **The reviewed-head rule does not rule a queue out** (§6). The trade a queue makes is acceptable in principle. It is not the reason to wait.

**Limits of this document.** The build stage, like the research stage, had no web access and no `gh` access. WebFetch, WebSearch, `gh api`, `gh --version`, `gh pr merge --help` and `gh-axi` were all refused by the session's permissions before they ran. So every GitHub-behaviour claim in §3 is marked **unconfirmed, needs a check**, and so is every live-repository fact in §4. The recommendation is conditional on them, as plan-1's open question 1 allows. Every Guildhall code citation in §2 and §7 was re-checked against this build's base, `7d99b2ad`.

## 2. Guildhall today

All citations below were checked against `7d99b2ad`.

### The forge boundary and the "never `--auto`/`--admin`" pins

- The package doc says Guildhall merges only the head that its merge decision or `deliver-<k>-authorized` record is bound to, always with a squash merge, and that it "never bypasses the forge's branch protection, required checks or review rules" (`internal/forge/forge.go:5-9`). The contract says the same (`docs/contracts/forge.md:6`).
- `forge.Merge` carries `Head`, "the full commit id the merge was approved for; the forge merges only while the pull request is at it" (`internal/forge/forge.go:50-58`).
- `GitHub.Merge` (`internal/forge/github.go:94-141`):
  - Its doc comment says it "never passes --admin or --auto" (lines 94-95).
  - A PR already merged at the head is returned as it is (lines 115-116).
  - It runs one command, `gh pr merge … --squash --match-head-commit <head>` (line 121).
  - It then views the PR again. Success is only `State == Merged && Head == m.Head` with a valid `MergeCommit` (line 135).
  - A failed gh call is `ErrRefused` with gh's output (lines 137-138). A PR that is still not merged after gh succeeded is also `ErrRefused`: "is … at … after the merge" (line 140).
- `ErrRefused` lists failing or pending checks, a branch behind its base, protection, a moved head and a closed PR (`internal/forge/forge.go:100-103`).
- Tests pin the absence of `--auto` and `--admin`:
  - `TestGitHubArguments` is documented at `internal/forge/forge_test.go:207-208`. It fails on any call containing `--admin`, `--auto` or `--base` (line 243).
  - The strict gh stand-in refuses a merge with `--admin` or `--auto` with exit 2 (`internal/forge/gh_syntax_test.go:99-104`).
  - The contract records the pin (`docs/contracts/forge.md:62`).
  - D60 says the same: "never uses `--admin` or `--auto`" (`docs/decisions/delivery-stage.md:11`).

### Synchronous `run.merge`, with no "queued" state

`run.merge` (`internal/interp/deliver.go:438-481`) works like this:

1. It calls `Forge.Merge` once (line 451).
2. On `ErrRefused` it records `<id>-refused`, sends `merge-refused` and waits for the Director. Advancing again retries (lines 452-460).
3. On success it records `<id>-merge` with the merged PR, including its merge commit (line 464).
4. Then it sends `merged`, retires the worktree, opens the audit and marks the task done (lines 468-480).

There is no state for "merge requested, not landed yet": a merge has either happened or been refused.

Deliver reaches `run.merge` in one of two ways:

- **Unattended (#163).** With `guildmaster_approval = false` and every other requirement holding, deliver records `deliver-<k>-authorized` and merges by itself (`internal/interp/deliver.go:239-248`; `docs/configuration.md:167`).
- **Through the Guildmaster.** Otherwise it opens the merge decision.

### `mergeBlocked` and `BEHIND`

- `GitHub.Mergeability` maps `CONFLICTING`/`DIRTY` to "conflicting" and `BEHIND` to "behind". Everything else is clear (`internal/forge/mergeability.go:60-67`). Unknown states and a generic `BLOCKED` are not guessed (`docs/contracts/forge.md:42`).
- `mergeBlocked` (`internal/interp/mergeability.go:31-80`) records one observation per head and state and tells the Director. Its message says "Guildhall does not rebase automatically" (line 110).
- Validate calls it before reading checks (`internal/interp/validate.go:129-138`). Deliver calls it again before merging (`internal/interp/deliver.go:214-217`).
- "Behind" only appears when `main` requires branches to be up to date ("strict" protection). That is the case d23 targets.
- Validate reads checks for one commit, the reviewed head (`internal/interp/validate.go:139`; query at `internal/forge/github.go:143-162`, function at line 228).
- `docs/decisions/delivery-stage.md` leaves out two options on purpose:
  - a head option: "the delivered head is always the reviewed head, and a rebase gets a new review round" (line 38)
  - a merge method option: "squash stays fixed" (line 40)
- D42 gives a rebased range a narrowed review only when `range-diff` proves a pure rebase. A base change to a file the task touches gets a full review (`docs/decisions/rebase-classification.md:4-5`).

### CI

- `.github/workflows/ci.yml:3-7` triggers on `push` to `main`, `pull_request` and `workflow_dispatch`. **There is no `merge_group` trigger.**
- The matrix is ubuntu, windows and macos (lines 21-31).
- Concurrency is keyed on the ref and cancels in progress except on `main` (lines 12-14).

## 3. What a merge queue needs (Q1)

Plan-1 requires each claim to be confirmed with a source, or marked unconfirmed. This build could not reach GitHub Docs, the gh manual or gh source, or the GraphQL reference (§1, Limits). So **every claim below is unconfirmed, needs a check.** Each one is stated as what the research notes recalled, not as fact. The pages to check are named so the follow-up check task can confirm or correct each one.

| # | Claim (unconfirmed, needs a check) | Where to confirm |
|---|---|---|
| 3.1 | **Availability.** Merge queues are available for public repositories owned by an organization, and for private organization repositories on GitHub Enterprise Cloud. Repositories owned by a personal account are not eligible on any plan. | GitHub Docs, "Managing a merge queue": https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue |
| 3.2 | **The rule.** "Require merge queue" is a branch-protection or ruleset rule on the base branch. Its settings include the merge method (merge, squash or rebase, chosen by the queue rather than the caller), build concurrency, minimum and maximum group size, a wait time before merging a smaller group, "only merge non-failing pull requests", and a status-check timeout. Required checks must report on the queue's commits. | Same page; GitHub Docs, "Available rules for rulesets": https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets |
| 3.3 | **CI trigger.** Workflows that provide required checks must add `on: merge_group` (type `checks_requested`). Otherwise no check reports on the queue's commit, and every entry times out and is removed. The queue tests a temporary commit on a `gh-readonly-queue/<base>/pr-<n>-<sha>` branch, built from the base, the PRs ahead of it in the queue, and this PR. | "Managing a merge queue"; GitHub Docs, "Events that trigger workflows", `merge_group`: https://docs.github.com/en/actions/writing-workflows/choosing-when-your-workflow-runs/events-that-trigger-workflows#merge_group |
| 3.4 | **`gh pr merge` on a queue-protected branch.** It adds the PR to the queue when the requirements are met, and otherwise enables auto-merge, which enqueues it later. The strategy flags (`--squash` and the others) are not needed because the queue's method applies; whether passing one is an error is unknown. `--auto` explicitly asks for auto-merge or enqueueing. `--admin` merges directly, bypassing the queue, where the caller is allowed to. **Whether `--match-head-commit` is honoured when enqueuing is unknown.** The GraphQL `enqueuePullRequest` mutation is recalled as taking `expectedHeadOid`. | gh manual: https://cli.github.com/manual/gh_pr_merge; gh source, `pkg/cmd/pr/merge/merge.go` (https://github.com/cli/cli); GraphQL mutations reference: https://docs.github.com/en/graphql/reference/mutations#enqueuepullrequest |
| 3.5 | **The result.** After a queue merge the PR's `state` is `MERGED`. Its `headRefOid` stays the head that was queued, because the queue does not push to the PR branch. `mergeCommit` is the commit that landed on the base, which is the commit the queue tested. On a failure, a timeout, a failure of a PR ahead of it in its group, or a new push to the PR branch, the PR is removed from the queue and stays `OPEN`, and GitHub records a removal timeline event (recalled as `RemovedFromMergeQueueEvent`). Queue status is readable through fields recalled as `isInMergeQueue` and `mergeQueueEntry` on `PullRequest`. A merge by the queue pushes to `main`, so the existing `push: main` CI run still follows. | GraphQL objects reference, `PullRequest` and `MergeQueueEntry`: https://docs.github.com/en/graphql/reference/objects#pullrequest; `gh pr view --help` for which of these `--json` exposes |

If 3.1 is confirmed and the owner is a personal account (§4), §5-7 are only relevant after a transfer to an organization. A transfer changes the remote in each Guildhall configuration, release URLs and links. That is a cost of its own, which this document does not size.

## 4. The live repository, read-only

None of the three facts could be read. Plan-1 allows only `gh api` GETs. These were attempted in this build, and the session's permission layer refused each one before it ran: the reply was "This command requires approval", so no request reached GitHub.

| Fact | Command (GET only) | Result |
|---|---|---|
| Owner type and visibility | `gh api repos/ndrwl/guildhall --jq '{owner_type: .owner.type, visibility: .visibility, private: .private, default_branch: .default_branch}'` | Could not be read: not run, needs approval |
| Rules on `main`, including any merge-queue rule | `gh api repos/ndrwl/guildhall/rules/branches/main` | Could not be read: not run, needs approval |
| Classic protection on `main`, including `required_status_checks.strict` ("require branches to be up to date") | `gh api repos/ndrwl/guildhall/branches/main/protection` | Could not be read: not run, needs approval |

What the repository itself suggests, which is not a substitute for the reads above:

- The git user is a person, and the remote is `ndrwl/guildhall`. That suggests a personal account, but the owner type is **unconfirmed**.
- Guildhall has a `pr-blocked … behind` path, and d23 exists. That suggests strict protection has been seen on `main`, but the setting is **unconfirmed**.

Nothing in the live repository was changed.

## 5. Comparison (Q2)

**The scenario.** Three PRs, A, B and C, are each reviewed and green at their own heads against the same `main`. They become ready to deliver together, and none of them conflicts textually with the others.

**T** is one full 3-OS CI run, and the Windows leg sets it. `docs/evidence/windows-ci-speed-2026-10-22-timings.md` gives Windows `go test` wall times of 249 s and 365 s on main before the speed work, and 91 s and 176 s after it. Hosted runners vary by about 2x. Taking setup on top, **T is about 3-6 minutes.**

CI-run counts are full 3-OS matrix runs. They exclude the three runs at the original heads, which every option shares, and the `push: main` run after each landing, which every option also shares (for the queue that is unconfirmed, claim 3.5).

| Option | Latency until all three land | Extra CI runs | Failure modes | Director load |
|---|---|---|---|---|
| **Status quo, no strict protection** | About 0: A, B and C merge back to back, each in one synchronous call | 0 | B and C land on a base their CI never saw. A semantic conflict breaks `main`, and only the following `push: main` run shows it. A textual conflict makes the next PR "conflicting", which goes to the Director. | None, unless something conflicts |
| **Status quo, strict protection** | About 2T plus three Director recoveries and up to three review rounds. A lands; B and C go `BEHIND` and the Director is told (`pr-blocked`). B is rebased, re-reviewed (D42 narrowed or full), re-run and landed. Then C goes `BEHIND` again and repeats. | 2-3 (B once; C once or twice, depending on when it was rebased) | Rebase conflicts. Review churn on every rebase. A base change to a touched file forces a full review. | High: a manual recovery for each `pr-blocked`, which is the cost d23 exists to remove |
| **Status quo + d23 (strict)** | About 2T plus two automated rebases and their review rounds. Same sequence as above, without the Director. | 2 (or 3 if C is rebased after both A and B) | Rebase conflicts go to the Director. Review churn. A full review on a touched-file base change. Guildhall's own merges race each other, so it rebases onto a base that is about to move again. | Low: only conflicts and full reviews |
| **Merge queue** (concurrency ≥ 3, squash; all unconfirmed, §3) | About T when all three pass: the queue tests `main+A`, `main+A+B` and `main+A+B+C` in parallel, or as one group. One failure removes that PR and re-runs those behind it: about 2T. | 3 `merge_group` runs (fewer when grouped; more after a removal) | Removal on a failing group, a push to the branch, or a check timeout. **Every entry times out if `ci.yml` lacks `merge_group`.** A new asynchronous state that Guildhall cannot see today. The landed tree is never reviewed, though CI tests it. Unavailable on personal accounts (unconfirmed). | Low once supported: removals only. Without code changes, false `merge-refused` messages (§7). |

Reading the table:

- The queue wins on latency only when PRs arrive in bursts, by about T per extra PR.
- d23 wins on cost of change, and on not needing an organization.
- At today's rate, where the Director usually delivers one PR at a time, the 2T gap rarely arises.

## 6. The exact-reviewed-head rule

**Verdict: the queue's trade is acceptable**, on two conditions. The authorization and decision records must keep binding the PR head and record the landed commit separately. And the follow-up must confirm claim 3.4's head binding, or replace it with the GraphQL `expectedHeadOid` (unconfirmed).

The reasoning:

- **What the rule binds today.** Guildhall binds a merge to the PR's head (`--match-head-commit`, `internal/forge/github.go:121`), not to the commit that lands on `main`. The merge requirements decision, now in [delivery-stage.md](../decisions/delivery-stage.md), ("the delivered head is always the reviewed head") is a rule about the head. A queue does not change the PR's head (claim 3.5, unconfirmed), so the rule still holds under a queue.
- **What lands today without strict protection.** A squash merge lands the reviewed changes on whatever `main` has become. That tree was never reviewed as a whole and **never tested**. So today's flow can already land an untested combination.
- **What lands with strict protection, with or without d23.** The landed tree is a rebased head. It was re-reviewed, but the review is only narrowed when range-diff proves a pure rebase (D42). It was also tested as landed.
- **What lands with a queue.** The landed tree is the reviewed head stacked on the PRs ahead of it. Guildhall never reviewed that tree, but CI tested it as landed. Compared with no strict protection, that is strictly better: the same review binding, plus testing. Compared with strict protection plus d23, it trades a narrowed review of the rebase for a CI test of the combination. A narrowed review of a pure rebase does not examine the interaction with the new base anyway, so little review is lost.
- **Interaction with #163 unattended merges.** With `guildmaster_approval = false`, deliver records `deliver-<k>-authorized` bound to the head and merges by itself (`internal/interp/deliver.go:243-248`). Under a queue, that becomes an unattended *enqueue*, and the commit that lands is chosen later by the queue. The authorization still binds the head, not the landed commit, exactly as today. Two things are new:
  - Time passes between authorization and landing.
  - The landed commit includes other tasks' changes.

  An adoption should therefore record the queue's `mergeCommit` in `<id>-merge`, as today (`internal/interp/deliver.go:464`). It should also state, in `docs/configuration.md` near line 167, that the authorization covers the head and not the landed commit.

## 7. Cost of adoption

**Warning: turning on a merge queue for `main` without code changes produces false `merge-refused` messages.** The effect depends on gh behaviour that is unconfirmed (claim 3.4):

1. `gh pr merge … --squash --match-head-commit <head>` would enqueue the PR, or enable auto-merge, and exit 0. It might instead fail on the strategy flag.
2. The re-view sees the PR still `OPEN`.
3. `GitHub.Merge` returns `ErrRefused` "… after the merge" (`internal/forge/github.go:140`).
4. Deliver records a refusal and wakes the Director (`internal/interp/deliver.go:452-460`).
5. Each re-advance calls gh again on a PR that is already queued. Only after the queue lands the PR does the view see `MERGED` at the head, and only then is the task done.

The code, contract, test and CI changes a real adoption needs (research notes §3):

- **Forge.** A result for "queued" that is distinct from merged and refused, and a way to read queue state and removal. That means `isInMergeQueue`/`mergeQueueEntry` or polling the existing `view` (claim 3.5). Possibly an `enqueuePullRequest` call with `expectedHeadOid` instead of `gh pr merge`, if gh does not honour `--match-head-commit` when enqueuing (claim 3.4).
- **Interpreter.** A new deliver wait, like the checks wait, that polls until the PR is merged or removed. A removal maps onto the existing refusal and Director path. `<id>-merge` still records the landed `mergeCommit`.
- **Mergeability.** Under a queue `BEHIND` is no longer a blocking state (unconfirmed), so `pr-blocked … behind` (`internal/forge/mergeability.go:63-64`, `internal/interp/mergeability.go`) would stop firing. That overlaps with d23's purpose.
- **Pins to change on purpose:**
  - `internal/forge/forge_test.go:243`
  - `internal/forge/gh_syntax_test.go:99-104`
  - `docs/contracts/forge.md:62`
  - the package doc and the merge comment (`internal/forge/forge.go:5-9`, `internal/forge/github.go:94-95`)
  - D60's "never uses `--admin` or `--auto`" (`docs/decisions/delivery-stage.md:11`)
  - a new decision recording the change
- **Merge method.** The queue's method must be squash, to keep the history D60 fixes. Guildhall has no merge-method option by design (`docs/decisions/delivery-stage.md:40`), so it would have to check or document the queue's setting instead.
- **CI.** Add `merge_group` to `.github/workflows/ci.yml:3-7`. Check that the concurrency key (lines 12-14) does not cancel one queue run with another. Each queue entry costs a full 3-OS run.
- **Repository.** An organization owner, if claim 3.1 holds. A trial needs a throwaway organization repository, or is recorded as "needs a live trial". The live repository stays unchanged.

## 8. Recommendation

**Adopt later.**

**The condition that would change it** to "adopt" is all three of these:

1. GitHub confirms that merge queues are available for the repository's owner type and plan (claims 3.1 and 4).
2. The repository is, or moves to, such an owner.
3. Bursts of several ready PRs are frequent enough that d23's serial landing, about T per PR plus a review round, is a measured bottleneck.

If check 1 shows that personal-account repositories are eligible, only condition 3 remains.

**Follow-up tasks for the Director to open:**

1. **Keep d23.** Auto-rebase for behind-only PRs is the path for strict protection, whatever happens to the queue.
2. **Verify d24's unconfirmed facts.** This is read-only, and needs web and `gh` access. Confirm or correct each claim in §3 against the linked sources. Run the three GETs in §4 and quote their output. Amend this document. If the owner is a personal account and claim 3.1 holds, the recommendation stays "adopt later, after moving to an organization".
3. **Only when the condition above holds: adopt a merge queue.** Make the code, contract, test and CI changes in §7. Start with a trial on a throwaway organization repository, and only then make any change to the live repository's rules.
