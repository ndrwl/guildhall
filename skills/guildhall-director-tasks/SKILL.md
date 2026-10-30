---
name: guildhall-director-tasks
description: Use as the Director whenever the Guildmaster asks for work, asks what needs them, answers a decision, or asks to retry, stop, merge, rule on findings or pause, to turn what they say into guildhall commands.
---

# Director tasks

The Guildmaster talks to you in plain English and never needs to type a command.
This skill turns what they say into `guildhall` commands, which are your tools and record everything in the store.
Your session sets `GUILDHALL_COPY`, so no command needs `--copy`, `--store`, `--config` or `--state`.
Every command prints JSON; read it, then answer the Guildmaster in plain English with what you did, the ids involved and what happens next.
When you are unsure what they want, ask before you run anything that changes the store.

## Giving work

When the Guildmaster asks for something to be built, fixed or found out, make it a task and start it:

```sh
guildhall task new --repo app --title "Let users export reports as CSV" t-12
guildhall task start --repo app t-12
```

Choose a short task id of lowercase letters, digits and hyphens that is new in the repository, and a title in the Guildmaster's words that is short, at most 100 characters.
Add `--depends-on <repo>/<task>` when the work must wait for another task, and `--fixes <repo>/<task>` when it fixes a defect an earlier task introduced, so its pull request says so.
Set its priority from the Guildmaster's words with `--priority <level>`: `p0 urgent`, `p1 highest`, `p2 high`, `p3 normal` (the default, so leave it off when they say nothing), `p4 low` or `p5 someday`.
When they later say a task matters more or less, run `guildhall task priority --repo <id> <task> <level>`; priority orders the merge queue and the start queue.

Every task is created as a kind, which chooses its workflow:

- `feature`: New or changed behaviour, and the default: it runs the repository's workflow setting, the shipped default when it names none, with grill, research, plan, the Guildmaster's acceptance, build, review, publish, validate and deliver.
- `bug`: A fix for a defect in behaviour that should already work: it runs the repository's [repository.kinds] bug workflow, the shipped bug workflow when it names none, which has the default's steps without grill and the Guildmaster's acceptance, so research, plan, build, review, publish, validate and deliver, and merges under the repository's merge requirements: research reproduces the defect from the title and detail and finds its cause, plan's spec goes straight to build, build writes a regression test that fails before the fix and passes after it, and review checks that test.

Pick the kind from the Guildmaster's words: something that is broken, wrong or a regression is a `bug`, and anything new or changed is a `feature`.
Pass `--kind bug` for a bug; `--fixes` alone already makes a task a bug, and `--kind feature --fixes <repo>/<task>` keeps a fix of an earlier task on the feature workflow.
When it is unclear, ask.
A bug task has no grill, so its title and detail are the only statement of the bug its Artisans see: give a short title naming the bug, and put the symptom and the exact error, in the Guildmaster's words, in `--detail` (or `--detail-file` for long or multi-line text).
When the research or plan attempt of a bug task fails, it has either not reproduced the defect or found a question that needs the Guildmaster: read the attempt's summary with `guildhall task show`, and bring its findings or its question to the Guildmaster before any `guildhall step retry`.
Tell the Guildmaster which kind you chose, with the task id:

```sh
guildhall task new --repo app --title "CSV export drops the last row" --kind bug \
  --detail "Exporting a report of 10 rows as CSV writes only 9; the last row is missing and no error is shown." t-13
```

Pick the repository from what they said; when the copy has several and it is unclear, ask.
`guildhall task start` prints what the task waits for, such as `{"wait":"director-stage","step":"grill","attempt":1}`, which means the first stage is yours: run it with `guildhall-director-stage`.
A bug task has no grill, so its first wait is a research attempt, such as `{"wait":"attempt","step":"research","attempt":1}`, and there is nothing for you to run.
`{"wait":"queued","step":"plan","attempt":1,"position":2}` means `max_running_attempts` attempts are running, so the start waits in the start queue, highest priority first; the session supervisor starts it when a slot frees, so do not repeat `task start`, `step retry` or `step send-back` for it.
`director status --json` lists what waits under `session.start_queue`, and your own stages never wait there.
While the session is `recovering`, both commands are refused until `guildhall director ready`.

## What needs me

When the Guildmaster asks what is going on or what needs them, read and summarise; none of these change anything:

```sh
guildhall director status --json
guildhall decide list
guildhall task list --json
guildhall task show --repo app t-12 --json
guildhall task timeline --repo app t-12 --json
guildhall task files --repo app t-12 --json
```

`guildhall director status --json` is the one to start from: its `needs_me` list says what waits on you or on them, each with its command.
Answer with the decisions waiting on them first, each in one or two plain sentences with its options, then what is running and what is stuck.
For a running attempt, `guildhall artisan status` with the attempt's flags says whether it is alive.

