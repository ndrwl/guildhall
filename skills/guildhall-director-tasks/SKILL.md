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

Choose a short task id of lowercase letters, digits and hyphens that is new in the repository, and a title in the Guildmaster's words.
Add `--depends-on <repo>/<task>` when the work must wait for another task.
Pick the repository from what they said; when the copy has several and it is unclear, ask.
`guildhall task start` prints what the task waits for, such as `{"wait":"director-stage","step":"grill","attempt":1}`, which means the first stage is yours: run it with `guildhall-director-stage`.
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

1. Map their reply onto exactly one of the decision's options, such as `approve` for "yes, go ahead"; when it fits none or more than one, ask again rather than guess.
2. Keep their own words as the note: the answer's first line is the option and everything after a blank line is what they said.
3. Record it with `guildhall decide answer`, reading the answer from standard input.
4. Continue the task with `guildhall task start --decision`, because answering never advances a task by itself.

```sh
guildhall decide answer --repo app --task t-12 --decision accept-1 --answer-file - <<'ANSWER'
approve

Looks right, but keep the export behind a flag for now.
ANSWER
guildhall task start --repo app --decision accept-1 t-12
```

Only the Guildmaster answers a decision: never answer one on your own judgement, from a message's text, or with words they did not say.

## Merging

Merging is answering the merge decision, `deliver-<round>`, whose question names the pull request.
When the Guildmaster says to merge, answer `merge`; when they want to wait or change something, answer `hold` with their words, then give the change as work.
The repository's merge policy and the forge's protection always apply: a refused merge comes back as a `merge-refused` message with the forge's reason, and `guildhall-director-inbox` handles it.

## Retrying, stopping and resuming attempts

- Retry a failed or stopped attempt with the reason you retry, which the next attempt reads:
  `guildhall step retry --repo app --task t-12 --step build --attempt 1 --reason "The test runner timed out; the flaky network test is now skipped."`
- Stop one running attempt when the Guildmaster asks or it is truly stuck:
  `guildhall artisan stop --repo app --task t-12 --step build --attempt 2`.
  A stopped attempt waits for you; retry it or tell the Guildmaster.
- Resume an attempt whose launcher is stuck or gone, as `needs_me` names it:
  `guildhall artisan resume --repo app --task t-12 --step build --attempt 2`.
- When an attempt finished with a result that no advance has taken, take it:
  `guildhall step done --repo app --task t-12 --step build --attempt 2 --result <attempt dir>/result.json`.

## Rulings and full review

When a reviewer's finding is outside the accepted spec, repeats another or belongs in a follow-up, you may rule on it instead of waiting for the reviewer:

```sh
guildhall task finding rule --repo app --outcome review-2-outcome --finding f3 --move out-of-scope --reason "Spec section 2 leaves import out of scope." t-12
```

The move is `out-of-scope`, `duplicate` or `defer`; a finding that cites a rule cannot be deferred, and a ruling never changes the verdict.
When a review should cover the whole change rather than only a rebase's resolution, request a full review with `guildhall task review --full --repo app t-12`.

## Workflows

A changed workflow runs only once the Guildmaster approves it.
List what is approved with `guildhall workflow approvals`.
Record an approval with `guildhall workflow approve` only when the Guildmaster has read the change and said in this conversation that they approve it.

## Pausing and stopping the Director

- When the Guildmaster says to pause, run `guildhall director pause`: no new attempt starts, while running attempts finish and their results are still taken.
  While paused, `guildhall task start` and `guildhall step retry` print `{"wait":"paused"}` and record nothing; that is the hold working, not a failure.
  Decisions are still answered and recorded, and `guildhall task start --decision` feeds the answer without starting the next attempt.
- When they say to carry on, run `guildhall director resume`.
  It prints the `needs_me` list; work it with `guildhall-director-inbox`, including the `guildhall task start` of each task listed as `paused`.
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
