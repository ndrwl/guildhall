# `internal/config`

## Contract

`Parse` refuses unknown keys, newer schemas and credentials, applies defaults and validates (`docs/configuration.md`).
Repository paths follow the owner-machine contract (D11): POSIX-rooted and rooted Windows drive-letter paths are accepted on every host; relative, drive-relative and backslash-prefixed UNC paths are refused.
Duplicate comparison normalizes dot segments and, for drive paths, separators and drive-letter case independently of the parsing host.
`EncodeManaged` and `ParseCombined` frame and verify the separate managed file and refuse inline managed settings beside it (D14).
`Load` follows the filesystem fallback and error rules in [configuration](../configuration.md).

`Profile` and `CheckProfiles` refuse an unknown profile name (`ErrUnknownProfile`).
`StallInterval` is the effective `stall_interval` [setting](../configuration.md#settings) as a duration, `DefaultStallInterval` (15 minutes) when unset and zero for `0s`; `Parse` refuses a managed or local value that is not a nonnegative Go duration, and `StallInterval` returns that error for a config built without `Parse`.
`ChecksRefresh` is the effective `checks_refresh` setting the same way, `DefaultChecksRefresh` (1 minute) when unset and zero for `0s`.
A repository's `NoCI` is its `no_ci` setting, `false` by default.

## Tests

`config_test.go`, `managed_test.go`, `profile_test.go`; real temporary files: `load_test.go`.
`stall_test.go` was written red first against a stub `StallInterval` ([red run](../evidence/stall-adapter-choice-2026-10-22-red.txt)): the default, local, zero, managed and overridden values, and refusal of values that are not nonnegative durations.
`checks_refresh_test.go` pins the same cases for `ChecksRefresh`, and `TestDirectorConfig` the `no_ci` default and value; they were added with the implementation and were not run red.
