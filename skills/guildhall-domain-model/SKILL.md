---
name: guildhall-domain-model
description: Use with guildhall-grill to sharpen the language of a task while you interview the Guildmaster, and record the terms and hard-to-reverse decisions you agree on in the grill notes.
---

Adapted from `domain-modeling` in mattpocock/skills (MIT, commit 5beb35b); see CREDITS.md.

# Domain model

While you grill the Guildmaster with `guildhall-grill`, build and sharpen the product's domain model as the interview goes.
This is the active discipline: challenging terms, inventing edge-case scenarios and writing the agreed language down as it settles.

## Where the model lives

The grill stage's notes are the model's home: a Language section of terms and the hard-to-reverse choices among the Decisions.
You only orchestrate, so you never edit the repository yourself.
When the repository already keeps a glossary or decision records, read them first and challenge against them; a term or decision that should be added there goes into the notes as something for the plan to carry into the repository.

## During the interview

### Challenge against existing language

When the Guildmaster uses a term that conflicts with the language in the repository's glossary, its documents or its code, say so at once, for example: "The glossary defines cancellation as X, but you seem to mean Y; which is it?"

### Sharpen fuzzy language

When the Guildmaster uses a vague or overloaded term, propose one precise canonical term, for example: "By account, do you mean the customer or the user, which are different things?"

### Discuss concrete scenarios

When relationships between concepts come up, stress-test them with specific scenarios that probe the edge cases and make the boundaries between concepts precise.

### Cross-reference with the code

When the Guildmaster says how something works, check whether the code agrees, and surface any contradiction as a question in the next round.

## Recording terms

When a term is resolved, add it to the Language section of your draft notes at once rather than batching terms up.
Use this format:

```markdown
**Order**: A customer's request for goods, from placing to delivery.
Avoid: purchase, transaction.
```

Be opinionated: when several words exist for one concept, pick the best one and list the others under Avoid.
Keep each definition to one or two sentences that say what the term is, not what it does.
Include only terms specific to this product, not general programming concepts.
The Language section holds no implementation details: it is a glossary, not a spec.

## Recording decisions

Mark a decision as a record for the repository only when all three are true:

1. It is hard to reverse: changing your mind later has a meaningful cost.
2. It is surprising without context: a future reader will wonder why it was done this way.
3. It is the result of a real trade-off: there were genuine alternatives, and one was picked for specific reasons.

When any of the three is missing, the decision stays an ordinary entry under Decisions.
A decision record is one to three sentences: the context, what was decided and why.
