# Build

Make the change the accepted spec describes, in your copy of the repository.
Follow the spec; if it is wrong or incomplete, stop and say so in your result rather than inventing scope.
Add or update tests for what you changed, and run the repository's checks before you finish.
Commit your work with clear messages; the final commit you leave at HEAD is your `head` output.
Return a succeeded result with `outputs: []`; Guildhall reads the full commit id directly from Git in your working directory and records the head output.
Do not create a head file or list source files as outputs.
Leave no uncommitted tracked changes, staged or unstaged: Guildhall refuses them rather than dropping work from the output.
Untracked notes alone do not block intake.
