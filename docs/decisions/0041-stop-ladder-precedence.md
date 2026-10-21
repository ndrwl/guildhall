# D41. Stop ladder decided from the attempt's files

The launcher decides each attempt once, from its files before its processes ([stop ladder](../artisan.md#stop-ladder)): a cancel marker means cancelled and makes any result late, otherwise a valid result means finished, otherwise a stop writes the marker.
The marker is written under an attempt lock (`attempt.lock`, `flock` on Unix and `LockFileEx` on Windows) after checking again for a result, so the launcher, and later a reconcile of a dead launcher's attempt, never decide the same attempt two ways; the worker takes no lock, because a result it publishes after the marker is late whatever order the files land in.
The decision goes to `outcome.json`, published with a hard link so it is never replaced.
Because an error reading the attempt's files decides it failed, every `AttemptFiles` adapter reads the result through the [bounded retry policy](../contracts/atomicfile.md) before returning an error to the launcher, and `RunStopContract` checks it on Windows for each adapter.
The launcher reads the result and writes the marker through the `AttemptFiles` seam rather than the adapter's result file and its `.cancelled` sidecar directly, so the ladder is tested with a file double.
The real `AttemptFiles` is the composition package's `launch.Files`, over the adapter's identity-bound, version-checked result parse and its `.cancelled` sidecar; `RunStopContract` runs against it, and `reconcile.Classify` consumes the same value, so marker and result have one reader each.
A rung counts only when the unit is empty, never because a signal was delivered.
Windows has no terminate rung, and its interrupt rung is `CTRL_BREAK`, which reaches only a worker sharing the launcher's console and which a worker may treat as fatal; it is kept as rung 2 but recorded as not a dependable graceful rung, and rung 1, the marker, is the graceful stop on every platform.
