# Build

Make the change the accepted spec describes, in your copy of the repository.
Follow the spec; if it is wrong or incomplete, stop and say so rather than inventing scope.
Add or update tests for what you changed, and run the repository's checks before you finish.
Run checks so that they print failures only, for example `go test` without `-v`, and send long output to a file and read its tail or search it, rather than printing it whole.
When you need more detail, rerun only the failing test or command with verbose output.
When your feedback is a failed validate result, its `logs[]` holds each failed CI job's log excerpt; to see more, run `logs[].command` with `--out <file>` and search the file, or with `--tail <lines>`, rather than printing the whole log.
Commit your work with clear messages; the final commit you leave at HEAD is your `head` output.
Stay on the task's branch; to move between branches use `git switch`, not `git checkout`, and do not push, since publishing is a later stage.
Leave no uncommitted tracked changes, staged or unstaged: Guildhall refuses them rather than dropping work from the output.
Untracked notes alone do not block intake.

When an earlier slice or a merged pull request already delivered everything the current slice asks for, commit nothing and report `failed`.
Start the summary with `Already delivered by slice <j>` or `Already delivered by <pr url>`, then give the evidence: which item each named slice or pull request delivers.
The Director confirms it with `guildhall slice delivered`, which closes the slice.
