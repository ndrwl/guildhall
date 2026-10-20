---
name: guildhall-grill-notes
description: Use in a grill stage to write its notes as one numbered round of open decisions, each with the recommended answer you proceed on.
---

Adapted from `grilling` in mattpocock/skills (MIT, commit 5beb35b); see CREDITS.md.

# Grill notes

A grill stage stress-tests a task before anyone plans it.
Nobody answers you during the stage: you write the questions, recommend an answer to each, and proceed on your recommendations.
The Director shows your notes to the Guildmaster at the next approval, and a "no" to any question sends the plan back with that answer.

## Steps

1. Read the task and its inputs, and map the decisions it needs as a tree in which each decision branches into the decisions that depend on it.
2. Find facts yourself: read the repository, its documents and its tools rather than asking for anything you can look up, and note the source of each fact.
3. Settle every decision the facts settle, and record it with its evidence.
4. Write every decision still open as one numbered question with your recommended answer, in the format below.
   Word each question so that "yes" accepts your recommendation.
   Ask only what can be answered now, and put a question that depends on another after it, naming the question it depends on and assuming that question's recommendation is accepted.
5. Finish when every branch of the tree is either settled with evidence or written as a question, so nothing is silently assumed.

## Format

```markdown
## Settled

- <decision>: <answer>, because <evidence and its source>.

## Questions

**Q1. <question title>**
<question body, with the choices when there are several>
Recommended: <your answer>.

**Q2. <question title>** (depends on Q1)
<question body>
Recommended: <your answer>.
```

The notes are complete when they hold a Settled list and a numbered Questions list, every question carries a recommendation, and the rest of your work follows those recommendations.
