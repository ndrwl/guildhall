# Refuse implicit Artisan runtimes

An Artisan stage must select a runtime explicitly through its named profile.
A missing profile is refused at task admission and before an advance mutates task records, naming the stage and directing the user to set a profile on that step in a custom workflow.
There is no default adapter fallback, real or fake.
Explicit `fake` remains supported for demos and deterministic tests; there is no test-process exemption for an absent selection.
Director, Guildmaster and engine stages need no Artisan runtime.
The shipped workflow remains profile-neutral so existing copies can choose their own profile names; the example Director config selects a custom workflow whose Artisan steps name real Claude profiles.
This is pure configuration validation at the existing config/interpreter boundary, not a new process interface.
