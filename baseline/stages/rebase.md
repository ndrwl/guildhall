# Rebase

Guildhall replayed the task's change onto the base branch's tip and the replay conflicts.
Your working directory is at that replay, committed with the conflict markers written into the files your `conflicts` input lists.
The input also gives the commits on both sides that touch those files; the `spec` input is the accepted spec the change was built to.
Resolve every conflict so the change still does what the spec asks and keeps what the base branch changed.
Change nothing outside the conflicting hunks, and do not rework, extend or tidy the change.
Run the repository's checks, then commit the resolution on top of the replay; the final commit you leave at HEAD is your `head` output.
Leave no conflict markers and no uncommitted tracked changes.
Write `rebase.json` with `behaviour_change` and `reason`, as the required outputs below describe.
Set `behaviour_change` to true when no resolution keeps both sides' behaviour, or when keeping it needs more than the conflicting hunks; Guildhall then sends the build back.