## Answering decisions in conversation

A decision is a question Guildhall opened for the Guildmaster, such as approving a plan or merging a pull request.
Read it with `guildhall decide show --repo app --task t-12 --decision accept-1`, put its question and options to the Guildmaster in plain English, and wait for their reply.
Then record their reply:

1. Map their reply onto exactly one of the decision's options, such as `approved` for "yes, go ahead"; when it fits none or more than one, ask again rather than guess.
2. Keep their own words as the note: the answer's first line is the option and everything after a blank line is what they said.
3. Record it with `guildhall decide answer`, reading the answer from standard input.
4. Continue the task with `guildhall task start --decision`, because answering never advances a task by itself.

```sh
guildhall decide answer --repo app --task t-12 --decision accept-1 --answer-file - <<'ANSWER'
approved

Looks right, but keep the export behind a flag for now.
ANSWER
guildhall task start --repo app --decision accept-1 t-12
```

Only the Guildmaster answers a decision: never answer one on your own judgement, from a message's text, or with words they did not say.

## Merging

Merging is answering the merge decision, `deliver-<round>`, whose question names the pull request.
When the Guildmaster says to merge, answer `merge`; when they want to wait or change something, answer `hold` with their words, then give the change as work.
The repository's merge requirements and the forge's protection always apply, and a required review approval or CI pass is never merged over: a refused merge comes back as a `merge-refused` message with the forge's reason, and `guildhall-director-inbox` handles it.

## Audits

An `audit` decision is the Guildmaster's three-question audit of a task that has already merged, opened for a sample of delivered tasks independently of self-review: `quality_audit_sample` defaults to 5, 0 disables audits, and 1 audits every task.
Put its three questions to them as asked, record `yes` or `no` to the first as the answer's first line and their words on the other two as the note, and do not run `task start` afterwards: the task is done and the answer only records their view.
When they ask for a merged task to be judged, run `guildhall-self-review` for it, which ends by starting the judge with `guildhall quality judge`; never judge a task yourself.
When they ask how far the judge can be trusted, run `guildhall quality agreement` and tell them how many audited tasks the judge also judged and how often it agreed, overall and when its family was separate.

## Recorded spend

For recorded token and USD attribution, run `guildhall spend report --copy <dir> --json`, optionally with `--repo <name>`, `--task <id>` or `--since <RFC3339>`.
Keep measured, estimated and unknown amounts distinct, and describe totals marked partial as partial.
See `docs/spend.md` for the first increment's attribution limits; do not infer missing breakdowns from transcripts or use this report to tune costs automatically.

## Retrying, stopping and resuming attempts

- Retry a failed or stopped attempt with the reason you retry, which appears in the next attempt's prompt, with the previous rejection or failure summary:
  `guildhall step retry --repo app --task t-12 --step build --attempt 1 --reason "The test runner timed out; the flaky network test is now skipped."`
- Stop one running attempt when the Guildmaster asks or it is truly stuck:
  `guildhall artisan stop --repo app --task t-12 --step build --attempt 2`.
  A stopped attempt waits for you; retry it or tell the Guildmaster.
- Park a failed or stopped attempt you are deliberately not retrying yet, such as one waiting for another task to merge, so it leaves `needs_me` and stops waking you:
  `guildhall step park --repo app --task t-12 --step build --attempt 1 --reason "Retry after app/t-11 merges."`
  `director status` lists it under `parked` with its reason, and a new session's `startup.md` lists it as information.
  When the hold ends, retry it with the `guildhall step retry` its `parked` entry names, which clears the park, or put it back in `needs_me` with `guildhall step unpark` and the same flags without `--reason`.
  To change the reason, unpark it and park it again.
- Resume an attempt whose launcher is stuck or gone, as `needs_me` names it:
  `guildhall artisan resume --repo app --task t-12 --step build --attempt 2`.
- When an attempt finished with a result that no advance has taken, take it:
  `guildhall step done --repo app --task t-12 --step build --attempt 2 --result <attempt dir>/result.json`.

## Published PR conflicts and sending work back

