# Containment persists as a recovery description, not a name

The store's ownership record describes the owner's containment unit with the same fields as Artisan's `Containment` (kind, name, Linux detection reason) plus the worker identity, so every backend's description is stored as it is reported ([containment record](../store.md#containment-record)).
Before this, the store required every containment to have a name, which a process group does not have: the Linux fallback and macOS backends could only be recorded with an invented name, such as the group id, which is reused once its leader is gone.
A process group is found by its leader, so its record carries the worker's PID and creation token instead and must not carry a name; a job or cgroup keeps its name.
The store owns the record's kind vocabulary as plain strings equal to Artisan's kinds, and a composition test in `internal/artisan` stores each backend's description, so the two cannot drift silently.
`Containment.Recoverable` checks structural recovery evidence, not whether a unit is empty; a record that fails it, including an older incomplete one, is still readable but never authorises a sweep or a take-over.
Unknown identity or surviving contained processes never grants take-over: probing only the launcher cannot rule out surviving workers or descendants.
The [attempt ownership contract](../store.md#attempt-ownership) defines the conservative default probe and explicit containment-aware recovery path; `TestTakeOwnershipPreservesContainmentAfterLauncherExit` in `internal/store/process_test.go` pins the refusal without discarding recovery evidence.
Recorded recovery evidence is also monotonic: `UpdateOwnership` refuses an update that clears or changes a recorded containment or endpoint, since `TakeOwnership`'s refusal only protects evidence still in the record; `TestProgressUpdateCannotEraseRecoveryEvidence` in `internal/store/process_test.go` pins it with real processes.
The store still imports `artisan` for identities and the default probe; moving those to store-neutral values with a launcher-supplied probe is a separate change.
