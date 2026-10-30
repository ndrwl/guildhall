# Failed CI job logs reach Artisans through Guildhall

A builder sent back by a failed validate round got the failing checks' names and URLs, and nothing of why they failed: it could not read a CI log, so it guessed (guildhall/d32 build-6, guildhall/d42 build-4).
Now validate puts a short excerpt of each failed GitHub Actions job's log into the failed result, and `guildhall ci log` serves the full log ([validate](../interpreter.md#validate), [CI logs](../ci-logs.md)).

## Guildhall fetches the logs; Artisans get no `gh`

The simpler stopgap was to grant the builder `gh run view` or `gh pr checks`.
It was not taken:

- an Artisan would run `gh` with the Guildmaster's credentials, with whatever reach those have, where `forge.GitHub` keeps them in gh's own sign-in store and passes none;
- a `gh` rule is adapter-specific and would have to be kept in step for every runtime;
- a raw log dump spends the Artisan's tokens before it reaches the cause.

So Guildhall reads the log with its own `gh` (`forge.GitHub.CheckLog`, `gh api repos/<owner>/<repo>/actions/jobs/<job>/logs`), and every claude profile is granted only `Bash(guildhall ci log:*)`.
That command is read-only and serves only the jobs its task's failed validate results record, so it is given to every claude profile, not only the builder, and is added even when a copy sets a profile's own `allowed_tools`.
No `gh`, `curl` or GitHub API rule is granted.

## Logs come from a cache, not from store documents

An earlier plan recorded each full log as a store document and gave it to the builder as a second feedback input.
That was changed at acceptance (accept-3): recorded documents are kept for good and the pruning of old task state (d46) must be free to drop logs, and multi-document feedback would have changed how loops serve and stale their feedback.
So only the excerpt is recorded, inside the one result document the builder already gets, and the full log lives in a disposable cache, `<state>/<repo>/<task>/ci-logs/<job>.log`, beside `<step>.checks.json`.
The cache is written by rename, so a concurrent reader never sees half a log, and deleting it at any time only makes the next read fetch again.
Because the result is the only write, a crash before it is recorded replays to the same excerpt from the cache, or to a fresh fetch once the cache is pruned; nothing recorded can disagree with it.

## Caps

- `ci_log_limit`, default 5 MiB and 64 KiB to 100 MiB, caps each cached log, keeping its tail from a line boundary, because GitHub allows job logs far longer; `"0"` turns fetching off.
  gh's output streams through the cap, so memory stays within twice it.
- At most 10 failed checks are fetched per round, in the order of the checks, so a matrix failing everywhere does not stall one advance; the rest name the cap and keep their command.
- The excerpt is the last 200 lines, at most 32 KiB, of the step holding the first `##[error]`, so the result the builder reads stays short.

## Scope

GitHub Actions jobs only: another check app's runs and commit statuses have no log Guildhall can read and are marked unavailable.
A log that cannot be had, for any reason but a cancelled advance, is an `unavailable` entry and never fails validate.
The checks the merge queue reads for a rebased head (`<deliver>-<k>-checks-<head>`) are out of scope (accept-1): that path escalates with a reason and records no validate result, so the rebaser, which can run the command, gets no excerpt.
The reviewer's inputs, the Director's send-back text and other forges are unchanged.
`gh api` follows the endpoint's redirect to the plain-text log, as a live failed job confirmed on 2026-10-30 ([evidence](../evidence/failed-job-logs-redirect-2026-10-30.txt)), so the `gh run view --job <id> --log` fallback, which prefixes every line with the job and step names, was not needed.
gh prints that log only with `--allow-escape-sequences`, because it holds colour codes, and it starts with a byte order mark and ends lines with CRLF, so the excerpt removes all three before it matches a line.
