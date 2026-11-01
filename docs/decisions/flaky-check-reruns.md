---
status: accepted
date: 2026-10-31
topic: ci
tasks: d112
amended-by: flaky-check-reruns-queue-front.md
---

# Validate re-runs a failed CI job once before sending the task back to build

## Context

A flaky check cost a build round: guildhall/d87 was sent back to build by the Windows flake now tracked as guildhall/d111, and the Director then re-ran the job by hand.
Re-running by hand only helps after the failed round is recorded and the build is already launched, and nothing records the flake so it can be fixed.
The Guildmaster asked for a per-repository policy, injected from the config, and accepted every grill decision ("112 accept").

## Decision

A repository key, `flaky_checks = "off" | "rerun" | "rerun-and-file"`, sets the policy; the default is `rerun-and-file`, and no task overrides it ([configuration](../configuration.md#repositories)).
Under `rerun` and `rerun-and-file`, a failed validate round whose failing checks are all GitHub Actions jobs not yet re-run on that head is not recorded: validate waits until no check of the head is pending, writes a write-once rerun record per failing check with the failed run, job, URL and the failing test names read from the job's log, then asks the forge to re-run each job (`forge.GitHub.RerunJob`, `gh api --method POST repos/<owner>/<repo>/actions/jobs/<job>/rerun`) and waits on the checks ([reruns](../interpreter.md#validate-reruns)).
There is one rerun per check per head, keyed by head and check name rather than run id, so a second failure is a real failure that goes through the failed loop as before; a rerun spends no round of the loop's `max`.
A pass after a rerun is a flake: the passed result names it, and under `rerun-and-file` the engine sends the Director a `flake` message whose first line is the policy and whose text is the evidence, and the Director files a p3 bug or adds the evidence to an open bug naming the same test, reporting it in the Routine box ([inbox skill](../../skills/guildhall-director-inbox/SKILL.md)).
A rerun the forge refuses records why and falls through to the failed round in the same advance; a crash after the record and before the forge answered counts the rerun as spent, so the forge is never asked twice for one check on one head.
A rerun never marks a check passed: a passed result still needs every check of the head passed, and `validated`, `ci_green` and the merge requirements are unchanged.

## Alternatives rejected

- A heuristic for "likely flake", such as a failing test outside the change's diff on a check green on the default branch: the grill chose a plain rerun, which needs no diff or base-branch read and treats every repository's checks alike; a real gofmt or vet failure costs one wasted job run before the build round.
- Re-running at once while other jobs of the matrix are still running: GitHub refuses to re-run a job of a workflow run in progress, so validate waits for the matrix, which delays a real failure's send-back until the matrix finishes.
- Retrying a refused rerun at the next advance: CI trouble, such as a run too old to re-run, would stall the task; the round fails instead.
- Hard-coding the filing in the Director's inbox skill: the skill reads the policy line of the message, so a repository's config, not the skill, decides.
- A `checks-rerun` message for each rerun: the task waits on checks as for any pending check, and Clerk shows "CI pending"; only the flake needs the Director.

## Consequences

The shipped default turns reruns on, at upgrade, for every repository whose CI runs on GitHub Actions, including other Directors' repositories; `flaky_checks = "off"` keeps the earlier behaviour.
A config without the key has the same digest as before, and the shipped `default` and `bug` workflows and their digests are unchanged, since the policy is engine behaviour read from config.
A forge without `RerunJob` behaves as `off`.
The checks of a rebased merge-queue front (`headChecks`) are out of scope: a red check there still escalates back to build, and a follow-up can reuse the same rerun record and rule. [d143 does](flaky-check-reruns-queue-front.md).
The design assumes, from GitHub's documented behaviour, that a re-run job is a new check run with a new run id and job id, and that the old job's log stays readable under its id; it was not confirmed against a live run in this change.
