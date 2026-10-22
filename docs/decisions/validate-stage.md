# Validate reads the forge's checks of the published head

The engine now runs `validate` (requirements R1.12 and R1.63), the follow-up the [delivery decision](0060-delivery-stage.md) left open, over the same forge boundary: [`internal/forge`](../contracts/forge.md) gains `Checks`, and the interpreter's [validate](../interpreter.md#validate) reads it.
When the Guildmaster kept validation out of the delivery change, they asked for this follow-up to define it; it takes the shape of option (b) of that delivery question on red or pending CI: validate reads the forge's check state for the pushed head at each advance; pending waits and is advanced again by `task start` or a later watcher; red takes the workflow's `failed` loop back to build with the failing check names as feedback, honouring the loop's `max`; green records the result document.
With it the shipped default workflow runs from task creation to a merged pull request when its grill is overridden to an Artisan, the only grill the engine runs until the Director slice.

The choices the implementation made within that shape:

- **Bound to the head.** Checks are read for the commit, never for whatever the pull request is at now, so a pull request that moved cannot answer for an older head; on GitHub one GraphQL call reads the commit's status check rollup, which holds both check runs and commit statuses.
- **Every reported check counts**, not only the required ones: Guildhall cannot tell which checks a repository's protection requires on every forge, and the forge still enforces its own rules at merge.
- **No checks is pending**, not green: right after a push the forge may not have registered its checks yet, and validation must not pass on absent evidence.
  A repository with no CI says so explicitly with its `no_ci` setting, and then no checks reported passes, with the result marked as having no CI; without the setting it stays pending.
- **Any failed check is red** at once, even while others are pending, so the build gets its feedback without waiting for the slowest check.
  Cancelled, timed out, stale, startup failures and checks needing action count as failed; neutral and skipped count as passed.
  A state the implementation does not know fails the read rather than being guessed.
- **The failed loop's exit offers `continue` and `reject`**, not `accept`: accepting red checks would deliver a head nothing validated, which the review floor forbids; `reject` waits for the Director.
- **A waiting failed head is read again**, so a check the Director re-ran counts: without an `on.failed` rule, after a rejected loop exit, and when the loop's target left the head unchanged.
  The Guildmaster asked for this after review found those states had no exit ("Yes lets check ci periodically - refresh can be set in config").
- **A round is spent only on new evidence.** Each result records the head and every check's run id, and a failed read spends a round of `max` only when it holds a failed run, by check name and run id, that is not among the latest result's failed runs, a new failure or a failed re-run ("the checks should know which commits have been run"); the same or fewer failures keep waiting, since the build already had them as feedback, so an unchanged head no longer burns rounds and model launches on unchanged evidence.
- **Reads are paced** by [`checks_refresh`](../configuration.md#settings); autonomous continuation is now owned by the Director session's [checks watcher](../director.md#checks).
- **Deliver checks its validation input** is a passed result for the delivered head (`ErrValidationGate`) before opening the merge decision.
  Validate already completes only on such a result, so the gate is deliberately redundant: it is kept as defence in depth, mirroring the review gate, and catches a workflow and records that disagree.

Out of this change: the `auto` merge policy stays refused, the merge method stays squash, and nothing rebases a branch behind its base.
