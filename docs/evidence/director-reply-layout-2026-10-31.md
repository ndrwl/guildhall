# Director reply layout and Lanterns section drawing (d101)

The Lanterns tests for the layout split and the section drawing were written in build-1, before `layout`, `routineEdge` and the new `AssistantMessage` drawing existed, but that sandbox could not run `node` or `claude`.
Build-4 ran both, on the branch rebased onto main.

## Red: the real engine refused build-1's drawing

`claude plugin test internal/lanterns/plugin` on build-1's head, with zero model calls:

```text
tests/engine.test.ts:
(fail) real engine defaults Calm on, preserves assistant content and paints S2 [92.25ms]
  AssertionError: expect(received).toBe()

  Expected: "Box"
  Received: "Text"

  the engine reported:
    ui.render (AssistantMessage): a hook returned a tree that does not validate (Box prop "borderTop" is not allowed)
    lanterns's ui.render hook was skipped: lanterns: returned a tree AssistantMessage refuses: Box prop "borderTop" is not allowed

 0 pass
 1 fail
```

The engine accepted several per-section `next` calls; what it refused was the single-sided `Box` borders used for the rules and for the Routine box's open top.
On that refusal the engine skipped the hook and drew the reply natively, so nothing broke, but no frame or rules were drawn.
The node tests passed on build-1's head (16 of 16), because their fake engine does not validate props.
No red run of the node tests before build-1's code exists; this attempt was not allowed to put the old code back to show one.

## Fix

The rules are now `Text` lines of `─` from the pure `rule`, and the pure `routineBox` draws the Routine box whole in text: the `routineEdge` top edge, each line between `│` sides wrapped to fit, and a bottom edge.
The marker line, Needs you and What I did are still each drawn by calling `next` with their text.
The engine test now also checks that those three sections hold the engine's native drawing.

## Green

`node --test internal/lanterns/plugin/tests/*.test.mjs`:

```text
ℹ tests 17
ℹ pass 17
ℹ fail 0
```

`claude plugin test internal/lanterns/plugin`:

```text
tests/engine.test.ts:
(pass) real engine defaults Calm on, preserves assistant content and paints S2

 1 pass
 0 fail
Ran 1 test across 1 file.
```

`go test ./...` passes over the whole module, including `TestContentIsPlainMarkdown`, `TestOneSentencePerLine`, `TestDigestsMatchGolden`, `TestTreeDigestsMatchGolden` and `TestDirectorSkillsNameEveryReadmeCommand`.
