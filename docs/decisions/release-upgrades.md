# Release upgrades

Designed, not built: [release upgrades](../releases.md) implement R1.31 with releases installed side by side under `releases/<version>/`, each verified against its sha256 before it can run.
A release pin selects the release for new work; standalone Directors set it with `guildhall release use`, managed Directors take it from `managed.toml` by approved rollout, and either route records a `release-pin` store event, so no upgrade feature needs a Steward.
A managed pin is applied only after `guildhall config reload` authorizes its revision, consistent with the [optional Steward](optional-steward.md) observed-untrusted rule.
A fixed PATH launcher runs the pinned release and never falls back to another one, except for `guildhall release` commands, so a broken pin can always be repaired.
Tasks record their release at creation and every later step runs that release directly, so a pin change reaches new tasks only and rollback is just moving the pin back.
The Steward tells a Director about a new release by command, not message: after `config reload` it runs `guildhall director reload` over SSH, which restarts the Director's agent session at its next step boundary and never mid-step.
This makes `director reload` a second fixed remote command, further narrowing D12 as the optional Steward decision did for `config reload`.
`guildhall release prune` runs after every pin change and keeps the newest three releases by default, never removing the pinned release, the rollback target or any release a running or resumable task recorded; each removal is a store event.
Keeping protected releases beyond the limit was chosen over a strict count because deleting a release a task still needs would strand that task.
