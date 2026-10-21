# Owned attempt commands compose in internal/launch

`artisan run`, `stop` and `status` are thin commands over one composition package, `internal/launch`, which alone imports the adapter, artisan, store and reconcile together; `artisan` stays free of the adapter, and the command line only parses flags and prints JSON.
The launcher opens the Director store for each record operation and closes it before the next, so a running attempt never holds the store's single-writer lock and other commands work meanwhile; ownership is a store record, not a held handle.
`run` takes ownership with a containment-aware probe, so a crashed launcher whose recorded unit is proven empty can be replaced, while a live launcher or surviving members refuse.
Survivors after the last rung leave the store attempt running and owned, because finishing it would release the record a later sweep needs.
`stop` only asks the owner through its endpoint and otherwise requires verified termination; classification alone is not proof. Missing containment prevents takeover even after the launcher exits.
This PR is L7 part 1: `internal/launch`, F2 real AttemptFiles, F3 attemptOutcome, `artisan run/stop/status`, and the P6 custody test. L7 part 2 holds `artisan resume` with `artisan.Sweep` (signalling only after the owner is proven exited), `resume_argv` registration through `pane report-agent --source guildhall`, the fake Artisan's P0 directives, the `artisan harness` subcommand, the Tier 2 lab runs, and retirement of the Windows-only worker stop path after the native Windows acceptance case passes.
The direct-child mode of `artisan run` is replaced by the owned mode; the Herdr tab mode stays until L7 part 2 runs the launcher in the pane.
`run` exits 0 for a succeeded attempt, 4 for a failed one and 5 for a stopped one, from the store outcome.
