# Research accepts Artisan settings

Research is an Artisan stage, so it accepts the same optional `profile` and `skills` settings as plan and build.
Use the registry's shared Artisan settings rather than introducing research-specific validation or launch behavior.
This fixes task-creation rejection of a research profile and lets the existing interpreter select its configured runtime, model and effort.
The shipped default workflow and runtime fallback policy are unchanged.

The workflow and interpreter contracts were updated first, followed by failing regression tests and the registry fix.
The [workflow contract](../contracts/workflow.md#tests) and [interpreter contract](../contracts/interp.md#tests) link the captured red and green evidence.
