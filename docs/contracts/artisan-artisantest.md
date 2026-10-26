# `internal/artisan/artisantest`

## Contract

`RunUnitContract`, the containment contract every `Unit` passes, and the worker the test binary plays, which records its own identity in an inventory through `artisan.RecordProcess`; `Inventory` reads it as `Entry`, an alias of `artisan.ProcessRecord`, and treats a directory not yet created as empty.
The roles `grandchild-setsid`, `breakaway-refused` and `breakaway-succeeded` carry test metadata.
A breakaway request is recorded under the worker's own identity, since a record needs a token.
A harness states what its backend can do: terminate, whether a `setsid` grandchild escapes or stays a member, refused breakaway, and the containment kind it reports.
A worker can exit right after starting its grandchildren, orphaning them, honour a cancel marker by exiting, or publish a result using the delay and marker handshake defined by [`Worker`](../../internal/artisan/artisantest/worker.go).
`RunStopContract`, the launcher's stop ladder and precedence contract, runs a `Launcher` over units from a harness with `Files`, the double of `artisan.AttemptFiles` over a result file and a marker file.
Stop timing is selected by [`Harness.Ladder`](../../internal/artisan/artisantest/contract.go); both real and fake harnesses follow the [test timing policy](../test-timing.md), since marker observation still needs goroutine scheduling.
On Windows `RunStopContract` also holds a published result without sharing while the launcher decides and requires `finished`, so every adapter must read through `internal/atomicfile`; the case reads the result path through `Harness.StopFiles`, the one accessor for a harness's files.
`Harness.Worker` replaces the test binary as the stop contract's worker with a real Artisan's command, which records its processes in the case's inventory directory; the fake Artisan plays it in `internal/launch`.
`KillVerified` is `artisan.Kill`.
A harness may give its own attempt files as `StopFiles`: the paths its workers publish at and honour, the reader the launcher decides by and a result body that reader accepts, which the worker publishes as `ResultBody`.

## Tests

Run by `internal/artisan`
