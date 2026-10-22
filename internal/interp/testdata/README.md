# Interpreter fixtures

Shared by the scripted mock agent's tests, the interpreter's advance suite and the [adapter conformance suite](../../../docs/adapters.md#conformance).

- `baseline/`, `docs/` and `skills/` are the sources `interptest.Attempt.Render` renders prompts from.
- `docs/spec-v1.md` and `docs/spec-v2.md` are two revisions of one artefact.
- `manifests/` holds the prompt manifests those renders must produce.
- `results/` holds the result files the mock agent writes, and `reviews/` the review outcomes it can return.
- `workflows/straight.json` is the straight workflow the interpreter's advance suite runs; `workflows/loop.json` adds one bounded loop on each side of acceptance, and `workflows/three-reviews.json` is the Guildhall example of report §7.1 with three code reviews on distinct profiles, without publish and validate.
- `workflows/director-grill.json` puts a Director grill stage before research and the straight path, with both notes as the plan's context, and `baseline/roles/director.md` is the role its stage is rendered with.
