# `internal/launch`

## Contract

The composition package behind `artisan run`, `stop` and `status` ([commands](../artisan.md#commands)); its [doc comments](../../internal/launch/launch.go) own the API contract.
`Attempt` names an owned attempt by store, state directory and key, with its directory, adapter identity and `Files`.
`Files` is the real `artisan.AttemptFiles`: the adapter's identity-bound, version-checked result and its `.cancelled` sidecar; a missing or invalid result is not valid, an unreadable one is an error, and a late result is still seen while the marker is present.
`attemptOutcome` is the one translation of a launcher outcome and result into the store's outcome ([glossary](../glossary.md#outcome)).
`Run` takes ownership with `ProbeOwner`, binds the endpoint, records the containment once the worker starts, supervises with the stop ladder and finishes the store attempt, leaving it owned when survivors remain; it opens the store for each record operation only.
`ProbeOwner` is alive while the recorded launcher lives and gone only when it is gone and its recorded unit is proven empty.
`Stop` asks the owner through the endpoint and otherwise classifies, never signalling; `ErrNotStopped` when the attempt may still run.
`Status` is `reconcile.Classify` over the attempt's files and ownership record.

## Tests

Written red first against stub bodies, every case failing; the red runs are in the pull request description.
`outcome_test.go` is the translation table over every launcher state and result.
`files_test.go` reads missing, malformed, other-attempt, newer-version and draft results as not valid, a valid result before and after the marker, an idempotent marker, and refuses another attempt's marker; `TestStopContractRealFiles` runs `artisantest.RunStopContract` with `Files` on this platform's units, the test binary playing a worker that publishes a real version 1 result and honours the sidecar.
`launch_test.go` runs owned attempts against a real store with real units and processes: a succeeded and a failed result finished and recorded with the ownership released naming the launcher, endpoint and containment; a worker exiting without a result failed; a live owner refused with nothing started and the record unchanged; a stop through the endpoint cancelled at rung 1 and recorded stopped, and a second stop; the context ending the attempt past rung 1; a stop without an owner only classifying; and a silent live owner not stopped and not marked.
`TestArtisanRunCustody` in `internal/cli` is the launcher and store custody acceptance: a launcher process owns a fake worker while separate processes read its status, send a message, answer a decision and are refused a competing run, each within ten seconds and leaving the ownership record unchanged; a stop through the endpoint then ends it with exit status 5.
After the red commit the launcher gained one event: `rung-emptied` for rung 1 when the marker empties the tree before the ladder's first check, which the stop contract already required and the real files' slower marker write exposed.
