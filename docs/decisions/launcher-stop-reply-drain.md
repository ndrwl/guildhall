# Drain endpoint replies before launcher exit

## Decision

Drain replies at the shared endpoint boundary rather than special-casing launcher stop or accepting EOF as success.
The [endpoint contract](../contracts/endpoint.md) owns shutdown semantics; `internal/endpoint/endpoint.go` implements the accept-loop accounting that prevents a connection racing closure from escaping the drain.

## Reason

The launcher already defers endpoint closure, but closure previously returned while reply goroutines were still running.
A stop handler can release the launcher's main execution before the endpoint has encoded and written the stop reply.
Process exit then truncates the reply and makes a successful stop appear to fail.
Waiting at the endpoint boundary fixes that lifecycle ordering for the existing launcher without weakening the caller's protocol validation or treating EOF as a successful reply.

## Limits

Handler obligations and transport bounds are defined in the [endpoint contract](../contracts/endpoint.md).
Draining does not guarantee delivery to a disconnected caller.
The Windows-only worker stop path is unchanged.

## Proof

The [endpoint contract's tests section](../contracts/endpoint.md#tests) owns the regression description and captured evidence.
