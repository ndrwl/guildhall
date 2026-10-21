# Smell baseline

These Fowler smells (_Refactoring_, chapter 3) apply even where a repository documents nothing.
Each is a heuristic, reported as a suggestion with no `rule` and named as possible ("possible Middle Man").
A written standard always wins: suppress a smell it endorses, and cite the rule instead where it forbids the pattern.
Skip anything tooling already enforces.

Each entry reads what it is, then how to fix it.

- **Mysterious Name**: a function, variable or type whose name does not reveal what it does or holds; rename it, and if no honest name comes, the design is murky.
- **Duplicated Code**: the same logic shape in more than one hunk or file of the change; extract the shared shape and call it from both.
- **Feature Envy**: a function that reaches into another type's data more than its own; move it onto the data it envies.
- **Data Clumps**: the same few fields or parameters keep travelling together; bundle them into one type and pass that.
- **Primitive Obsession**: a string or number standing in for a domain concept; give the concept its own small type.
- **Repeated Switches**: the same `switch` or `if` cascade on the same value recurs across the change; replace it with one table or one method both sites share.
- **Shotgun Surgery**: one logical change forces scattered edits across many files; gather what changes together into one module.
- **Divergent Change**: one file or module is edited for several unrelated reasons; split it so each part changes for one reason.
- **Speculative Generality**: abstractions, parameters or hooks for needs the spec does not have; delete them until a real need shows.
- **Message Chains**: long `a.B().C().D()` navigation the caller should not depend on; hide the walk behind one method on the first value.
- **Middle Man**: a type or function that mostly delegates onward; cut it and call the real target directly.
- **Refused Bequest**: an embedded type or implemented interface whose methods are mostly ignored or overridden; drop the embedding and hold the value as a field.
