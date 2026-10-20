# `internal/artisan/artisantest`

## Contract

`RunUnitContract`, the containment contract every `Unit` passes, and the worker the test binary plays, which records its own identity in an inventory.
A harness states what its backend can do: terminate, whether a `setsid` grandchild escapes or stays a member, refused breakaway, and the containment kind it reports.
A worker can exit right after starting its grandchildren, orphaning them.

## Tests

Run by `internal/artisan`
