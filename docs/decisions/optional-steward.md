# Optional Steward

Designed, not built: a Director runs [standalone or managed](../director-modes.md), and no Director feature may require a Steward.
The mode is a store fact recorded by explicit `enrol` and `leave` commands, not a config key, because the key `mode` already means a copy's role (R1.9) and a store event gives the change a receipt and a place in the timeline.
Copies with a `managed.toml` and no enrolment record keep working as `managed (unrecorded)`, so existing rollouts are unaffected.
Guard rails are enforced by the binary in both modes; standalone takes them from local config, managed lets `managed.toml` win for managed keys, which the Director never writes, and an unexplained managed digest raises an alert rather than being trusted.
Leaving keeps the last managed values as local values, so leaving never loosens a guard rail silently.
The Steward acts by command: rollout copies stay `scp` (D12), followed by an optional `guildhall config reload` over SSH that records the revision; without it the binary records the revision when it first loads it, so every applied revision is a store event either way.
The reload command is a remote command, so adopting it supersedes D12's "no remote command" for that one fixed command.
Lessons are recorded locally in both modes and collected by the Steward on request when managed, so the Director still never reaches the Steward or the private repository (R1.8).
Director suites that load config run on a standalone and a managed copy with the same effective settings, so the rule that no feature needs a Steward is tested rather than assumed.
