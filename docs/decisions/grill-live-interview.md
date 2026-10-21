# The grill is the Director's live interview of the Guildmaster

A grill stage exists so the Artisans build the product the Guildmaster wants.
With the default `director` actor it is a live interview in the Director's conversation with the Guildmaster: rounds of numbered questions over a design tree, each with a recommended answer worded so "yes" accepts it, until both share an understanding.
The agreed result, every settled decision with the Guildmaster's answer, is recorded as the stage's notes, so it is stored durably and no later stage depends on the conversation (R1.13).
The release skills are `guildhall-grill`, adapted from `grilling` in mattpocock/skills, and `guildhall-domain-model`, adapted from `domain-modeling`, the pair that `grill-with-docs` invokes upstream.

The shipped `guildhall-grill-notes` inverted this: it told the stage that nobody answers it and to proceed on its own recommendations.
It is renamed `guildhall-grill-unattended` and kept only as the opt-in for a repository meant to run unattended, whose grill actor is overridden to `artisan`; the Guildmaster then answers its recommendations at acceptance instead of in conversation.

Guildhall has no home for a managed product's glossary or decision records: the project store keeps task documents, and `docs/glossary.md` is Guildhall's own.
`guildhall-domain-model` therefore records agreed terms and hard-to-reverse decisions in the grill notes, and the Director, which only orchestrates (R1.5), never writes them into the repository; a repository's existing glossary is read and challenged against, and additions to it are left to the plan.
No glossary subsystem is added.

The engine does not run Director stages yet, and the default workflow's grill step lists no skills.
Handing the grill stage to the Director, with these two skills, comes with the Director slice; until then an `artisan` override is the only grill the engine runs, and a workflow that wants the unattended skill lists it in the grill step's `skills`, because `workflow_overrides` changes only the actor.
