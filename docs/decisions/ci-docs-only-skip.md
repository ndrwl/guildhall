---
status: accepted
date: 2026-11-04
topic: ci
tasks: d205
---

# Docs-only pull requests skip the Go CI steps and run only the docs tests

## Context

Every pull request ran the whole Go suite on Linux, Windows and macOS and in the three [race shards](ci-race-shards.md), about 12–14 min, even when it changed only documentation.
About 3.5% of commits are docs-only, so the saving is small on average, but such a pull request still waits behind the full run, and again after every rebase the up-to-date rule forces (H9 of [the CI speed evidence](../evidence/ci-speed-2026-11.md)).
Branch protection requires the `test (…)` and `race (…)` checks by name, so each must still report success.

## Decision

- **The docs set.**
  A pull request is docs-only when `git diff --name-only HEAD^1 HEAD` on its merge commit succeeds, lists at least one file, and every listed path is under `docs/` or is exactly `README.md` or `CONTRIBUTING.md`.
  Markdown anywhere else is not docs: `skills/`, `baseline/` and `internal/` Markdown is compiled in with `go:embed`, and `.guildhall/policies.md` is read by Guildhall.
  `push` to main, `workflow_dispatch`, an empty diff and a git error always run everything.
- **Step-level skips.**
  Both jobs check out with `fetch-depth: 2` and first run a `Classify changes` step (id `changes`), plain bash and git, which logs the changed paths and writes `docs_only=true` or `false`.
  In each `test (<os>)` job every later step except setup-go and the [rolling Go cache](ci-go-cache.md) restore (with the step that finds its paths) carries `if: steps.changes.outputs.docs_only != 'true'`, combined with `&&` with any existing condition; that includes the test timing summariser's own vet, test and build, and the timings upload, which runs `always()` otherwise but has nothing to upload here; the restore stays so the docs tests compile warm, and the cache save already never runs on a pull request.
  Each `race (N)` shard skips setup-go and the cache restore too, so on a docs-only pull request it only checks out, classifies and succeeds.
  The docs tests keep `-count=1`, as every `go test` step must under the rolling cache.
- **A fixed docs-test list.**
  On a docs-only pull request each `test (<os>)` job runs a `Docs tests` step: `go test ./internal/doccheck/...`, then `TestEventsDocumented` (`./internal/artisan`), `TestHelpNamesReadmeCommands` (`./internal/cli`) and `TestDirectorSkillsNameEveryReadmeCommand` (`./skills`) with anchored `-run` and `-v`.
  The step fails unless the output has `--- PASS: <name>` for each of the three, so a renamed or moved test cannot silently match nothing.
  The race shards do not repeat the docs tests.

## Alternatives rejected

- Workflow-level `paths-ignore`: a skipped workflow reports no checks, so the required checks would stay pending and block the merge.
- A job-level `if:`: a skipped job reports as skipped rather than success, and computing the decision needs its own job, which adds a check name and a `needs:` that delays every other check run.
- Running the full suite of every package that reads docs: it is most of the suite, which is the cost this removes.

## Consequences

The docs-test list can drift: a new test that reads `docs/`, `README.md` or `CONTRIBUTING.md` must be added to the `Docs tests` step in `.github/workflows/ci.yml` ([CONTRIBUTING](../../CONTRIBUTING.md)).
If it is not, a docs-only pull request can pass while breaking that test, but main always runs the full suite, so the merge run on main turns red.
A pull request that touches `ci.yml` itself is never docs-only, so a change to this logic runs every step.
