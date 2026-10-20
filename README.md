# guildhall
Agent-agnostic local engineering workflows, shared tooling and fleet setup.

Guildhall is at its foundation stage: it builds and runs, but no platform or agent runtime is supported for real work yet.
The specification is in `docs/requirements.md`.

## Build

Guildhall needs Go 1.27 or later.

```sh
go build -o guildhall ./cmd/guildhall
```

The release workflow attaches binaries for Linux, Windows and macOS, with a `SHA256SUMS` file, to each tagged GitHub release; none has been published yet.

## Commands

- `guildhall version` prints the Guildhall version, platform and Go version.
- `guildhall doctor` prints the platform and, for `git`, `ssh`, `claude` and `herdr`, the version found on `PATH` or why it could not be read.
  A missing tool is reported, not treated as an error.
- `guildhall help` lists the commands.

## Documentation

- `docs/requirements.md`: the product requirements.
- `docs/review.md`: the review standard every pull request is held to.
- `docs/decisions.md`: engineering decisions.
- `CONTRIBUTING.md`: how to contribute.
