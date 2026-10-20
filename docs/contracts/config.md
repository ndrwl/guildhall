# `internal/config`

## Contract

`Parse` refuses unknown keys, newer schemas and credentials, applies defaults and validates (`docs/configuration.md`).
Repository paths follow the owner-machine contract (D11): POSIX-rooted and rooted Windows drive-letter paths are accepted on every host; relative, drive-relative and backslash-prefixed UNC paths are refused.
Duplicate comparison normalizes dot segments and, for drive paths, separators and drive-letter case independently of the parsing host.
`EncodeManaged` and `ParseCombined` frame and verify the separate managed file and refuse inline managed settings beside it (D14).
`Load` follows the filesystem fallback and error rules in [configuration](../configuration.md).

## Tests

`config_test.go`, `managed_test.go`; real temporary files: `load_test.go`
