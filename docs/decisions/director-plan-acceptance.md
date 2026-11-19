---
status: accepted
date: 2026-11-19
topic: director
tasks: d263
---

# The Director accepts a small feature's plan and tells the Guildmaster, when the repository opts in

## Context

The Guildmaster asked to be asked only the most pressing decisions (s53, "how can we make you more autonomous and only ask me for the most pressing decisions"), and answered "yes to all" to proposal G: a small feature's plan is accepted by the Director, with the Guildmaster told, not asked.
Bugs already skip plan acceptance, since the `bug` workflow has no acceptance step.
On approving the task the Guildmaster added: "approve all -- make sure that these are all configurable per repo."
Until now the workflow-edit rules said that no blanket delegated authority is granted ([workflow-edit authority](../configuration.md#workflow-edit-authority)).

## Decision

This is a narrow exception to that rule, for one decision only: a feature task's plan acceptance, `accept-<k>` of a `guildmaster-approval` step whose spec comes from a plan step.
A repository opts in with two repository keys, `director_accept_lines` and `director_accept_files`, both `0`, off, by default, and each capped by the repository's own size budget ([repositories](../configuration.md#repositories)); no task override sets them, and a task may keep its acceptance with the Guildmaster with the stricter-only override `accept.requires_guildmaster = true`.
The engine judges each round (`interp.DirectorAcceptable`): the setting on, the grill unattended or run by an Artisan, at most one slice, open questions "None", the Summary's Size within both thresholds and its Touches `none`, and no override; any rule it cannot read fails closed. The round's `decision` message states the verdict and its reasons.
The Director records the acceptance with `guildhall decide accept <repo>/<task> <decision> --director --reason <text>` ([decide accept](../decisions-cli.md#decide-accept)), which judges the round again under the task lock, against the spec digest the decision binds, and refuses anything else.
It records the ordinary answer `approved`, with the reason as its note and `answered_via = "director"`, which is part of the answer's request digest, and a conversation entry by the Director carrying the reason, in one transaction.
The Director accepts only when the command allows it and its own reading of the plan finds no secrets, managed settings or trust in it; otherwise the decision stays the Guildmaster's, and the Director never requests changes on the Guildmaster's behalf ([Director role](../../baseline/roles/director.md)).
The decision leaves Needs you; the task page, Clerk, the stepper and the task's `director_accepted` in `director status` say "accepted by the Director" with the reason, and the Director's reply names it once under What I did.
The Guildmaster takes an acceptance back with `step send-back` to plan, which opens a new round that is judged again.

## Not yet settled: who may switch it on

The keys are read when a decision is answered, and they live in `director.toml`, which the Director edits; so as built, the Director could grant itself this authority by setting them.
The accepted spec's open question Q8 recommends that the keys take effect only with a recorded Guildmaster approval, like `workflow approve --words-file`, and the Guildmaster approved the plan ("approve all").
That gate is not built yet: neither slice of d263 adds it, and it is raised as a follow-up task.
Until it lands, the Director sets the keys on a repository only on the Guildmaster's own words naming that repository, quoted in the task or chat that asks for it, and never on its own initiative ([Director role](../../baseline/roles/director.md)).

## Alternatives rejected

- The engine answering `approved` by itself: the task says the plan is "accepted by the Director", and the Director's reading is the backstop for sensitive areas the plan does not declare.
- The Director answering with plain `decide answer`: that would record the Guildmaster as the author of words they never said.
- A separate message to the Guildmaster for each acceptance: the point is less noise, and the task page and the Director's reply already tell them.
- One switch that uses the quality size budget itself as the threshold, or a task-scoped setting: the task asks for a threshold within the budget, and a task override could loosen a task-scoped one.

## Consequences

Merge requirements, including `guildmaster_approval`, are unchanged: a Director-accepted task still asks the Guildmaster before merge by default.
The spec gate is unchanged, since the answer is an ordinary `approved` bound to the spec digest; a superseded spec voids the round as before.
Loop exits (`accept-x<k>`), `changes_requested`, document reviews, code reviews, merges, grants and tickets are never Director-accepted, nor is any plan on a repository without the keys.
A config without the keys keeps its digest.
