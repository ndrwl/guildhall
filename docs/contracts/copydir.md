# `internal/copydir`

## Contract

The package is copy discovery and the copy marker of [copies](../running.md#copies), moved out of `internal/cli` in d127 slice 2 so the CLI and the [PATH launcher](launcher.md) share one implementation and cannot drift.
The CLI keeps its short names (`discoverCopy`, `readCopyMarker`, `samePath`, `within`) as aliases of these; behaviour is unchanged.

- `MarkerFile` is `.guildhall-copy`, holding one `Marker`, schema `guildhall.copy` version 1, and the copy's name.
- `Discover(getwd)` searches upward from the working directory for the nearest directory holding a marker; a start under that copy's `state/` finds no copy, as does a nil `getwd`, and a marker that cannot be read is an error naming it.
- `ReadMarker(path)` refuses an unreadable marker, one that is not JSON and one with another schema or version; `IsCopy(dir)` is true when `dir` holds a readable marker.
- `SamePath` and `Within` compare absolute, cleaned paths, ignoring case on Windows.

## Tests

The package's behaviour is pinned by `internal/cli`'s copy tests, which run through the aliases (`copy_test.go`, `copy_windows_test.go`), and by `TestCopySelectionIsShared`, one table run through the CLI's selection and the launcher's.
