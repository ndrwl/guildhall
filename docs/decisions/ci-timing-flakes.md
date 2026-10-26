# Timing-independent test outcomes

Tests synchronize on observable events, channels or conditions, not an assumed amount of runner time.
Real process and filesystem waits retain generous, CI-scaled failure ceilings; polling cadence is not a success deadline.
The timing guard enforces a ten-second minimum for real-time test ceilings and requires a local `timing:allow` comment explaining intentional short timers or sleeps.
Synthetic time, deliberate timeout inputs and polling cadence are not runner-speed assertions.

An owned Artisan's `--timeout` is a result-wait budget, beginning only after the worker starts and its ownership is recorded.
It must not expire while taking ownership, binding the endpoint or starting the worker.
Caller cancellation remains effective throughout setup.
An ownership wait checks cancellation before accepting a ready endpoint, so delayed reads cannot turn an expired wait into success.

A launcher which finishes without ever asking its unit to start a worker records explicit `not-started` containment evidence when no named containment was recorded.
That evidence is published only after supervision ends, never in the crash window before startup.
It proves there are no worker processes, but does not prove the launcher itself has exited.
Missing containment, a failed startup invocation and a crashed launcher remain conservative, not equivalent to `not-started`.
This lets a later stop prove termination after a deliberately cancelled pre-start launch on process-group platforms without weakening crash recovery.
