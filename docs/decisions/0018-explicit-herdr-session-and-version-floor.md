# D18. Explicit Herdr session and version floor

Every Herdr call carries `--session <name>` and a matching `HERDR_SESSION`, with inherited Herdr context variables removed, so ambient state never chooses the server.
The session-selection and tab-ownership rules are defined in [Herdr access](../adapters.md#herdr-access).
0.9.5 is the minimum client and server version because it is the version exercised live; an older or unparsable version is refused.
