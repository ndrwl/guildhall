# Artisan child workspaces and Director chat address

## Decision

Keep one Herdr workspace per workflow Artisan, with a visual child label `└ <task> · <step> #<attempt>`.
Keep the existing detailed tab label and owned launcher, log, task record and lifecycle unchanged.
Use the Director's exact stored pane binding, verified live, as the parent rather than guessing from labels or focus.
Under an account-private named-session lock, create and journal each workspace and make one optional protocol-16 `workspace.move` request to append it to the Director's contiguous child block.
Verify every returned workspace id and label in order, including foreign workspaces.
Only the newly created workspace moves; earlier children, the Director and foreign workspaces never move, rename or close.

Persist exact created-workspace bindings in a presentation-only journal under the copy's state directory.
The journal is not endpoint or task ownership authority and grants no cleanup or recovery action.
Closed siblings disappear from the visual block; present siblings must retain their recorded labels and launch order immediately after their Director.
Missing capability, unavailable lock, ambiguous identity, changed layout and failed verification warn and skip ordering without failing launch or changing task records.
Do not retry or roll back a move whose outcome is uncertain.

The baseline Director role addresses the user as "Guildmaster" in every chat reply, including bad news, but never adds that address to commits, PR text, records, documents or messages.
This is a role instruction, not a text-rewriting filter or a claim that deterministic CI proves model compliance.

## Evidence

The new label and ordering contracts were exercised red before implementation against the existing launcher and a refused ordering stub.
See the [Herdr contract](../contracts/herdr.md) for captured red-first evidence and the live ordering check.
Journal and warning/lock tests were added after implementation and passed on their first run; they are regression coverage, not claimed as test-first history.
The live ordering check uses the guarded named-session helper and no model calls.
The broader pre-existing workflow lab initially hit an idle-shell starting-tab cleanup refusal before launch; the targeted lab isolates ordering from that unrelated shell/prompt behavior.
