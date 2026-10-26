# Pull-request mergeability belongs to the Director

Guildhall observes mergeability but never rebases automatically.
Validate and deliver report a conflicting PR or a branch-protection-blocked behind PR and wait for the Director.
A report identifies the PR, head, base, behind count and conflicting paths, and offers send-back, hold and abandon as independent options rather than a recovery procedure.
Reports are durable documents and inbox messages, deduplicated by head and state across validate and deliver.
Unknown mergeability remains an ordinary wait rather than an assumed conflict.
The forge reads mergeability through a narrow observation interface, using valid command-specific gh arguments, and computes local evidence against the exact fetched base commit without changing a worktree.

`step send-back` names a succeeded Artisan step and its attempt, and requires Director feedback.
It records immutable feedback before starting a new attempt, retaining the old attempts and review evidence.
Replays of the same request do not start another attempt; changing its feedback is refused.
A running attempt anywhere in the task prevents a new send-back request.
Normal changed-input propagation reruns downstream review, publish and validation; existing rebase classification decides whether review can scope to the resolution, and publish updates the same open PR.
This is not a retry of a failed attempt and does not change `step retry` semantics.
All advancement retains the existing task lock, admission holds, caps and review gates.
