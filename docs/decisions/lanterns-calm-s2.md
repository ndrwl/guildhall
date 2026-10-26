# Adopt S2 Rare forge spark and default Calm on

The selected animation is S2, preserving the smithy silhouette with two-tick smoke poses, the existing hearth beat and one three-tick spark per sixteen ticks.
Its eleven-cell origin centers the smithy and anvil together; narrower Rasters clip individual cells at origin zero instead of wrapping.
Per-cell colouring keeps the anvil ambient and only the hearth and spark orange, replacing the previous row-wide hearth colouring.
The existing pure geometry/encoding functions and function-hook adapter remain the focused integration seam; no new platform interface or status authority is needed.

With function hooks enabled and a Director copy present, an absent preference now means Calm on.
The existing command persists both choices before changing the live presentation, so an explicit off survives sessions and failed writes leave the current state unchanged.
No-copy sessions and sessions without the exact function-hook flag retain native drawings and perform no Calm preference operations.
Invalid or unreadable existing preferences continue to fail closed to native drawings.

New fake-engine and pure-renderer contract tests were run before implementation and failed for the new frame and default-on requirements.
Existing lifecycle tests are retained; the real zero-model-call engine test now checks default-on and persisted-off restoration.
