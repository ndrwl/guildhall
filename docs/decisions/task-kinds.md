# Task kinds

A task is created as a task kind, `feature`, `bug` or `hallmark`, with `task new --kind`; `feature` is the default and `--fixes` without `--kind` means `bug`.
The `feature` kind runs the repository's `workflow` setting, the shipped `default` unchanged byte for byte, so its digest, existing tasks and approvals are unaffected; there is no second shipped definition named `feature`, which would be the same content under another digest.
The `bug` kind runs the new shipped `bug` workflow, the default's steps without grill and accept ([decision](0095-bug-workflow-without-acceptance.md)) with [research, plan, build and review instructions](research-build-instructions.md), or the workflow a repository names in `[repository.kinds]`.
That table accepts only `bug` and, since d74, `hallmark`: `feature` is refused because `workflow` already names it, and it is omitted from JSON when unset so existing config digests do not change.
The `hallmark` kind runs the shipped report-only `hallmark` workflow, which finishes as done without a pull request ([decision](report-only-workflows.md)).

The task records no kind: its bound workflow's name already says what it was created as, so the store and its goldens are unchanged.
The kind descriptions live in Go, shown by `task new --help` and copied into the Director's tasks skill, so rewording them never changes a workflow digest.
Go names it `taskKind` and `TaskKind…`, never a bare `Kind`, because `workflow.Kind` is the step kind.
`workflow approve --config --repo` takes `--kind` too, because the shipped `bug` with a defaulted reviewer's profile needs the Guildmaster's approval like the default does.
