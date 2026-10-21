# Immutable attempt preparation

An attempt's prepared prompt, input copies, skill copies and manifest are published once and never replaced, because a missing `launched.json` does not prove that no worker is reading them.
`Launch` can be accepted while its acknowledgement is lost, or the engine can stop after `Launch` returns and before it writes the mark; in both cases a live worker may already hold the prepared files when the advance is repeated.
The interpreter used to remove `prompt/` and render it again from current sources before the launcher could refuse the live duplicate, so a changed role, guide, standard or skill rewrote a live worker's files and the later provenance described bytes the worker never read.

The [attempt preparation contract](../interpreter.md#attempt-preparation) owns the publication, replay validation and refusal rules.
Invalid preparation is retained because its files are evidence of what a worker may have read and only the Director can decide what happens to them.

A check that the owner is not alive before rendering again was rejected: it races with the launcher it asks, and identical re-rendered bytes still replace files a live worker has open.
A manifest kept outside `prompt/` was rejected because publishing it and the directory would be two steps with a crash window between them.
Validation stays private to the engine rather than a new filesystem interface: the fault cases are reproduced with real temporary files, so no substitutable boundary is needed.
