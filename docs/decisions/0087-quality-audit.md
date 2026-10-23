# D87. Deliver opens the Guildmaster's audit of a sampled task

The Guildmaster accepted a three-question audit of one in five delivered tasks, behind configuration, so the model judge's drift is measured rather than assumed ([G4](../quality.md#honesty-and-evidence-g)).
This change gives it a decision kind and opens it.

## A decision of its own kind, opened by deliver

The audit is a decision of a new kind, `audit`, with id `audit`, bound to the head that merged, so it is listed by `decide list`, shown in `needs_me` and answered in conversation like every decision ([interpreter](../interpreter.md#publish-and-deliver)).
A step's decision is always `<step>-<round>`, so the id cannot collide with one, and the history view, which reads only names of that shape, ignores it.

- Deliver opens it as it closes a merged task, the one place a task becomes done, rather than leaving it to the self-review skill: the sample is then chosen by code, not by a model remembering to count.
- It opens before the task is marked `done`, so a close that fails replays to the same audit, and the advance still waits `done`: the audit never holds a task, and answering it records the answer only.
- Its first question, would you have merged this as is, takes `yes` or `no` as the answer's first line, so agreement with the judge's verdict can be counted from `choice`; the other two, what was asked needlessly and what was decided that should have been asked, are the note in the Guildmaster's own words, because they name decisions and reasons no option list could hold.

The alternative, a `question` decision with the audit's wording, would leave the audit indistinguishable from every other question in the timeline and in the H2 counts that read it.

## Independently configured, sampled by the task's identity

The audit is controlled by `quality_audit_sample` alone: 0 disables it, 1 selects every task, and the default of 5 samples about one in five tasks even when self-review is off.
The Guildmaster's spot checks are useful independently of model self-reviews; enabling either practice does not require enabling the other.

The deterministic hash sample is defined by the [`Quality.Audits` contract](../contracts/config.md).
The task's identity alone chooses, so a replayed or retried close gives the same answer with no counter to keep, and two tasks closing at once cannot race for the same count.
It is about one in N rather than exactly every Nth: a counter of delivered tasks would be exact but needs a store record updated in the same transaction as the close, which this sample does not need.

## Not yet

Reference runs are not sampled, because the reference runner does not exist yet; it will open the same decision for each of its runs.
Agreement between the judge and these answers was not computed by this change, because the judge session was later work; [the judge decision](quality-judge.md) computes it and adds the report's fix-PR link line, which C5 and E2 read.

Consequences: by default a copy gains, for about one in five merged tasks, one decision after the merge and one `decision` message to the Director, even with self-review off.
Setting `quality_audit_sample` to 0 disables those audits; the default values and the config digest of a copy that sets nothing are unchanged.
