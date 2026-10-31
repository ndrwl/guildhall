---
status: accepted
date: 2026-10-30
topic: ci
tasks: d45, d81
---

# Guildhall fetches failed CI job logs for Artisans, who get no `gh`

## Context

A builder sent back by a failed validate round got the failing checks' names and URLs, and nothing of why they failed: it could not read a CI log, so it guessed (guildhall/d32 build-6, guildhall/d42 build-4).

## Decision

Validate puts a short excerpt of each failed GitHub Actions job's log into the failed result, and `guildhall ci log` serves the full log ([validate](../interpreter.md#validate), [CI logs](../ci-logs.md)).
Guildhall reads the log with its own `gh` (`forge.GitHub.CheckLog`, `gh api repos/<owner>/<repo>/actions/jobs/<job>/logs`).
Every claude profile is granted only `Bash(guildhall ci log:*)`, even when a copy sets a profile's own `allowed_tools`, because the command is read-only and serves only the jobs its task's failed validate results record; no `gh`, `curl` or GitHub API rule is granted.
Only the excerpt is recorded, inside the one result document the builder already gets; the full log lives in a disposable cache, `<state>/<repo>/<task>/ci-logs/<job>.log`, written by rename.
Fetching is capped: `ci_log_limit` per cached log (default 5 MiB, keeping the tail; `"0"` turns fetching off), at most 10 failed checks per round, and an excerpt of the last 200 lines, at most 32 KiB, of the step holding the first `##[error]`.
A log that cannot be had, for any reason but a cancelled advance, is an `unavailable` entry and never fails validate.
`guildhall ci log` also serves the failed checks that a hallmark task's `baseline` input lists, from that digest-checked document's failed-jobs block (d81).

## Alternatives rejected

- Granting the builder `gh run view` or `gh pr checks`: an Artisan would run `gh` with the Guildmaster's credentials and whatever reach those have, where `forge.GitHub` keeps them in gh's own sign-in store and passes none; a `gh` rule is adapter-specific and would have to be kept in step for every runtime; and a raw log dump spends the Artisan's tokens before it reaches the cause.
- Recording each full log as a store document given to the builder as a second feedback input (changed at accept-3): recorded documents are kept for good while pruning old task state (d46) must be free to drop logs, and multi-document feedback would have changed how loops serve and stale their feedback.
- The `gh run view --job <id> --log` fallback: `gh api` follows the endpoint's redirect to the plain-text log, as a live failed job confirmed on 2026-10-30 ([evidence](../evidence/failed-job-logs-redirect-2026-10-30.txt)), while the fallback prefixes every line with the job and step names.

## Consequences

Deleting the cache at any time only makes the next read fetch again, and a crash before the result is recorded replays to the same excerpt or a fresh fetch, so nothing recorded can disagree with it.
Only GitHub Actions jobs have logs: other check apps' runs and commit statuses are marked unavailable.
The checks the merge queue reads for a rebased head (`<deliver>-<k>-checks-<head>`) are out of scope (accept-1): that path escalates with a reason and records no validate result, so the rebaser gets no excerpt.
The reviewer's inputs, the Director's send-back text and other forges are unchanged.
