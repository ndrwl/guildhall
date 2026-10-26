# Refuse implicit Artisan runtimes

An Artisan stage must select a runtime explicitly, through its named profile or the effective `default_adapter` managed/local setting.
A caller embedding the interpreter may instead supply `Engine.DefaultAdapter` explicitly.
An absent selection is refused at task admission and before an advance mutates task records, naming the stage and both configuration remedies.
Explicit `fake` remains supported for demos and deterministic tests; there is no test-process exemption for an absent selection.
Director, Guildmaster and engine stages need no Artisan runtime.
The shipped workflow remains profile-neutral so existing copies can choose their own profile names; example Director configs explicitly select Claude as their default adapter.
This is pure configuration validation at the existing config/interpreter boundary, not a new process interface.
