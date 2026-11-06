---
name: guildhall-grill
description: Use as the Director in a grill stage to interview the Guildmaster, in rounds of numbered questions they answer on the task page, until you share an understanding of the product, then record it as the stage's notes.
---

Adapted from `grilling` in mattpocock/skills (MIT, commit 5beb35b); see CREDITS.md.

# Grill

A grill stage makes sure the Artisans build the product the Guildmaster wants.
You are the Director, and you interview the Guildmaster, in rounds they answer on the task page, until you reach a shared understanding of the task's product requirements.
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

Each question is a `**Q<n>. <title>**` line, its body, which may be empty, and a last line `Recommended: <your answer>`, numbered from Q1 with nothing before it.

## Rounds on the task page

While the task pages are served, every round goes on the page, under the grill's running attempt:

```sh
guildhall grill round open --repo app --task t-12 --attempt grill-1 --questions-file - < notes/app-t-12-grill-1-round-1.md
```

The page asks the round as one form: for each question the Guildmaster accepts your recommendation or answers in their own words, can save and come back, and sends the whole round once every question has an answer.
In chat, the round is only its Needs you line, `t-12 · grill round 1 · grill-1`, ending with the link to the attempt on its page, such as `ticket:app/t-12#grill-1`; never write the round's questions in chat too.
A new round opens only once the Guildmaster has sent the one before it, and not while your closing summary waits for their answer.

Sending the round wakes you with a `grill-answered` message, which names the command that prints the answers:

```sh
guildhall grill round show --repo app --task t-12 --attempt grill-1
```

Each answer is `Accepted: <your recommendation>`, sometimes with words after it, or `Your words:` and their words.
Read them as answers given in the interview, recompute the frontier, and open the next round or, when the frontier is empty, put your closing summary on the page as Finishing says.
The page never finishes the stage: you still do, as Finishing says.
Anything they tell you in the stage's talk or in chat counts as well; reply on the page with `guildhall task say` under the attempt.

With no task pages served, ask each round in chat as before and wait for their answers there.

## Finishing

The interview is done when the frontier is empty: every branch of the tree visited and nothing silently assumed.
Do not finish the stage until the Guildmaster confirms you have reached a shared understanding.

Ask for that confirmation with a closing summary: the decisions you will record, in short Markdown, one line each with the answer, not the notes themselves.
While the task pages are served, put it on the page under the grill's running attempt, never as a `guildhall task say` comment, which the page shows as talk rather than as something waiting on them:

```sh
guildhall grill summary open --repo app --task t-12 --attempt grill-1 --summary-file - < notes/app-t-12-grill-1-summary.md
```

The page shows it as a **Grill summary** card with **Confirm** and **Request changes**, and the Guildmaster's words with either.
In chat it is only its Needs you line, `t-12 · grill summary · grill-1`, ending with the link to the attempt on its page, such as `ticket:app/t-12#grill-1`; never write the summary in chat too.
Then wait: their answer wakes you with a `grill-answered` message, which names the command that prints the summary, its outcome and their words:

```sh
guildhall grill summary show --repo app --task t-12 --attempt grill-1
```

- **Confirmed**: write the notes from the summary, adding any words they gave with Confirm as their own, and finish the stage.
- **Changes requested**: read their words as answers in the interview, recompute the frontier, and open a round on what is still open, or a revised summary when their words settle it.

With no task pages served, put the summary in chat, ask them to confirm it there, and once they do record it on the task with `guildhall task say --author guildmaster-chat` under the attempt, quoting their confirmation, before you finish.

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
A resumed or reopened grill is a new attempt, such as `grill-2`, so its rounds open under that attempt, numbered from round 1 again.
