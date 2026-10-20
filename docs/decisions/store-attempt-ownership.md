# Attempt ownership lives in the store

The record of which launcher owns a stage attempt is a store record written under the store's single-writer lock, replacing the scratch launcher's `owner.json` file and its separate kernel file lock ([attempt ownership](../store.md#attempt-ownership)).
The store already serializes writers across processes and makes every write durable with a receipt (D10), so a check-and-set inside one mutation gives the same exclusion as the scratch lock, plus replay by mutation id and an audit trail, without a second lock.
The store does not interpret liveness itself: `TakeOwnership` takes an `OwnerProbe`, whose default checks the recorded launcher identity with `artisan.Probe`, so the launcher can fold contained survivors into the answer and the tests can script live, unknown and exited owners deterministically.
The probe runs inside the write transaction; it is a bounded local process lookup, and running it there is what makes two concurrent take-overs unable to both succeed.
Ownership records use `TaskID` like every other store record rather than the `Task` field name in the slice plan, and the slice's `own:<repo>/<task>/<step>/<n>` mutation id becomes `own:<repo>:<task>:<step>:<n>:<launcher pid>`, because store mutation ids cannot contain `/` and a take-over needs an id of its own.
`UpdateOwnership` is added beside the take so the owner can record its containment unit, endpoint and `running` once they exist, since the endpoint is bound only after ownership is taken.
