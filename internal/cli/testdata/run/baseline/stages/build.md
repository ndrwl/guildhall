# Build

Build the accepted specification and report the head.
When your feedback is a failed validate result, its `logs[]` holds each failed CI job's log excerpt, and running `logs[].command` with `--out <file>` or `--tail <lines>` fetches that job's full log.

guildhall-fake: output=head:0123456789abcdef0123456789abcdef01234567
