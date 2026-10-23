# `internal/quality`

## Contract

The judge session of the [quality rubric](../quality.md#running-it) and its agreement with the Guildmaster's audit ([decision](../decisions/quality-judge.md)).
Its seams are `interp.Records` extended with `TaskHistory` and `LatestDirectorSession` for the store, `interp.Outbox` for Director messages, `Launcher`, which `launch.Process` implements with `LaunchFiles`, and `AgreementRecords` for the agreement reads.

`ReadSelfReview` reads a `guildhall.self-review/1` document as the [self-review skill](../../skills/guildhall-self-review/SKILL.md) writes it, taking only its repository, task, number, head and the criteria whose `judge` is `pending`, each with its kind; it refuses another schema, a missing identity or number, and a head that is not an object id, with `ErrSelfReview`.
`ParseAnswer` reads the judge's `judgement.json` against those pending criteria: `merge_as_is` is `yes` or `no`, and `criteria` lists each pending criterion at most once, a score with a whole `score` from 0 to 3 and a gate with a boolean `pass`, never both, each with non-blank `evidence`; an unknown field, an id that is not pending, a duplicate, a missing judgement of a criterion or more than 1 MiB is refused with `ErrAnswer`.
`ParseJudgement` reads the `guildhall.judgement/1` record Guildhall writes from an accepted answer, refusing another schema or a newer version with `ErrJudgement`.

`Judge.Start` and `Judge.Take` are not safe for concurrent use on the same task; the CLI holds the task's lock around each.

`Judge.Start(ctx, repository, task, review)` starts the judge of self-review `review` of a delivered task:

- it refuses, starting nothing, a task that is not `done` (`ErrNotDelivered`), a self-review document that is missing, names another task or number, or has nothing pending (`ErrSelfReview`), and a judge attempt of the task that is already launched and has not finished (`ErrJudgeRunning`);
- it chooses the judge with `config.Quality.Judge`, excluding the builder's profile and each profile whose earlier judge attempt for the same review failed, refusing with `ErrNoJudge` when no eligible profile is listed; the builder's profile name comes from the prompt manifest of the task's latest succeeded build attempt in finish-receipt order; that manifest does not prove the historical adapter, so the builder's family is unknown and `SeparateFamily` is false (`config.JudgeChoice`); a judge profile currently selects only adapter and role; the judge's `LaunchFiles` path does not pass that profile's model or effort to the runtime, unlike ordinary workflow Artisan launches through `Process.Launch`;
- it records the next attempt of step `workflow.JudgeStep` with `StartCappedAttempt` under `Judge.Caps`, whose refusals it returns unchanged, and a mutation id derived from the task and the attempt number, so a replay records nothing twice;
- it publishes the attempt's preparation once, by renaming `prompt.partial` to `prompt`: `prompt.md`, holding the judge's role and brief and naming the criteria to judge, the inputs beside it under `inputs/` (the self-review document, its brief, and its diff and facts when they exist), and `judge.json`, the choice and the review number; a published preparation is reused unchanged;
- it launches the attempt with an empty working directory, `work` in the attempt directory, and marks it launched; an attempt recorded but never marked launched is launched again with the same identity and preparation, never recorded again; `Caps.Hold` checks the current Director hold before both fresh and retried launches, refusing with `store.ErrDirectorHeld` while held.

`Judge.Take(repository, task, attempt)` takes a finished judge attempt, as its launcher's advance does with `guildhall quality judged`:

- a still-running attempt or one that is not a judge attempt of the task is refused with `ErrJudgeRunning` or `ErrNotJudge`;
- a succeeded attempt whose result names `judgement.json` as its first output and whose answer `ParseAnswer` accepts is recorded as the document `quality-judge-<n>-judgement`, a `guildhall.judgement/1` record with the self-review's identity and head, the attempt, the choice and the answer, and the Director is sent a `judgement` message;
- a refused answer records nothing and sends `judgement-refused` with the reason; a failed or stopped attempt records nothing and sends `judge-failed`; its retry guidance says a later `Start` passes over only a failed judge's profile, while a stopped judge's profile is chosen again with unchanged settings;
- every write and message id is derived from the attempt; `MessageID` supplies the deterministic notification id shared with Director status; taking a recorded judgement again verifies its document digest and retries the deterministic notification from that record, so interrupted delivery recovers and delivered messages are deduplicated.

`PreparedReview(attemptDir)` reads the review number from the published preparation using the same decoder as judge launch and intake; unpublished or unreadable preparation returns an error.

Director status offers `quality judge` for unfinished judges without ownership or a launch marker, even before preparation exists. It supplies the published review number when available; otherwise its reason explicitly asks the Director to supply the self-review number with `--review`. Partial preparation does not establish that number. Owned orphaned judges still receive `artisan resume`, then `quality judged` for every settled judge attempt without a durable intake notification receipt, including older attempts. Acknowledging the notification does not make intake pending again.

`Agreement(records, documentDir, repositories)` pairs each task's latest judgement with its answered `audit` decision and reports, overall and split by whether the judge's family was separate, how many tasks were audited, judged and paired and how many pairs agreed, where a pair agrees when the judge's `merge_as_is` equals the audit's choice; a judgement file whose digest is not its recorded one is an error.

## Tests

`quality_test.go` was written red first against stub bodies ([red run](../evidence/quality-judge-2026-10-23-red.txt)): reading the self-review's pending criteria and each refused document; each accepted and refused answer; the refusals of `Start` for an open task, a missing or foreign self-review, nothing pending and a running judge; the choice of a separate family, the pass over a failed profile, a replay after a failed launch and a start refused at a cap, all over a real store and a recording launcher; `Take` recording a judgement once, refusing an answer, and telling a failed attempt; and `Agreement` over judged, audited and paired tasks.
