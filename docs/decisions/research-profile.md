# Research accepts a profile

Research is an Artisan stage and accepts optional `profile` only; `skills` remains refused.
Reuse the registry's existing Artisan `profile` definition rather than introducing research-specific validation or launch behavior.
This profile-only decision supersedes the original broader shared-settings scope.
This fixes task-creation rejection of a research profile and lets the existing interpreter select its configured runtime, model and effort.
The shipped default workflow and runtime fallback policy are unchanged.

The workflow and interpreter contracts were updated first, followed by failing regression tests and the registry fix.
The [workflow contract](../contracts/workflow.md#tests) and [interpreter contract](../contracts/interp.md#tests) link the captured red and green evidence from the original implementation; the workflow contract also describes the profile-only regression coverage.
