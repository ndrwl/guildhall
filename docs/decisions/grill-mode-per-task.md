---
status: accepted
date: 2026-11-04
topic: tasks
tasks: d172
amends: grill-live-interview.md
---

# Each feature task's grill is live or unattended, chosen per task with a recorded reason

## Context

Every feature task's grill was a live Director stage, so the Guildmaster was grilled on tasks that did not need them: hallmark follow-ups whose contracts were already written, tasks they had delegated ("you decide"), and tasks with one small question.
The unattended grill, an Artisan writing each open decision with the answer it proceeds on, existed only as a per-repository opt-in through `workflow_overrides` ([live interview](grill-live-interview.md)), and it could not run: admission refused an Artisan grill, which had no role profile and whose kind takes no `profile` setting, and the override kept the live grill's skills.
The d172 grill settled that the Director chooses, at `task new`, whether each task's grill is live or unattended, records a one-line reason, keeps it live when unsure, and that the Guildmaster can overrule it until research starts ([grill mode](../tasks.md#grill-mode)).

## Decision

1. **Grill mode is a per-task record, applied to the bound definition.**
   A task's grill-mode records hold `live` or `unattended`, the reason, who set it (`director`, or `guildmaster` when the Director acts on their words) and when; they append, the latest is in effect, and a task with none is live.
   Each advance applies the mode after the bound revision's digest check, as task overrides are applied to the config: for an unattended task the workflow's Director grill becomes an Artisan step with `guildhall-grill-unattended` and `guildhall-domain-model`.
   The task binds the same revision either way, so no shipped workflow bytes change, nothing goes to `internal/workflow/retired/`, and every task bound before stays live with its digest.
2. **The grill's role profile is `researcher`.**
   An Artisan grill is read-only like research, so `grill` maps to `researcher` in the role profiles, with no workflow-schema change; admission (`task new`) judges the definition with the mode applied, so a copy that cannot run it is refused at creation, naming `--grill live`.
   The legacy profile defaults never write a profile into a grill.
3. **The mode changes until research starts.**
   `guildhall task grill` switches it, refused while a grill attempt runs and once a later step of the workflow has an attempt; each grill attempt binds the mode, so a grill that already succeeded in the other mode runs again in the new one at the next advance, and its notes supersede the old.
4. **An unattended grill escalates by failing.**
   An Artisan that finds a question whose answer would change the task's goal, or which repository it touches, fails its attempt with the question in its summary; the Director switches the task to live and retries the grill, which then runs as a Director stage.
5. **Design rounds follow a live grill.**
   A design round on an unattended task is refused with the command that switches it to live; a switch to unattended is refused once the task has had a design round.

## Alternatives rejected

- A shipped `default-unattended` workflow bound at `task new`: it covers no repository with a custom workflow, and a switch after creation would need a rebind no command does.
- A `profile` setting on the grill kind: a workflow-schema change for what a role profile already expresses.
- An Artisan grill serving design rounds: an untested reopen path for a case nobody asked for, when switching to live is one command.

## Consequences

An unattended grill is not run by the Director, so it waits at the [dependency gate](../tasks.md#dependencies) with the rest of the task; that suits follow-ups.
Its open questions reach the Guildmaster only through the plan's open questions at acceptance.
A repository whose `workflow_overrides` already runs the grill by an Artisan has no Director grill, so grill mode does not apply to it.
