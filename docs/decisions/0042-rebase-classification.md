# D42. Rebase classification through a git boundary

Rebase-aware re-review (director slice S6b part 3, steering 029) needs `git range-diff`, so Guildhall gains its first git boundary, `internal/git`, which runs the git program with argument lists rather than reading repositories through a Go library: range-diff has no library equivalent, and git is already a doctor prerequisite.
The classifier answers one question, whether a new commit range is a rebase of the reviewed one, and is conservative: every check must pass for a narrowed review, and any failure or doubt, including a git error, is a full review with a reason rather than an error, so a broken repository can only widen review.
A base change to a file the task touches is a full review, which is stricter than Firstmate's interim helper: the helper sends such conflict resolutions to a narrowed review, but the slice's tests require a full one.
The commit ranges are explicit inputs; where the base comes from and when the interpreter asks are the follow-up engine wiring, where the caller supplies the base and a missing one means a full review.
See the [git contract's tests](../contracts/git.md#tests) for the contract suite and scripted double.
