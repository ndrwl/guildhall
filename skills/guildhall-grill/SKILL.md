---
name: guildhall-grill
description: Use as the Director in a grill stage to interview the Guildmaster live, in rounds of numbered questions, until you share an understanding of the product, then record it as the stage's notes.
---

Adapted from `grilling` in mattpocock/skills (MIT, commit 5beb35b); see CREDITS.md.

# Grill

A grill stage makes sure the Artisans build the product the Guildmaster wants.
You are the Director, and you interview the Guildmaster live in your conversation with them until you reach a shared understanding of the task's product requirements.
The decisions are the Guildmaster's: put each one to them and wait for their answer.
Use `guildhall-domain-model` alongside this skill, so the words you agree on are as settled as the decisions.

## The design tree

Map the task as a design tree, in which every decision branches into the decisions that depend on it.
Work the tree in rounds.
The frontier is every decision whose prerequisites are already settled: the questions you can ask now without guessing at answers you have not heard yet.
Ask the whole frontier in one round, number each question and give your recommended answer, then wait for the Guildmaster's answers before the next round.

Word each question so that "yes" accepts your recommended answer.
A question whose answer depends on another question still open in this round belongs to a later round.
Each round of answers reshapes the tree: settled decisions push the frontier outward and unblock the questions that depended on them, so recompute the frontier before the next round.

Finding facts is your job, never the Guildmaster's.
When a frontier question needs a fact from the repository, its documents or its tools, make a quick read-only lookup rather than asking the Guildmaster for anything you could look up.
Reading is not doing the work: you never write to the repository or build anything.
Anything beyond a quick check, such as an investigation or a cross-cutting analysis, belongs to the research stage, where an Artisan does it.
Do not block on it: only the questions downstream of a fact still being found wait for it, so ask the rest of the frontier now.

## A round

```markdown
**Q1. <question title>**
<question body, with the choices when there are several>
Recommended: <your answer>.

**Q2. <question title>**
<question body>
Recommended: <your answer>.
```

## Finishing

The interview is done when the frontier is empty: every branch of the tree visited and nothing silently assumed.
Do not finish the stage until the Guildmaster confirms you have reached a shared understanding.

Then record the agreed result as the stage's notes, so it is stored in the project store and no later stage depends on your conversation.
The notes hold every settled decision with the Guildmaster's answer in their own words where they gave more than "yes", the facts you found with their sources, and the language from `guildhall-domain-model`:

```markdown
## Decisions

- <decision>: <answer>, as the Guildmaster answered <their words, or "yes" to the recommendation>.
- (record) <decision>: <answer>, as the Guildmaster answered <their words>. <context and why, for the repository's decision records>.

## Facts

- <fact>, from <source>.

## Language

**<term>**: <definition>.
Avoid: <rejected words>.
```

The notes are complete when every decision of the tree appears under Decisions with the Guildmaster's answer, so the plan stage can follow them without asking again.

## A resumed grill

When the Guildmaster's changes_requested sends the task back to the grill, from a loop or a design round, the stage's inputs include `feedback`, their request; `previous`, your last notes; and `reviewed`, the document they asked changes to, such as the design.
Start from them rather than starting over: keep every decision in `previous` that still stands, put the request to the Guildmaster in rounds as before, and record the full notes again, the new decisions with the ones that stand.
