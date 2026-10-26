# Validate the GitHub forge's command-specific CLI syntax

The D3 publish failure exposed a false assumption in both the GitHub implementation and its stand-in: `gh repo view` does not take the `--repo` flag accepted by pull-request commands.
Use the existing `github.com/<owner>/<repo>` identity as the positional argument to `repo view`.
An offline probe against gh 2.102.0 confirmed that this form resolves the expected owner and repository on api.github.com, so no `GH_HOST` override or repository-identity change is needed.

Keep real git integration tests and make the gh process stand-in validate the supported command-specific subset before executing any operation.
Reject unsupported flags, missing flag values and incorrect positional counts, including the original `repo view --repo` call.
Check every supported call's flags, plus actual recorded forge calls, with an installed gh using `--help` when available.
This is credential-free and network-free, but help does not validate positional arity or GitHub server behavior; the stand-in's shape tests and existing real-git contract suite cover the former.
Do not describe these tests as a live GitHub integration run.

The audit covered `repo view`, `pr list`, `pr create`, `pr view`, `pr merge`, `api graphql`, `git cat-file -e` and the task-branch push refspec.
Installed command help confirmed the remaining emitted flags, including `--squash`, `--match-head-commit`, `--hostname` and `-f`.
No production caller shells out to `gh pr edit`, and the only production gh process boundary is `internal/forge/github.go`.
Merge continues to omit `--admin` and `--auto`; publish continues to leave base selection to the remote's default branch.

The strict stand-in was added before the production fix and reproduced the publish failure in `TestGitHubArguments`.
The command-shape tests are a retrofit of an existing boundary, not evidence that the original implementation was developed against real gh.
