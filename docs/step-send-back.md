# Sending a succeeded step back

`guildhall step send-back` lets the Director start a new attempt of an earlier succeeded Artisan step, without changing the old attempts.
It is an option for conflicts Guildhall could not [rebase itself](interpreter.md#tiered-rebase), changed requirements within the accepted scope, or other work the Director chooses to request, not a mandated conflict-recovery procedure.
A `major_conflicts` rebase makes the same send-back automatically, with feedback naming the base, the conflicting files and the reason.

```sh
guildhall step send-back --copy <dir> --repo <id> --task <id> --step <id> --attempt <n> --feedback <instructions>
guildhall step send-back --store <file> --config <file> --state <dir> --repo <id> --task <id> --step <id> --attempt <n> --feedback <instructions>
guildhall step send-back --help
```

With `GUILDHALL_COPY` set, the path flags can be omitted.
`--attempt` names the succeeded attempt, not the next attempt to create.
The task must be open, the step must run as an Artisan, the named attempt must be the step's latest succeeded attempt, and no task attempt may still be running.
A new request is also refused if the target or any downstream attempt-running step already has immutable inputs prepared for its next attempt, whether recorded as a binding document or only written to disk before an interrupted record.
Advance the already-prepared work instead; accepting new feedback could otherwise launch an attempt with obsolete inputs.
Failed or stopped attempts use [`step retry`](running.md#step-retry) instead.
Missing or whitespace-only feedback, invalid identifiers or attempt numbers, extra arguments and unknown flags are usage errors, exit 2, before the store is opened.
Other refusals and operational errors exit 1 on standard error; success prints the normal advance JSON on standard output, exit 0.

The engine first records the instructions as immutable document `send-back-<step>-<attempt>` and then starts the next attempt with that document on its `feedback` port.
The request runs under the same task lock, admission holds and attempt caps as every advance.
A hold may leave the feedback pending with `wait: paused`; a subsequent `task start` consumes it once the hold lifts.
A replay with the same step, succeeded attempt and feedback never creates an additional request, including after a crash or after the next attempt finishes.
Changing feedback for that request identity is refused; further work after success uses the newer succeeded attempt as its identity.
A failed new attempt retains its feedback for the normal retry.

Changed build outputs invalidate downstream input bindings, triggering a new review and publish.
The review keeps the previous outcome, classifies the new range and reviews only the resolution when it proves a rebase; otherwise the review is full.
`task review --full` can force a full review.
Publishing pushes the new head to the same task branch and updates its existing open PR.
All normal spec, review, validation and merge-decision gates still apply; send-back approves nothing and never changes git history itself.

For a blocked published PR, the Director also has the option of holding the task, leaving the work unadvanced, or rechecking after an external change with `task start`.
Existing `artisan stop` and Director-stage `step finish --failed` apply only to their existing states; there is no task-cancellation CLI.
These are independent choices, not a sequence Guildhall requires.
