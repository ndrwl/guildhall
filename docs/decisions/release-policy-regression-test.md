---
status: accepted
date: 2026-10-20
topic: release
amended-by: ci-action-pins-and-race.md
formerly: D7, 0007-release-policy-regression-test.md
---

# The release policy test parses the workflow with Python and PyYAML, as development-only dependencies

## Context

The [release builds](release-builds.md) policy, that only a pushed version tag publishes, lives in the release workflow's YAML, where a careless edit could publish from a branch.
Matching the YAML's source text would pass or fail on formatting rather than on the policy.

## Decision

The focused release policy test, `.github/tests/test_release.py`, uses Python 3 and PyYAML 6.0.1 to parse the workflow into a semantic model rather than matching YAML source text.
These are development-only test dependencies; the binary remains standard-library-only.
What the test checks is described with the [release workflow test](../contracts/cmd-guildhall.md#release-workflow-test).

## Alternatives rejected

- Matching the workflow's source text: it tests formatting, not the publication condition.

## Consequences

Running the test needs Python 3 and PyYAML, which a Go-only contributor may not have.
[CI action pins and race](ci-action-pins-and-race.md) runs it in CI and pins the Python version.
