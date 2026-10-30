# Build

Make the change the accepted spec describes, in your copy of the repository.
Follow the spec; if it is wrong or incomplete, stop and say so rather than inventing scope.
Add or update tests for what you changed, and run the repository's checks before you finish.
When your feedback is a failed validate result, its `logs[]` holds each failed CI job's log excerpt, and running `logs[].command` with `--out <file>` or `--tail <lines>` fetches that job's full log.
Commit your work with clear messages; the final commit you leave at HEAD is your `head` output.
Stay on the task's branch; to move between branches use `git switch`, not `git checkout`, and do not push, since publishing is a later stage.
Leave no uncommitted tracked changes, staged or unstaged: Guildhall refuses them rather than dropping work from the output.
Untracked notes alone do not block intake.
