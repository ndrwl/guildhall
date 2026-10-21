# Owned attempt commands compose in internal/launch

`artisan run`, `stop` and `status` are thin commands over one composition package, `internal/launch`, which alone imports the adapter, artisan, store and reconcile together; `artisan` stays free of the adapter, and the command line only parses flags and prints JSON.
The launcher opens the Director store for each record operation and closes it before the next, so a running attempt never holds the store's single-writer lock and other commands work meanwhile; ownership is a store record, not a held handle.
`run` takes ownership with a containment-aware probe, so a crashed launcher whose recorded unit is proven empty can be replaced, while a live launcher or surviving members refuse.
Survivors after the last rung leave the store attempt running and owned, because finishing it would release the record a later sweep needs.
`stop` only asks the owner through its endpoint and otherwise classifies; signalling from outside the owner is `resume`'s, after the owner is proven exited.
The direct-child mode of `artisan run` is replaced by the owned mode; the Herdr tab mode stays until the next slice runs the launcher in the pane and retires the Windows-only worker stop path.
`run` exits 0 for a succeeded attempt, 4 for a failed one and 5 for a stopped one, from the store outcome.
