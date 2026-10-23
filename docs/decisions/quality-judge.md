# A delivered task's judge is an owned attempt of its own step

The Guildmaster accepted that the model-judged rubric scores come from a separate judge session, never the builder's or the Director's, of a different model family when quota allows, with its agreement with the Guildmaster's audit tracked ([Q3](../quality.md#running-it)).
This change launches that session and records what it says.

## An attempt on the delivered task

`guildhall quality judge` records and launches an attempt of the reserved step `quality-judge` on a task that is `done`, through the same owned launcher, caps and pause hold as every Artisan attempt ([quality](../contracts/quality.md)).
It is an attempt rather than a workflow step because the self-review is a Director skill first, not a step bound into every workflow digest (the [rubric decision](0083-quality-rubric.md)), and rather than a process of its own because every model process Guildhall starts is owned, contained, counted against the guard rails and has its spend recorded.

- `quality-judge` is a reserved step id that `workflow.Validate` refuses, so it can never be one of a workflow's steps, and it is the only step the store starts on a task that is not open, and only on a `done` one; every other step still needs an open task.
- The judge reads the self-review document and judge brief the skill wrote, the diff and the facts, copied beside its prompt, and writes `judgement.json`: a yes or no to "would you merge this as is", and a score or a pass for each pending criterion, each with the evidence it quotes.
- When the attempt ends, its launcher runs `guildhall quality judged` instead of `step done` or `task start`: a valid judgement becomes the task document `quality-judge-<n>-judgement`, with the judge's profile, adapter and family recorded beside it, and the Director is told; a failed attempt or a refused judgement is told to the Director and records nothing.
- The task stays `done`: a judge attempt never reopens, re-gates or changes a delivered task. Director status still offers `artisan resume` for an orphaned judge and `quality judged` for each settled attempt until its intake notification is durably recorded, even after newer judge attempts exist.

## The judge's family

A profile's family is its adapter: today the `fake` and `claude` adapters, and every model behind one adapter shares its family.
The new setting `quality_judge_profiles` names the profiles a judge may run as, in order of preference; the builder's profile is excluded, and an unset list or no eligible profile refuses the judge with `ErrNoJudge`.
A judge profile currently selects its adapter and role only; its model and effort are not yet passed to the runtime, as for every Artisan launch.
With `quality_judge_separate_family` on and a known builder adapter, the first listed eligible profile whose adapter is not the builder's is chosen; otherwise the first listed eligible profile is chosen. The build manifest currently proves only the profile name, not the historical adapter, so the builder's family is unknown and never classified as separate; today's configuration cannot establish historical execution identity.
The builder is the profile the task's latest succeeded build attempt was rendered for, read from that attempt's prompt manifest; finish receipts establish execution order rather than step names.
"When quota allows" is measured, not guessed: a profile whose earlier judge attempt for the same self-review failed is passed over, so running `quality judge` again after a judge ran out of quota falls back to the next profile, and every choice records whether the judge's family was separate and why not.

`quality_judge_profiles` is a setting rather than a switch because the existing switches say whether to judge and how independently, and only the copy knows which of its profiles are fit to judge.
A copy that leaves it unset keeps the behaviour of its other settings and its config digest, but cannot start a judge until it lists an eligible profile.

## Agreement

`guildhall quality agreement` pairs each task's latest judgement with its answered `audit` decision and counts how often the judge's yes or no matches the Guildmaster's, overall and split by whether the judge's family was separate.
It reads the recorded documents and decisions, so agreement is recomputed from durable records rather than stored a second time.
Position-swap consistency needs pairwise comparisons, which only reference evaluations make, so it is not computed yet.

## The fix line

`task new --fixes <repository>/<task>` records the earlier task whose defect the new one fixes, and publish adds "Fixes a defect introduced by task `<repository>/<task>`." to the pull request body, the line rework (C5) and change failure (E2) count from.
The task must already exist; the line is recorded with the task, so a republished head carries it unchanged.

Consequences: judging consumes Artisan attempts, including retries, counted against the guard rails and recorded in the delivered task's spend, so a task's E5 cost includes its judging; a copy that never runs `quality judge` gains nothing and its config digest is unchanged.
A workflow that names a step `quality-judge` is now refused by `workflow.Validate`; the shipped default and examples name none.
