# guildhall
Agent-agnostic local engineering workflows, shared tooling and fleet setup.

Guildhall is at its foundation stage: it builds and runs, but no platform or agent runtime is supported for real work yet.
The specification is in `docs/requirements.md`.

The current foundation provides configuration and workflow parsing and validation, the durable record store and a local message inbox on it, and the Steward's managed-settings rollouts, not a runnable Director or workflow executor.
See [configuration and workflows](docs/configuration.md) for the format, examples and library usage, and [Steward rollouts](docs/steward.md) for rollouts.

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
- `guildhall store doctor <path>` checks a store file read-only and prints its schema version and record counts; see [the store](docs/store.md).
- `guildhall steward rollout --config <steward.toml> --machine <label> --plan <dir> --dry-run` previews a rollout of the private managed settings to the enrolled Director copies on that machine and writes the plan; `--apply` carries out that plan over `scp` and writes a receipt.
  See [Steward rollouts](docs/steward.md).
- `guildhall messages send|list|read|ack --store <path> ...` sends, lists, reads and acknowledges local Director and Artisan messages kept in a store; see [messaging](docs/messaging.md).
  It does not notify running agents or reach other machines.
- `guildhall help` lists the commands.

## Documentation

- `docs/requirements.md`: the product requirements.
- `docs/review.md`: the review standard every pull request is held to.
- `docs/decisions.md`: engineering decisions.
- `docs/contracts.md`: how contracts, interfaces, tests and packages are organised.
- `docs/store.md`: the store's records, mutation receipts and locking.
- `docs/steward.md`: Steward rollouts.
- `docs/messaging.md`: the local Director and Artisan message inbox.
- `CONTRIBUTING.md`: how to contribute.
