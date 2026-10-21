# `internal/artisan/artisantest`

## Contract

`RunUnitContract`, the containment contract every `Unit` passes, and the worker the test binary plays, which records its own identity in an inventory.
A harness states what its backend can do: terminate, whether a `setsid` grandchild escapes or stays a member, refused breakaway, and the containment kind it reports.
A worker can exit right after starting its grandchildren, orphaning them, honour a cancel marker by exiting, or publish a result using the delay and marker handshake defined by [`Worker`](../../internal/artisan/artisantest/worker.go).
`RunStopContract`, the launcher's stop ladder and precedence contract, runs a `Launcher` over units from a harness with `Files`, the double of `artisan.AttemptFiles` over a result file and a marker file.
Stop timing is selected by [`Harness.Ladder`](../../internal/artisan/artisantest/contract.go), allowing the fake harness to avoid native process waits without changing the shared assertions.
A harness may give its own attempt files as `StopFiles`: the paths its workers publish at and honour, the reader the launcher decides by and a result body that reader accepts, which the worker publishes as `ResultBody`.

## Tests

Run by `internal/artisan`
