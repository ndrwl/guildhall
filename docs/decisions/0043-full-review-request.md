# D43. Full-review requests are store records consumed by a round

`guildhall task review --full` records a numbered per-task store record, not a document or a message, because it is a Director move that must survive restarts, carry a receipt like rulings, and be read by the engine through the same history view as every other record.
The engine consumes a request with a second mutation naming the attempt, the same shape as contesting a ruling, so the timeline shows when it was requested and which round it forced.
The attempt consumes the request before its binding is recorded; a replay finds the request consumed by the same attempt, so a crash between the two never loses the request or applies it to a later round.
At most one request is pending, so a retried command is idempotent through its mutation id, which names the request number.
A request makes the round full without classifying it, but the binding still records the range, so a later rebase can be classified against it.
