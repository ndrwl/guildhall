# Launcher fixtures

Shared by the scripted mock agent's tests and, later, the interpreter and adapter conformance slices.

- `baseline/`, `docs/` and `skills/` are the sources `interptest.Attempt.Render` renders prompts from.
- `docs/spec-v1.md` and `docs/spec-v2.md` are two revisions of one artefact.
- `manifests/` holds the prompt manifests those renders must produce.
- `results/` holds the result files the mock agent writes, and `reviews/` the review outcomes it can return.
