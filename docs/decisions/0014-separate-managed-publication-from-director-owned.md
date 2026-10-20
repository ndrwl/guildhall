# D14. Separate managed publication from Director-owned config

Rollouts write only `managed.toml`, never `config.toml`, so concurrent local edits cannot be overwritten by a stale whole-file snapshot.
See [configuration](../configuration.md) for combined loading and legacy inline-layout compatibility.
A checksum framing line detects incomplete managed transfers, but scp publication is non-atomic and may transiently refuse reads; this does not establish uninterrupted config availability.
An exclusive lock file in the private config directory prevents concurrent local applies sharing that directory, not cross-host writers.
Operators must designate one writer and recover a stale lock only after confirming its owner has stopped.
See [Steward revisions and active tasks](../steward.md#revisions-and-active-tasks) for revision retention and task binding.
