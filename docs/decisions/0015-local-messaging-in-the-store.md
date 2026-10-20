# D15. Local messaging in the store

Director and Artisan messages are records in the Director's existing store, not a separate database, queue, server or file drop.
A send is a store mutation whose id is derived from the message id, so the message, its inbox entry and its receipt commit in one transaction and the store's replay and conflict rules give retries and id reuse their meaning without a second deduplication scheme.
The `messages` and `inbox` buckets are per-repository buckets created on first use, like the other record buckets, so the store stays at schema 1; no release has shipped a schema 1 reader that lacks them.
With no Director or Artisan registry yet, an Artisan's inbox is named by the running stage attempt it works on; a later registry can map onto that identity rather than replace it.
The command line takes the store path explicitly and the envelope from a file or standard input; runtime notifications, daemons and transports remain deferred.
