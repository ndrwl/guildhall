# D29. Skills on grill steps

Version 1 of the `grill` step accepts the optional `skills` setting so `guildhall-grill-notes` can be used in its intended stage.
The setting uses the existing shared list-of-distinct-valid-names rule used by other skill-bearing stages.
Both full-workflow validation and isolated render-request validation use this registry declaration.

Actor rules are unchanged: `actor` is required and must be `director` or `artisan`.
The setting is available with either allowed actor.
This change does not add `profile` or `instructions` to grill steps, and undeclared settings remain errors.

The built-binary rendering test exercises an Artisan grill step with the release skill, including first-root precedence and preservation of its attribution.
The skills installation test validates its grill step before rendering.
See the [workflow contract](../contracts/workflow.md) and [release skills decision](0030-release-skills-root.md).
