# D8. TOML parsing for copy configuration

Copy configuration uses TOML (R1.11), which the Go standard library does not parse.
Add `github.com/BurntSushi/toml` v1.6.0 for `internal/config`, following D3's policy of adding a dependency when a feature needs it.
Its typed decoding and undecoded-key metadata let the configuration parser populate concrete Go types and reject unknown keys without maintaining a custom TOML parser.
Workflow JSON continues to use the standard library's `encoding/json`; this dependency adds no execution or activation behavior.
