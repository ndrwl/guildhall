# Immutable attempt preparation

An attempt's prepared prompt, input copies, skill copies and manifest are published once and never replaced, because a missing `launched.json` does not prove that no worker is reading them.
`Launch` can be accepted while its acknowledgement is lost, or the engine can stop after `Launch` returns and before it writes the mark; in both cases a live worker may already hold the prepared files when the advance is repeated.
The interpreter used to remove `prompt/` and render it again from current sources before the launcher could refuse the live duplicate, so a changed role, guide, standard or skill rewrote a live worker's files and the later provenance described bytes the worker never read.

The engine renders a fresh attempt into `<step>-<n>/prompt.partial`, writes the manifest beside the prompt as `manifest.json`, and publishes the preparation by renaming the whole directory to `<step>-<n>/prompt` in one step.
A directory rename is atomic on every supported platform, so `prompt/` either does not exist or holds a complete preparation with its manifest; no launch is ever given a path inside `prompt.partial`, so a leftover one is discarded and rendered again.
A replay that finds `prompt/` validates it against its manifest and the attempt's recorded binding and launches from it unchanged; it never renders again.
Preparation that fails validation is refused with `interp.ErrPreparation`, launching nothing and removing nothing, because the files are evidence of what a worker may have read and only the Director can decide what happens to them.
Changed preparation needs a new attempt identity, which is a new attempt number.

A check that the owner is not alive before rendering again was rejected: it races with the launcher it asks, and identical re-rendered bytes still replace files a live worker has open.
A manifest kept outside `prompt/` was rejected because publishing it and the directory would be two steps with a crash window between them.
Validation stays private to the engine rather than a new filesystem interface: the fault cases are reproduced with real temporary files, so no substitutable boundary is needed.
