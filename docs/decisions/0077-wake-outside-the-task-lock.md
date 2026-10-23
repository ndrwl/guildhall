# D77. The wake ends at its bound and runs outside the task lock

Sustainability review 11 (F36) found the wake's ten-second bound not kept and the wake run under the task lock.
`herdr` ran with `exec.CommandContext` and no `WaitDelay`, so a killed `herdr` whose child kept its output pipe held the call until that child exited; and the engine's outbox woke the Director inside the advance, so every message the advance sent held the task lock for up to three Herdr calls.
In the review's probe one slow pane turned a two-message advance into 34 seconds under the lock, long enough for another advance of the task to lose the lock (F35).

## A `herdr` call ends with its context

Every `herdr` run sets `WaitDelay` to five seconds, as `forge` and `git` already do: once the context ends the program is killed and its pipes are closed five seconds later at most, so a call is bounded by its caller and a wake by its ten seconds.

## The advance wakes after releasing the lock

The engine's outbox in an advance records each send under the task lock and keeps the ids of the messages sent to the Director; the advance wakes for them, one wake per message in send order as before, once it has released the lock, whether or not it succeeded.
The lock is then held for records and launches only.
A second advance of the task may run while the first one wakes; both may wake for one message, which the wake already allows, and the first decision recorded stands.
Sends outside an advance, `messages send` and the launcher's `worker-stalled` and `advance-failed`, hold no task lock and still wake straight after the send.
