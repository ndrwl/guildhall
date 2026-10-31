---
name: guildhall-spec
description: Use in a plan stage to write the spec the Guildmaster accepts, including problem, solution, contracts, contract tests, acceptance criteria, out of scope and size.
---

Adapted from `to-spec` and `triage/AGENT-BRIEF.md` in mattpocock/skills (MIT, commit 5beb35b); see CREDITS.md.

# Spec

A plan stage turns a task and its notes into the spec that the build stage works from.
Nobody answers you during the stage: the task, the grill notes and the research notes in your inputs are the conversation you synthesise.
Your result file is the spec, and the Guildmaster accepts it or sends it back at the next approval.
The Guildmaster accepts a spec that states its scope, contracts, tests and size.

## Steps

1. Read the task and every input, and treat each recommendation in the grill notes as accepted unless the task says otherwise.
2. Read the repository where the change lands, use its vocabulary throughout, and follow the live decision records for that area, not the superseded ones.
   Propose a new decision record only when the change meets the repository's bar for one, such as the bar in Guildhall's `docs/decisions.md`; a plan, slice order or test budget stays in the spec.
3. Find the contracts the change touches: prefer contracts that already exist, choose the highest one that can show the behaviour, and add a new one only where no existing one can.
   Fewer contracts are better, and the ideal number is one.
   A contract with one implementation is a hypothetical one, so add an interface only where two implementations exist or a test double must replace a real boundary.
4. Write the spec in the order and shape of `template.md`, keeping every section.
5. Check the size: the spec is one coherent pull request, or it lists the work's slices under `## Slices`, one pull request each, delivered in order under this one accepted spec.
   A wide refactor that cannot land in one pull request says so and slices as expand then contract: add the new form beside the old, move callers across, then remove the old form.
   Guildhall parses `## Slices` strictly: a numbered list `1.`, `2.`, … with no gaps, each entry one item that starts with a bold title, then says what the slice delivers and its expected size in lines and files, and no two titles alike.
   Leave the section out for one pull request.
   When the plan runs again after slices have merged, keep every merged slice's entry byte for byte and rewrite only the slices still to come.
6. Write every decision you could not settle from the inputs or the repository as a numbered open question with your recommended answer, and proceed on that recommendation.

## Rules

- Describe behaviour, not procedure: say what the system does when the change is done, and leave how to build it to the build stage.
- Write for durability: name modules, types, function signatures, errors and configuration shapes, and leave out file paths, line numbers and code snippets, which go stale before build reads them.
- Make every acceptance criterion concrete and independently verifiable by a reviewer who reads the spec line by line against the diff.
- State what is out of scope, so build neither gold-plates nor guesses at adjacent work.
- Keep the spec short enough to review: a short list of acceptance criteria, not a list of user stories.

The spec is complete when every section of `template.md` is filled, every contract names its errors, ownership, concurrency, idempotency, identity and recovery or says why one does not apply, every acceptance criterion can be checked on its own, and the size is one pull request or an ordered `## Slices` list.
