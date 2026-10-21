# Director rulings as store records, disputes as loop exits

The Director's `out-of-scope`, `duplicate` and `defer` rulings (report §5.4) are a new store record keyed by the review outcome document and finding id, written by `guildhall task finding rule`, rather than messages or documents: a ruling is a move that is made once and contested once, and both need the store's once-only and replay rules.
Keying by document rather than step means neither the store nor the command parses record names; the engine maps the document to its step through its history view.
So that the store can check a ruling's finding and inspection can show findings without opening outcome files (D24), a review outcome's document reference projects its findings and dispositions.
A ruling never clears a finding: the next review attempt is given it and either withdraws the finding or keeps it open, which contests the ruling.
A contested rule-cited blocker opens the round's existing `loop-exit` decision early instead of a new decision kind, so `continue`, `accept` and `reject` keep one meaning and `accept` satisfies the review gate through the same outcome; the alternative, a dedicated dispute decision, would need its own answers and a second path to the gate.
The heartbeat-driven `worker-stalled` event and rebase-aware re-review are later slices.
