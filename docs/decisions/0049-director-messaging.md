# D49. Director messaging

Designed, not built: [Director messaging](../messaging.md#director-messaging-designed-not-built) extends the local inbox (D15) so the Guildmaster and other Directors can message a Director.
The inbox has two external sender types, `guildmaster` and `director`; the Steward never messages and acts by command, and each managed revision it causes is a store event, not a message, so a Director's inbox holds only things the Director must judge.
Messages inform only: a Director message may at most be filed as a ticket that opens a Guildmaster decision, so no Director starts work in another's repositories without the Guildmaster's answer, which keeps R1.15's no automatic cross-Director scheduling.
Messages are typed (`handoff-offer`, `dependency-notice`, `question`, `answer`) under a new envelope schema, leaving `guildhall.message/1` unchanged, because free chat between agents would make authority and spam hard to bound.
Waking is per sender type: the Guildmaster always wakes, Director messages draw on one wake budget with backoff, and there is no per-sender limit, because the receiving agent judges content better than a sender quota and the budget bounds the cost; the wake line carries counts only, so no sender types into a Director's session.
Spam is handled by the receiver closing its director inbox with a system alert that only the Guildmaster can resolve, plus a mailbox cap that refuses rather than drops.
Transport is Director to Director over SSH with a forced command per sender key, chosen over relaying through the Steward because a relay would make messaging need a Steward and put it in every conversation; the forced command makes the transport, not the envelope, the sender's identity.
Delivery is at least once with an outbox in the sender's store and exactly-once storage in the receiver's through mutation ids namespaced by sender, the same receipts the store already gives local messages; nothing retries in the background (R1.29).
The shared receive boundary validates identity and resolves an identical committed retry before closed-inbox or capacity checks, returning the original receipt without another wake; conflicting content is refused.
Read, acknowledgement and ticket filing retain the full `(sender, message id)` identity through `--sender <sender>`, and ticket source references retain both fields.
