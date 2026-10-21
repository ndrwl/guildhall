# Review skill

The release ships `guildhall-review` for automated `review` stages, adapted from upstream `code-review` as the [release skills root](0030-release-skills-root.md) records.
It keeps upstream's two axes, Spec and Standards, never merged or reranked, with the spec line quoted for each spec finding, the standard cited for each standards finding, anything tooling enforces skipped, scope creep reported, and the smell baseline as labelled judgement calls in `smells.md`.

What upstream asks a user for is given by the stage instead.
The head comes from the `head` input and the spec from the `spec` input by its recorded digest, which the renderer already refuses to mismatch, so the branches that ask for a fixed point or a spec are dropped, and an empty range is a `rejected` outcome with a finding rather than a question.
The standards are the ones the prompt inlines, so the Standards axis measures against the exact words and revision the outcome binds to.
On 2026-10-21 the captain approved ("Yes, yes") the default-branch fallback as an interim adaptation until the reviewer is given the recorded base as an input, which remains a separate follow-up.
When the inputs give no base, review the commits on the head that the repository's default branch does not have; a supplied recorded base takes precedence.

The output is the shared [review outcome](../configuration.md#review-outcome) rather than a new report format.
Each finding carries `id`, `rule`, `file` and `line` and a concrete failure, as `docs/review.md` defines a finding.
A smell with no rule behind it is a finding without `rule`, which the contract and `docs/review.md` already make a non-blocking suggestion, so suggestions need no separate list.
The axis is the id's prefix, `spec-`, `std-` or `smell-`, because the outcome has one findings list and no axis field.
The upstream one-line summary becomes the result file's `summary`, since the outcome has no summary field.
On 2026-10-21 the captain approved ("Yes, yes") these contract-compatible adaptations: rule-less suggestions in `findings`, `resolved`/`still-open`/`withdrawn` dispositions, and the result file's `summary`.
Extending the shared contract with a separate suggestions list, different disposition states and an outcome summary remains a separate follow-up.

On a later round every earlier finding is dispositioned before new ones, using the contract's `resolved`, `still-open` and `withdrawn`.
A finding the builder disputed but did not fix stays `still-open`, so the Director sees the stall, and keeping a ruled finding `still-open` contests the ruling as the [Director rulings](0040-director-rulings.md) record defines.

Upstream's parallel sub-agents are dropped: the skill is one reviewer's discipline, and a workflow that wants independent reviewers runs it in several attempts on distinct profiles, as the Guildhall repository's three reviews do.
A test checks the three-file artifact shape and passes the example outcome through `workflow.ParseReviewOutcome`, asserting a blocking round with dispositions and a rule-less suggestion, with no model call.
These checks establish artifact shape and example compatibility, not whether a model follows the review instructions.
Upstream wrote `code-review` for a session with a person, and nobody has yet run the two-axis format as an unattended Artisan outcome.
