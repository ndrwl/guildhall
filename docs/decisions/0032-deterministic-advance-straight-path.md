# D32. Deterministic advance over records, one write per record

The interpreter derives where a task stands from its store records on every advance, not from the event it was given, so the same advance repeated after a crash finds the same position and repeats only what is missing.
Each write uses a mutation id derived from the task and the record it writes, so a repeated write replays its original receipt instead of recording twice.
An advance is several writes rather than one transaction, because the store's mutations are one record each and a cross-record transaction would widen the store's interface for one consumer; ordering the writes so that every prefix is one a repeat completes gives the same recovery.
An attempt is recorded before it is launched and marked launched only after `Launch` returns, so a crash between the two relaunches the same identity and never records a second attempt; refusing a live duplicate is left to the launcher's ownership record rather than guessed at here.
The engine reads the store through `interp.Records`, a narrow interface of the methods it calls, with an in-memory double that passes one contract suite with the real store (D9 permits a consumer's narrow interface).
A step's first output is its output port's artefact; the original file-only code-head representation is superseded by [code-head output intake](../interpreter.md#code-head-output-intake).
The merge decision offers `merge` and `hold`, because the review verdicts do not describe a merge.
Loops, gates other than the spec gate, engine steps other than `deliver` and Director stages are refused rather than approximated, so later slices add them without changing what the straight path records.
