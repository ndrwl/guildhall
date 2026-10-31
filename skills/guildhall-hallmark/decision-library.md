# Decision-library step

Run this step when the repository keeps decision records, such as Guildhall's `docs/decisions/` with its generated index `docs/decisions.md`.
Its checks catch what a test cannot: whether a record still says something true and still earns its place.
It changes nothing: it reports ranked follow-ups, and the Guildmaster picks which become tasks.

## What to report

Read the index and every live record, then list each of these with its evidence:

1. Chains to compact: three or more records about one decision linked by supersedes or amends, or a superseded or amended record that has stood longer than one hallmark cycle.
   Name the records and the current record the chain should become.
2. Records whose mechanism is gone from the code: grep the names, settings, commands and types a record cites, and list the ones that no longer exist.
3. Records that are not decisions: a plan, slice order or test budget, a review or report, or behaviour reference that belongs on a contract or topic page.
   Name where the content should go.
4. Records over the size aim the repository sets, such as Guildhall's 40 lines, usually because they copy contract detail.
5. Supersessions nobody marked: a later record that changes an earlier one whose header names neither the other.
   The tests check the pairs the headers name, so only reading finds these.
6. Bare citations of retired numbers, such as Guildhall's old `D60`, in docs and code comments, which should be links to the record.
   The quality rubric's D1 to D5 and task ids such as `d87` are not record citations.

## How to rank

Rank the follow-ups by how often Artisans hit the topic, judged from the recent history and the modules the next slices touch, because a stale record misleads every plan that reads it.
Write each follow-up as one task the Guildmaster can pick, with the records it touches and the expected end state.
Never compact, move or rewrite a record yourself, and never open a task for a follow-up.
