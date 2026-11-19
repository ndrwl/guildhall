# Decisions

A decision durably records a question for the Guildmaster and its answer (R1.61).
The record is described in [the store](store.md#records); this page is the command line for one machine's Director store.
`guildhall decide list` is the per-machine inbox of waiting decisions (R1.20); there is no push service.

## Commands

```
guildhall decide list --store <path> [--repo <id>]
guildhall decide show --store <path> --repo <id> --task <id> --decision <id>
guildhall decide answer --store <path> --repo <id> --task <id> --decision <id> --answer-file <path|->
guildhall decide defer --store <path> --repo <id> --task <id> --decision <id> --until-merged <repo>/<task>... --reason <text>
guildhall decide undefer --store <path> --repo <id> --task <id> --decision <id>
guildhall decide open --store <path> --repo <id> --task <id> (--question <text> | --question-file <path|->) [--option <text>]... [--recommend <option>] [--config <director.toml> --grant-option <option> --grant-step <id|all> --grant-tool <rule>... [--grant-until attempt|done] --grant-reason <text>]
guildhall decide explain <repo>/<task> <decision> --store <path> --subject-digest <digest> (--text <md> | --file <path|->) [--recommend <option>]
guildhall decide accept <repo>/<task> <decision> --director --reason <text> (--copy <dir> | --store <path> --config <path> --state <dir>)
```

Every command needs an existing store and never creates one; `list` and `show` open it read-only.
Each prints one JSON document on standard output and exits 0, exits 1 when the answer or the store refuses or fails, with the reason on standard error, and exits 2 on a usage error.

- `list` prints a JSON array of the unanswered decisions, of one repository with `--repo` or of every repository, ordered by repository, task and decision id.
  Each entry is a [decision record](store.md#records); optional fields are omitted when absent, including a question's subject and any empty options.
  An empty inbox prints `[]`.
- `show` prints one decision, answered or not, including the answer, choice and note when non-empty, and, for an answer given on the [task page](web.md#answering), `answered_via` (`page`) and `answered_by` (the Tailscale login), or, for a plan the Director [accepted](#decide-accept), `answered_via` (`director`) alone.
  It also prints `subject_digest`, the digest an [explanation](#decide-explain) of the decision must be written against (`SubjectDigestOf`: its subject's digest, or, for a decision with no subject, `sha256:` and the hex sha256 of its question text); copy it rather than computing it.
  When the Director has explained it, an `explanation` object follows: the [explanation record](#decide-explain); a decision with none has no `explanation` field.
- `answer` records the Guildmaster's words and prints the store receipt; it records no `answered_via` or `answered_by`. The task page answers through the same function under the same mutation id, so a decision is answered once whichever way.
- `defer` and `undefer` are the Director's: see [deferring a decision](#deferring-a-decision).
- `open` is the Director's: it opens a question decision on a task and prints it as `show` does; see [decide open](#decide-open).
- `explain` is the Director's: it records its plain-English explanation of an open decision for the task page; see [decide explain](#decide-explain).
- `accept --director` is the Director's: it accepts a small feature's plan itself, where the repository opts in, and tells the Guildmaster; see [decide accept](#decide-accept).

## Answers

The answer is read from the file, or from standard input for `-`.
A leading UTF-8 byte order mark is dropped and CRLF line endings become LF, so an answer written on Windows records the same words as on Linux or macOS; UTF-16, which Windows PowerShell 5.1 writes by default, is refused with a hint to write UTF-8.
Otherwise the words are recorded exactly; a blank answer, invalid UTF-8 and more than 64 KiB are refused.

For a decision offering `approve` and `reject` under the [store's exact option-matching rules](store.md#records), this answer:

```
approve

Keep the migration behind a flag.
```

records the whole text as the answer, `approve` as the choice and `Keep the migration behind a flag.` as the note.

A `question` decision, such as one [`decide open`](#decide-open) opened, always takes free text: an answer whose first line names an option sets the choice and note as above, and any other non-blank answer, such as `neither, do c`, is recorded as the answer with no choice or note.
Every other kind refuses an answer whose first line names none of its options.

A close decision, `close-<step>-<n>`, kind `close`, offers `close` and `retry` ([an attempt that reports it is already delivered](tasks.md#an-attempt-that-reports-it-is-already-delivered)); a note on the following lines goes into a retry's context.

A finished hallmark's `follow-ups` decision, kind `follow-ups`, offers `pick` and `none` ([decision](decisions/report-only-workflows.md#the-guildmaster-picks-the-follow-ups)); `pick` takes the picked ids on the following lines, so this answer:

```
pick
H1, H3
```

records `pick` as the choice and `H1, H3` as the note, and creates no task: the Director runs `guildhall task new` for each id.

The answer is recorded with mutation id `decision-answer:<repo>:<task>:<decision>`, or `decision-answer:sha256:<hex>` of that text when it is longer than 128 bytes.
Repeating the same answer prints the original receipt; a different answer to an answered decision is refused, so a decision is answered once.
Answering records the answer only: it never advances, resumes or launches a task.
In a Steward store, an answer to a decision of one of the Steward's reserved tasks (`rollouts`, `setup`, `secrets`, `incidents` and `lessons`) also sends the Steward a `decision-answered` message naming the decision, never the answer, and wakes its session ([inbox](steward.md#inbox)); a failure to send or wake is reported on standard error, and the receipt is still printed with exit 0. An answer in a Director store sends no message and wakes nothing.
On an open task the answer is also recorded, in the same transaction, on the task page's [conversation](web.md#the-conversation) as the Guildmaster's words in chat under the decision, so the page holds what was said in chat too.

## decide open

`decide open` is the Director's: it records a question the Director puts to the Guildmaster about a task, such as which grill to run first, as a **question decision** on that task, so it is not left in chat alone.
The question is `--question <text>`, or `--question-file <path|->`, read with the same byte order mark, CRLF and UTF-16 rules as an [answer](#answers); exactly one is given.
`--option <text>` may be repeated to suggest answers, under the store's rules: at most 16, each one line, unpadded, distinct and at most 256 bytes.
`--recommend <option>` names the option the Director recommends, which must be one of the `--option` values (else exit 2); it is written, with the question, as the decision's [recommendation record](store.md#decision-recommendations), made against the question's own digest, and the task page marks its tile Recommended ([Confirm and Say](web.md#confirm-and-say)). A question opened before that record carries it as `recommended` on the decision, which the page still reads.
The Guildmaster's reply is recorded with `decide answer` like any other, and free text is always allowed ([answers](#answers)).

- The task must be open: a `done` or `cancelled` task is refused with exit 1, naming the task and its state, and nothing is written.
- The decision id is chosen for you: `q<n>`, one more than the highest `q<n>` on the task, starting at `q1`. An id with no `-` is never read as a step round or loop exit, and does not clash with the engine's own decisions.
- An open question decision on the task with the same question, options, recommended option, its recommendation record's or its old `recommended`, and proposed grant, with the same option, step, rules in order, until and reason, or none, is printed instead, and nothing is written, so a retry opens no second question; the same question with a different grant is a new question. Otherwise the decision is opened with mutation id `decision-open:<repo>:<task>:<id>`.
- It prints the decision as one JSON document, as `show` does, and sends no `decision` message: the Director asked the question.
- The question is also recorded, in the same transaction, as the Director's words under the decision on the task page's [conversation](web.md#the-conversation), where the Guildmaster can answer it.

### A question that proposes a grant

One option of a question may propose an exact [task grant](tasks.md#task-grants), so the Guildmaster's answer choosing it also approves that grant, with no `grant-<id>` decision to answer afterwards ([decision](decisions/task-grants.md)).
`--grant-option <option>`, one of the `--option` values, `--grant-step <id|all>`, `--grant-tool <rule>`, repeated for more, and `--grant-reason <text>` are given together, with `--grant-until attempt|done` (default `attempt`) as `task grant create`'s `--until`; at most one grant per question.
With them the command needs `--config`, or a copy's `config.toml`, to resolve the task's bound workflow as `task grant create` does; without them it needs only `--store`, and `--config` alone is a usage error. Missing grant flags, an option not among `--option`, a bad `--grant-until` and `--grant-step all` without `--grant-until done` are usage errors (exit 2).

- The grant is recorded pending as the task's next `g<n>`, in the same transaction as the question, with `decision` set to the question's `q<n>` and `approving_option` to the grant option, so `task grant list` and `director status` show both.
- Every check of `task grant create` applies (the step, the rule refusals, `all` needing `until done`, one pending or active grant per step) and any refusal refuses the whole open, exit 1, writing neither the question nor the grant.
- The store appends one sentence to the question naming the grant exactly, ``Choosing `<option>` also approves grant g<n>: step `<step>`, these extra allowed tools: …, until …. Reason: …``, so chat, Clerk, `decide list` and `show` and the task page all show it, and the question's `subject_digest` covers it; `--recommend` is made against that text. The task page's tile for the option says "also approves grant g<n>".
- The Guildmaster's answer choosing the grant option approves the grant, `approved_at` being that answer's commit time; another option or free text rejects it, and a void question leaves it never active. The approval is the Guildmaster's own answer: the Director never answers the question for them.
- A question proposing a grant holds its task no more than any question: the step may run before the answer, and the grant binds the first attempt of its step bound after approval.

```sh
guildhall decide open --repo guildhall --task d222 --question "May the builder fetch the upstream spec?" \
  --option fetch --option skip --recommend fetch \
  --grant-option fetch --grant-step build --grant-tool 'Bash(curl:*)' --grant-reason "The spec is only online."
```

A question decision appears wherever open decisions do: `decide list`, Clerk's `/decisions`, `director status` `needs_me` (where `decide defer` can hold it) and `task show`.
It never blocks or advances its task: the merge requirement `nothing_open` does not count it, a dependent task's dependency state does not report it as waiting for the Guildmaster, and a merge-queue front task is not `waiting_on` it.
Any close of the task (cancel, a Guildhall merge that closes it, a merge outside Guildhall, a hallmark's finish), and closing a slice, void it with the task's other open decisions, so a question never outlives its task ([decision](decisions/closed-tasks-have-no-open-decisions.md)).
Questions not about a task, such as install or config questions, stay in chat.

```sh
guildhall decide open --repo guildhall --task d105 --question "Which grill first?" --option a --option b
guildhall decide answer --repo guildhall --task d105 --decision q1 --answer-file - <<'ANSWER'
neither, do c
ANSWER
```

## decide explain

`decide explain` is the Director's: it records, once, the plain-English explanation of an open decision that the Director would otherwise write in chat, what the ask is about, what each answer means and what it recommends, so the [task page](web.md#the-directors-explanation) shows it on the ask.
The decision is named by `<repo>/<task>` and its id; the flags may come before or after them.
The explanation is Markdown, `--text <md>` or `--file <path|->`, read with the same byte order mark, CRLF and UTF-16 rules as an [answer](#answers); exactly one is given, and blank text, invalid UTF-8 and more than 64 KiB are refused.

- `--subject-digest` is the digest the Director read the decision against: the `subject_digest` that `show` prints, copied as it is. Any other digest is refused with exit 1, naming the current one, and nothing is written. The page shows an explanation only while its digest matches the decision, and otherwise says it was written for an earlier version, as one stored before this refusal can.
- `--recommend <option>` names the answer the Director recommends, which must be one of the decision's options; it is written, with the explanation, as the decision's [recommendation record](store.md#decision-recommendations), against `--subject-digest`, replacing any earlier recommendation, and the page marks that answer's tile as recommended unless the ask already marks one itself. Without `--recommend` the record is left as it is. Repeating the stored explanation word for word writes nothing, so it does not make its recommendation the latest again; use `task say --recommend` for that.
- An answered, void or unknown decision is refused with exit 1, as is a recommendation the decision does not offer and a `--subject-digest` other than the decision's `subject_digest`, and nothing is written.

It prints the explanation record as one JSON line, `{"repository":...,"task_id":...,"decision":...,"text":...,"subject_digest":...,"recommend":...,"at":...,"revision":<n>}`, `recommend` omitted when none was named, and stores it in the repository's `decision-explanations` bucket ([store](store.md#decision-explanations)); the decision itself is not changed.
Explaining again replaces the record and makes its revision one higher; the mutation id carries the revision the command read, `decision-explain:<repo>:<task>:<decision>:<revision>`, so a retry replays, and repeating the stored explanation word for word, with the same digest and recommendation, prints it and writes nothing.

```sh
guildhall decide explain guildhall/d150 accept--s2-1 --subject-digest sha256:3f9c… --recommend approved --file - <<'TEXT'
The plan builds the task list at `/`. **Approve** takes both recommendations.
TEXT
```

## decide accept

`decide accept --director` is the Director's acceptance of a small feature's plan on the Guildmaster's behalf, where the repository sets [`director_accept_lines` and `director_accept_files`](configuration.md#repositories) ([decision](decisions/director-plan-acceptance.md)).
The decision is named by `<repo>/<task>` and its id; the flags may come before or after them, and `--copy`, or the copy found from the current directory, stands in for `--store`, `--config` and `--state`.
`--director` and a non-blank `--reason` are required; the Guildmaster's own answer goes through `decide answer`.

Under the task lock it judges the decision again, as the advance that opened it did: the decision must be the latest round `accept-<k>` of a `guildmaster-approval` step whose spec comes from a plan step, still open and bound to that step's current spec digest, on a feature task of a repository with both keys set, and the [verdict](configuration.md#repositories) on that spec, with the task's overrides, must be yes.
Anything else is refused with exit 1, naming the reasons, and nothing is written: an ineligible plan, a superseded or stale round, a loop exit `accept-x<k>`, a review, merge or question decision, and a decision on a repository without the keys.

It then records the answer `approved` with the reason as its note, `answered_via` `director` and no `answered_by`, and a `director` entry on the task's [conversation](web.md#the-conversation) tagged with the decision and carrying the reason, in one transaction, and prints the store receipt.
The mutation id is `decide answer`'s and `answered_via` is part of the request digest, so a retry of the same acceptance replays its receipt without judging again, while `decide answer` or the page answering the decision afterwards is refused, as is the Director's acceptance of a decision the Guildmaster already answered.
Like `decide answer`, it never advances the task: the Director then runs `task start --decision <id>`, and the spec gate takes the answer as it takes any `approved`.

The decision leaves Needs you, and the task page, its stepper and Clerk say "accepted by the Director" with the reason, as does the task's `director_accepted` in [`director status`](director.md#status) while it is open, so a Director after a handoff still sees it; the Director names it once under What I did in its next reply.
The Guildmaster takes it back with [`step send-back`](step-send-back.md) to plan, which opens a new round that is judged again.

```sh
guildhall decide accept guildhall/d270 accept-1 --director --reason "One slice, about 120 lines in 4 files, touches none, no open questions."
```

## Deferring a decision

`decide defer` is the Director's: it holds an open decision out of [`needs_me`](director.md#status) until every task named by `--until-merged <repo>/<task>`, which may be repeated, has merged, with the Director's `--reason` ([holds](director.md#holds), [decision](decisions/hold-until-merged.md)).
The decision itself is not changed: it stays open, `decide list` still lists it, and other tasks' dependency states do not shift.
While the hold waits, `director status` lists the decision under `held` and marks it `"deferred": true` in `decisions`, and the Lanterns band does not count it; once every named task is `done`, or any is `cancelled`, it is an ordinary `decision` item again with the release appended to its reason, and the session supervisor wakes the Director once with a `hold-released` message.
Answering a deferred decision closes it as usual, and the deferral is then ignored.

Each named task must be an open task of this store other than the decision's own; one already merged is refused with `<repo>/<task> already merged; act now instead`, as is one cancelled, and an answered or unknown decision is refused too.
`defer` and `undefer` each print the deferral record as one JSON line, `{"repository":...,"task_id":...,"decision":...,"reason":...,"until_merged":[{"repository":...,"id":...}],"deferred":<bool>,"cycle":<n>}`.
As with [`step park`](running.md#step-park-and-step-unpark), the mutation id carries the record's cycle, `decision-defer:<repo>:<task>:<decision>:<cycle>`, so an identical deferral replays, one with another reason or set while deferred is refused quoting the prior one, and `undefer` keeps the record with `deferred` false and the cycle one higher; `undefer` of a decision that is not deferred is exit 1.
