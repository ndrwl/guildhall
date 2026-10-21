# `internal/launch`

## Contract

The composition package behind `artisan run`, `stop` and `status` ([commands](../artisan.md#commands)); its [doc comments](../../internal/launch/launch.go) own the API contract.
`Attempt` names an owned attempt by store, state directory and key, with its directory, adapter identity and `Files`.
`Files` is the real `artisan.AttemptFiles`: the adapter's identity-bound, version-checked result and its `.cancelled` sidecar; a missing or invalid result is not valid, an unreadable one is an error, and a late result is still seen while the marker is present.
`attemptOutcome` is the one translation of a launcher outcome and result into the store's outcome ([glossary](../glossary.md#outcome)).
`Run` prepares the command, endpoint path, directory, unit and launcher before taking ownership with `ProbeOwner`. It refuses an existing outcome with `artisan.ErrDecided` during preflight, preserving prior ownership and containment evidence rather than finalizing the attempt. It then binds and records the endpoint; either failure finishes the known-unstarted attempt as failed and returns the setup error joined with any finalization error. After setup it records the containment once the worker starts, supervises with the stop ladder and finishes the store attempt, leaving it owned when survivors remain; it opens the store for each record operation only.
Worker startup errors, including containment persistence errors, remain in `Run`'s returned error while the launcher decides and writes the outcome and the store records it.
`ProbeOwner` is alive while the recorded launcher lives and gone only when it is gone and its recorded unit is proven empty.
`Stop` asks the owner through the endpoint, preserving request failures and never signalling itself. Without a successful answer it requires no ownership record, or a gone launcher and a recorded unit proven empty; otherwise it returns `ErrNotStopped`, even for terminal classifications. Missing containment is unverified for both stop and takeover.
`Status` is `reconcile.Classify` over the attempt's files and ownership record.

## Tests

The original cases were written red first against stub bodies, every case failing; the red runs are in the pull request description. Review regressions were added later.
`outcome_test.go` is the translation table over every launcher state and result.
`files_test.go` reads missing, malformed, other-attempt, newer-version and draft results as not valid, a valid result before and after the marker, an idempotent marker, and refuses another attempt's marker; `TestStopContractRealFiles` runs `artisantest.RunStopContract` with `Files` on this platform's units, the test binary playing a worker that publishes a real version 1 result and honours the sidecar.
`launch_test.go` runs owned attempts against a real store with real units and processes: a succeeded and a failed result finished and recorded with the ownership released naming the launcher, endpoint and containment; a worker exiting without a result failed; a live owner refused with nothing started and the record unchanged; a stop through the endpoint cancelled at rung 1 and recorded stopped, and a second stop refused while the released launcher still lives; the context ending the attempt past rung 1; a stop without an owner only classifying; and a silent live owner not stopped and not marked.
`recovery_test.go` checks refused takeover without recorded containment, successful takeover with an emptied real unit, terminal decisions with live owners, missing containment, surviving workers or verified empty units, and preservation of endpoint stop failures.
`decided_test.go` checks that finished, cancelled and failed outcomes refuse a recovery run without changing ownership, containment evidence, the persisted outcome or the store attempt, both with a usable endpoint path and one blocked by a directory.
`setup_failure_test.go` checks that an overlong endpoint path leaves no ownership or worker and permits a corrected retry, and that a blocked endpoint bind records a failed attempt and releases ownership without starting a worker.
`start_failure_test.go` is a review regression: a nonexistent working directory preserves the startup error, writes the failed outcome, records the failed store attempt and releases ownership without starting a worker.
`TestArtisanRunCustody` in `internal/cli` is the launcher and store custody acceptance: a launcher process owns a fake worker while separate processes read its status, send a message, answer a decision and are refused a competing run, each within ten seconds and leaving the ownership record unchanged; a stop through the endpoint then ends it with exit status 5.
After the red commit the launcher gained one event: `rung-emptied` for rung 1 when the marker empties the tree before the ladder's first check, which the stop contract already required and the real files' slower marker write exposed.
