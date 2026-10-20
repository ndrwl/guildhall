# D21. Fake adapter as a subcommand

The fake Artisan is `guildhall fake-artisan`, part of the same binary, so CI and lab runs need nothing else installed.
Its test behaviours are selected by `guildhall-fake:` lines in the prompt file, bounded to the outcomes and faults listed in `docs/adapters.md`.
