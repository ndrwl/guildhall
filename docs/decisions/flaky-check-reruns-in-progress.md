---
status: accepted
date: 2026-11-03
topic: ci
tasks: d154, d163
amends: flaky-check-reruns.md, flaky-check-reruns-queue-front.md
---

# A requested check rerun still in progress is waited for, not counted as a failure

## Context

d154's hallmark (H36) found two paths on which validate, and deliver's rebased queue front, recorded a failed round while a rerun it had asked for was still running, so the flake was never seen again and reached the Guildmaster as a real failure.
In a partial loop the forge accepted one job's rerun and then refused another's, and the round was recorded in the same advance from the read taken before any rerun.
In a lagging read one re-run check showed `pending` while another still showed its old failed run; any rerun record made the round ineligible, and an ineligible round was recorded without looking at `pending`, against the documented crash rule's "with nothing pending".

## Decision

A round's reruns are waited for as a whole ([reruns](../interpreter.md#validate-reruns)).
While any check of the head is pending and some check of the round has a rerun record on this head with no `-outcome`, the step records nothing, sends the same `checks-pending` message it sends before a rerun, and waits on `checks`, eligible or not.
A refused rerun still records why and asks for no further rerun, since the refused check already makes the round a real failure; when an earlier rerun of the same round was accepted, the step waits for it and records the failure from the read once it has finished, so its `checks` show the rerun's run and `reruns` names each check that failed again.
With no accepted rerun the refusal is a real failure in the same advance, as before; CI trouble still never stalls a task, since the wait ends when the accepted jobs finish.
At the rebased queue front this holds the front, without recording its checks or escalating, while the accepted rerun runs.

## Alternatives rejected

- Asking for the remaining reruns after a refusal: the round fails anyway, so they would spend CI for nothing.
- Also waiting when an accepted rerun's check still shows its old failed run with nothing pending (d154's "worth exploring" lag): the store cannot tell an accepted rerun from a crash before the call, so it needs an acceptance record or a grace period and would change the crash rule; it is left for a separate task if it is seen live.

## Consequences

A round with an accepted rerun takes as long as that rerun's job to be recorded after a refusal, and the queue front is held for the same time.
No record schema changes: the rerun record, its outcome and the result's `reruns` keep their formats, and a round with no rerun record behaves as before.
