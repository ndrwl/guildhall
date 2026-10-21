# `internal/git/gittest`

## Contract

The shared classification contract suite, `RunClassifierContract` with its `Harness` and `Case` histories, and `Script`, a scripted classifier for consumer tests.
`Script` answers what `Add` scripted for a repository and pair of ranges, a full review with a reason for anything else, and an error for an ended context; it records each call.
It passes the suite because its harness scripts each case's answer, so it pins the shape of answers, not git's behaviour.

## Tests

`internal/git/git_test.go` runs the suite against `Script`.
