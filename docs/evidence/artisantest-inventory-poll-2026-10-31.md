# Artisantest inventory poll and launch cleanup (d111), 2026-10-31

Windows CI run 41187907325 failed `TestStopContract/L16_a_result_after_the_marker_is_rejected_as_late` after 120.73s:

```text
stop.go:160: open …\worker-7020.json: The process cannot access the file because it is being used by another process.
stop.go:329: Run did not return within 2m0s
contract.go:258: unrelated sentinel {7144 1793438197387163800} is gone (<nil>) after the case
```

`entriesIn` failed the case on the first inventory read error; nothing then stopped `Run`, so the cleanup waited out the worker's lifetime (equal to `deadline` on CI), and the sentinel's fixed two-minute lifetime ended first.

## Unit regression, red before the fix

`go test ./internal/artisan/artisantest -run 'TestEntriesIn|TestInventoryOf' -count=1 -v`, with only the `readInventory` seam added:

```text
=== RUN   TestEntriesInPollsThroughReadErrors
    contract_internal_test.go:90: entriesIn failed on a transient read error: ["open C:\\Temp\\002\\worker-7020.json: The process cannot access the file because it is being used by another process."]
--- FAIL: TestEntriesInPollsThroughReadErrors (0.00s)
=== RUN   TestEntriesInFailsAtOnceOnAnInvalidRecord
--- PASS: TestEntriesInFailsAtOnceOnAnInvalidRecord (0.00s)
=== RUN   TestEntriesInNamesAPersistentReadErrorAtTheDeadline
    contract_internal_test.go:133: entriesIn gave up after 0s, want the full 1m0s
--- FAIL: TestEntriesInNamesAPersistentReadErrorAtTheDeadline (0.00s)
=== RUN   TestInventoryOfRetriesAFailedRead
    contract_internal_test.go:155: inventoryOf = [], failures []; want the one entry and no failure
    contract_internal_test.go:163: inventoryOf failures [] (fatal false), want one fatal naming "open C:\\Temp\\002\\worker-7020.json: The process cannot access the file because it is being used by another process."
--- FAIL: TestInventoryOfRetriesAFailedRead (0.00s)
FAIL
```

After the fix all four pass in synthetic time.

## Fault-injected runs of the stop case

A temporary, uncommitted `init` swapped `readInventory` for a reader returning the CI's sharing-violation text once a `worker` record existed, then ran `go test ./internal/artisan -run 'TestStopContract/L16_a_result_after_the_marker' -count=1 -v` on Linux (WSL), where `deadline` is 1m and the worker lifetime 30s.

- [Red](artisantest-inventory-poll-2026-10-31-cleanup-red.txt), before the fix: the case fails on the first read error, then waits out the worker's whole 30s lifetime (30.08s). On CI the lifetime equals `deadline`, which produced the "Run did not return" and false sentinel lines.
- [Green, error lasting until entriesIn's deadline](artisantest-inventory-poll-2026-10-31-cleanup-green.txt): the case fails at `entriesIn`'s 1m deadline naming the last read error; the cleanup's `Stop` ends `Run` about a second later (61.09s total). No "Run did not return" line and no sentinel line.
- [Green, error lasting one second](artisantest-inventory-poll-2026-10-31-transient-green.txt), the CI case: the case polls through the error and passes in 1.10s. Before the fix this injection fails exactly as the red run does.