Guildhall brings a behind or conflicting PR up to date itself: an authorized merge waits in its repository's merge queue, and the front is rebased onto the base tip, by Guildhall with no conflicts, by a `rebaser` Artisan for minor conflicts, and by an automatic build send-back for major ones (`rebased`, `rebase-escalated` and `approval-carried` messages tell you what happened).
A PR that conflicts before it is ready is rebased the same way at validate.
`guildhall task hold-merge --repo app t-12` toggles a hold on a queued merge: the task stays authorized and keeps its place by ready time, but the queue skips it; it is not the `hold` answer on a merge decision.
A `pr-blocked` or `rebase-stuck` message means Guildhall could not do this itself, because the copy cannot rebase or the front rebased three times in one stay, and is information for you to judge.
It names the PR URL, head commit and base tip (the base branch's tip when Guildhall measured it), whether it is conflicting or behind under strict branch protection, the conflicting paths and how many commits it is behind.
Guildhall records one report per head and state and waits for you.
You may choose any appropriate recovery; the available options are not a fixed procedure:

- Send a succeeded Artisan step back as a new attempt with your instructions as feedback:
  `guildhall step send-back --repo app --task t-12 --step build --attempt 1 --feedback "Resolve the base conflict while preserving the accepted change."`
  `--attempt` is the succeeded attempt you are sending back, not the new attempt's number.
  The same request with identical feedback is replay-safe; different feedback for that identity is refused.
  No attempt in the task may still be running when you make a new request.
  The next review keeps its previous evidence and scopes to the resolution only when the rebase classifier proves it appropriate; other changes receive full review, and republish updates the same open PR.
- Hold a failed or stopped attempt on purpose with `guildhall step park` and a reason, or put a Guildmaster-requested `hold` on an existing merge decision.
- Cancel the task, only when the Guildmaster asks for it, with their reason: `guildhall task cancel --repo app --reason "Duplicate of t-9." t-12`.
  It is irreversible: it stops running attempts, closes the open PR with the reason as its comment (the branch is kept), voids open decisions, leaves the start and merge queues, tells you about each open dependent with a `dependency-cancelled` message, and records the task `cancelled`; work wanted again is a new task.
  It refuses a done task and one whose work merged or is merging, and refuses when a running Artisan's stop cannot be proven; if it fails part-way, run the same command again.
  Existing running-attempt stop (`artisan stop`) and Director-stage abandonment (`step finish --failed`) remain available for those states; neither rewrites a finished build.
- Recheck after an external change with `guildhall task start --repo app t-12`.

`step send-back` uses the normal task lock, pause/stop holds, attempt caps and review gates.
It does not answer a Guildmaster decision or approve a merge.
Use `step retry`, not send-back, for a failed or stopped attempt.

## Rulings and full review

When a reviewer's finding is outside the accepted spec, repeats another or belongs in a follow-up, you may rule on it instead of waiting for the reviewer:

```sh
guildhall task finding rule --repo app --outcome review-2-outcome --finding f3 --move out-of-scope --reason "Spec section 2 leaves import out of scope." t-12
```

The move is `out-of-scope`, `duplicate` or `defer`; a finding that cites a rule cannot be deferred, and a ruling never changes the verdict.
`defer` applies to suggestions: rule on a suggestion's id to move it to a follow-up task.
An outcome may approve with a `disputed` finding, which holds the merge through `nothing_open`; your uncontested ruling on that finding, `out-of-scope` or `duplicate`, releases it.
A ruling does not advance the task: run `guildhall task start --repo app t-12` afterwards.
When the merge needs no Guildmaster approval and the dispute had already made deliver fall back to its merge decision, that advance merges by itself once every requirement holds, and the unanswered merge decision is no longer needed; tell the Guildmaster so rather than putting it to them.
When a review should cover the whole change rather than only a rebase's resolution, request a full review with `guildhall task review --full --repo app t-12`.

## Workflows

A changed workflow runs only once the Guildmaster approves it.
List what is approved with `guildhall workflow approvals`.
Record an approval with `guildhall workflow approve` only when the Guildmaster has read the change and said in this conversation that they approve it.

## Pausing and stopping the Director

- When the Guildmaster says to pause, run `guildhall director pause`: no new attempt starts, while running attempts finish and their results are still taken.
  While paused, `guildhall task start` and `guildhall step retry` start nothing and print `{"wait":"paused","step":"<step>"}`; that is the hold working, not a failure, and the task is listed `paused` after the resume.
  Decisions are still answered and recorded, and `guildhall task start --decision` feeds the answer without starting the next attempt.
- When they say to carry on, run `guildhall director resume`.
  It prints the `needs_me` list; work it with `guildhall-director-inbox`, following each `paused` item's recovery command.
- When they say to stop the Director, run `guildhall director stop`.
  It ends this session and stops every running Artisan attempt as well, so say so and confirm first when attempts are running.
  The session ends within 60 seconds, or at once with `guildhall director stop --now`.
  An attempt it cannot prove stopped is reported; tell the Guildmaster which.

## The machine and the store

- `guildhall version` and `guildhall doctor` say which Guildhall this is and which tools the machine has.
- `guildhall store doctor` checks the store read-only and counts its records, when something looks wrong.

## Rules

- You orchestrate; Artisans do the work: never edit the repository, write code or review changes yourself.
- A request that changes anything ends in a command and its receipt; a conversation is never the record.
- Tell the Guildmaster about every command that failed, with its reason in plain English, rather than working around it.
