# guildhall
Agent-agnostic local engineering workflows, shared tooling and fleet setup.

Guildhall is at its foundation stage: it builds and runs, but no platform or agent runtime is supported for real work yet.
The specification is in `docs/requirements.md`.

The current foundation provides configuration and workflow parsing and validation, not a runnable Director or workflow executor.
See [configuration and workflows](docs/configuration.md) for the format, examples and library usage.

## Build

Guildhall needs Go 1.27 or later.

```sh
go build -o guildhall ./cmd/guildhall
```

The release workflow publishes binaries for Linux, Windows and macOS, with a `SHA256SUMS` file, only for pushed version tags; manual runs produce downloadable workflow artifacts without publishing a release.
No release has been published yet.

## Commands

- `guildhall version` prints the Guildhall version, platform and Go version.
- `guildhall doctor` prints the OS, architecture, Guildhall version and build-time Go version, then, for `git`, `ssh`, `claude` and `herdr`, the first non-blank version-output line and resolved path or why the version could not be read.
  A missing tool is reported, not treated as an error.
- `guildhall help` lists the commands.

## Documentation

- `docs/requirements.md`: the product requirements.
- `docs/review.md`: the review standard every pull request is held to.
- `docs/decisions.md`: engineering decisions.
- `CONTRIBUTING.md`: how to contribute.
