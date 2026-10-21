# Optional Steward

Designed, not built: a Director runs [standalone or managed](../director-modes.md), and no Director feature may require a Steward.
The mode is a store fact recorded by explicit `enrol` and `leave` commands, not a config key, because the key `mode` already means a copy's role (R1.9) and a store event gives the change a receipt and a place in the timeline.
Guildhall is pre-release: legacy inline `[managed]` settings are refused with guidance to move them to checksum-bearing `managed.toml`; a copy with that file but no enrolment record must explicitly enrol, with no `managed (unrecorded)` compatibility state.
Guard rails are enforced by the binary in both modes; standalone takes them from local config, managed lets `managed.toml` win for managed keys, which the Director never writes, and an unexplained managed digest raises an alert rather than being trusted.
Leaving keeps the last managed values as local values, so leaving never loosens a guard rail silently.
The Steward acts by command: rollout copies stay `scp` (D12), followed by `guildhall config reload` over SSH to validate and record authorization for the rollout digest in the receiving store.
At the shared revision-admission boundary, a digest with no receiving-store rollout, reload or explicit Guildmaster approval authorization is recorded as `observed-untrusted`, raises a system alert and is not applied.
Only a recorded reload or explicit Guildmaster approval of that exact digest authorizes an observed-untrusted revision; a checksum, enrolment, or a receipt held only on the Steward does not.
Active tasks retain their bound configuration, and commands needing effective settings refuse an untrusted on-disk revision.
The reload command is a remote command, so adopting it supersedes D12's "no remote command" for that one fixed command.
Lessons are recorded locally in both modes and collected by the Steward on request when managed, so the Director still never reaches the Steward or the private repository (R1.8).
Director suites that load config run on a standalone and a managed copy with the same effective settings, so the rule that no feature needs a Steward is tested rather than assumed.
