# D72. Director stages are attempts the Director finishes

A step whose actor is `director`, the default workflow's `grill`, runs in the Director's own session rather than as an Artisan launch ([D61](0061-grill-live-interview.md)), and until now the engine refused it with `ErrUnsupported`.
The engine now runs it ([Director stages](../interpreter.md#director-stages)).

## Decisions

- **A Director stage is an attempt.**
  It is recorded with `StartAttempt`, bound and prepared under `<step>-<n>/prompt` exactly as an Artisan attempt is, and ends with a `result.json` taken by the same `attempt-result` intake, so outputs, outcomes, replay, `step retry`, the timeline and `director status` need nothing new.
  A separate record kind for Director work was rejected: it would duplicate the attempt's identity, intake and replay for one actor.
- **Handed over, not launched.**
  The engine sends the Director a `director-stage` message naming the prompt, the output ports and the finishing command, and marks `launched.json` with `launcher: director` and no process, after the message, so a crash between the two resends the message as a replay.
  The task waits with a fourth wait, `director-stage`, so the Director, `director status` and the watcher can tell a stage it must run from an Artisan it must watch.
  The wake is the outbox's, once per message, as for every Director message.
- **The Director's role renders the prompt.**
  The prompt is the Director's brief for the stage: `roles/director.md` (or the config's `[director] role`, as its session uses), the stage guide and the step's `skills`.
  The default grill lists `guildhall-grill` and `guildhall-domain-model`; the default workflow's digest changes, which is admitted because the shipped definition is approved by the release and kinds evolve in place before the first release.
  A `workflow_overrides` entry changes only the actor, so a repository that overrides the default grill to `artisan` would render these two skills to its Artisan; one meant to run unattended uses a workflow whose grill lists `guildhall-grill-unattended` instead, as D61 says.
- **`step finish` is the Director's verb.**
  `guildhall step finish --step --attempt --summary (--output <port>=<file>... | --failed)` copies each file into the task's working directory as `<port><ext>`, writes the result with `adapter.WriteResult`, which never replaces one, and feeds `attempt-result`.
  The Director writes no result JSON by hand, and its notes live wherever it drafted them until it finishes.
  It refuses a step not run by the Director, an attempt that is not current, finished or not yet handed over, so a stale or repeated command cannot finish the wrong attempt.
  Abandoning is `--failed`: the task waits for the Director as after a failed Artisan attempt, and `step retry` hands the next attempt over ([D62](0062-failed-attempt-retry.md)).
- **Copied into the working directory.**
  Outputs are recorded from the working directory, as an Artisan's are, so the existing output checks and [retirement](../interpreter.md#the-working-directory) treat them alike; the engine makes the working directory ready before handing the stage over.

## Not yet

The Director's stage skill (D8b) that reads the brief and runs `guildhall-grill` and `guildhall-domain-model`, and `needs_me` items for a waiting Director stage, come with the Director's skills; until then the `director-stage` message and `next.json` say what to do.
Loops back to a Director stage stay refused.
