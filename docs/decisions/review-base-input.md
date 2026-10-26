# Expose the recorded review base in the prompt

Expose the review base through the existing immutable binding rather than introducing a workflow port or a second document containing the same commit.
This replaces [D47's interim default-branch fallback](0047-review-skill.md) without changing review outcome schemas.
The [interpreter contract](../interpreter.md#rounds-and-loops) owns base selection and preparation, the [rendering contract](../rendering.md#artisan-prompts) owns the emitted field, and the [review skill](../../skills/guildhall-review/SKILL.md#what-you-review) owns range selection and missing-base handling.
