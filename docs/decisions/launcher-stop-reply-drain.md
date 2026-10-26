# Drain endpoint replies before launcher exit

## Decision

`endpoint.Endpoint.Close` stops accepting connections, removes the socket path, and waits for accepted exchanges to finish before returning.
An exchange finishes after its reply write has completed or failed, including the existing transport deadline.
Concurrent calls to `Close` wait for the same drain.
The accept loop itself is counted until it exits, so an accepted connection racing listener closure cannot escape the drain.

## Reason

The launcher already defers endpoint closure, but closure previously returned while reply goroutines were still running.
A stop handler can release the launcher's main execution before the endpoint has encoded and written the stop reply.
Process exit then truncates the reply and makes a successful stop appear to fail.
Waiting at the endpoint boundary fixes that lifecycle ordering for the existing launcher without weakening the caller's protocol validation or treating EOF as a successful reply.

## Limits

Shutdown waits for handlers to return; it does not impose a new handler deadline or cancel handler work.
Handlers must not call `Close` themselves because shutdown waits for their exchanges.
Read and write deadlines still bound stalled transport peers.
A reply write failure remains a transport failure; draining does not guarantee delivery to a disconnected caller.
The Windows-only worker stop path is unchanged.

## Proof

`TestCloseWaitsForReplyWrite` uses a real socket and blocks JSON encoding after the handler has returned.
It checks that two concurrent closure calls remain blocked until encoding is released, then checks both closures and the decoded reply.
The regression failed on the previous implementation and passed after the drain was added.
The endpoint contract page links the captured evidence.
