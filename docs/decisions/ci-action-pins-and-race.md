---
status: accepted
date: 2026-10-31
topic: ci
tasks: d78, d84, d89, d93
amends: ci-matrix-and-lint.md, release-policy-regression-test.md
---

# CI pins actions by commit, runs a Linux race step and runs the release test

## Context

Hallmark d78 finding H11 found three gaps in CI: the [release policy test](release-policy-regression-test.md) was run by no workflow, every action was pinned by a movable tag, and the suite never ran under `-race`.

## Decision

- **Actions pinned by commit SHA.**
  Every `uses:` in `.github/workflows` names a full 40-hex commit SHA, with the tag it was resolved from as a trailing comment, such as `actions/checkout@<sha> # v7`.
  This extends [the exact lint pin](ci-matrix-and-lint.md) to the actions themselves, so a moved tag can no longer change CI under a pull request.
  A pin is resolved from its tag with annotated tags dereferenced, for example `git ls-remote https://github.com/actions/checkout 'refs/tags/v7^{}'`.
- **Race detector on Linux only.**
  The `test (ubuntu-latest)` leg runs `go test -race ./...` as an extra step after the plain `Test` step and the release test, with the same `GUILDHALL_CI_REQUIRE` so [required skips](ci-required-skips.md) still fail.
  A race the step finds is filed as its own task rather than fixed in the change that found it.
  The step's first run failed, and guildhall/d89 (`e0993c47`) fixed that failure.
  The step is now blocking: a race failure fails the `test (ubuntu-latest)` job and so blocks merges; main run 41154510329 at `2420a81e` confirmed it green, and so did guildhall/d93's own CI run.
  The step sits after the release test so a red race run cannot skip it.
- **The release test in CI.**
  The same Linux leg runs `.github/tests/test_release.py` on Python 3.12 from `actions/setup-python`, with PyYAML 6.0.1 installed by pip, which makes the release test's "Python 3" exact.
  3.12 is chosen because PyYAML 6.0.1 is understood to publish wheels up to CPython 3.12, which this change's first CI run checked.
- All new steps sit in the existing `test` job, so they are part of the already-required `test (ubuntu-latest)` check without editing branch protection.

## Alternatives rejected

- Race runs on every platform: `-race` needs cgo and a C compiler, which the Linux runner has, and one race-enabled run finds what the other legs would.
- The runner's system Python: it is externally managed (PEP 668) and refuses `pip install`.
- A new CI job: it would need a branch-protection change to be required.

## Consequences

There is no Dependabot, so pins are updated by hand, and the comment is updated with the SHA.
The plain `Test` step still runs on all three operating systems.
