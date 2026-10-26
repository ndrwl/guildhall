# Generate required output contracts with every Artisan prompt

A review can approve in prose yet fail intake because its succeeded result lists no file for the required `outcome` port.
The old review guide asked only for a verdict, while the Claude result example actively showed an empty output list.
Code-head capture masks this omission for builds because Guildhall derives Git HEAD; review outcomes cannot be derived from a summary.

Generate each Artisan kind's required port names, file paths, formats, minimal valid examples and matching result entries in `render.Prompt`, independently of replaceable stage-guide prose.
The renderer owns the complete result template; Claude supplies the result destination and fixed identity, and directs the Artisan to that template instead of duplicating an empty-output example.
The fake adapter does not render separate result instructions and receives the same generated prompt.
Review examples use the interpreter's recorded revision binding when available, not revision values guessed from the worktree or a moving configuration.
Standalone rendering retains explicitly labelled illustrative revisions.
Shared stage guides describe what to produce, leaving Artisan completion mechanics to the actor-gated [generated instructions](../rendering.md#required-outputs-and-result-template).

The [interpreter contract tests](../contracts/interp.md#tests) own the zero-model output-example checks and their feedback command.
Existing prompt and baseline digest goldens are intentionally updated.
This prevents omission in future instructions without weakening intake, inventing a review verdict, rewriting published attempts or recovering a live review automatically.
Engine-only delivery and validation do not gain Artisan prompts because they do not run Artisans.
