# Budget for uncontended task-lock acquisition in the preflight test

The [CLI test contract](../contracts/cli.md#tests) owns `TestAdvancePreflightLockWait`'s required refusal and timing budget.
An advance must first acquire its task lock, whose deadline includes opening `attempt.lock` and scheduling its initial nonblocking acquisition.
Delaying that OS open by two seconds deterministically reproduces the reported task-lock deadline with the original one-second configuration, without another task-lock holder or a duplicate acquisition by retry.
This establishes a sufficient trigger for the symptom, not proof of which operation stalled on the Windows runner.

Increase the configured wait in this test only, leaving headroom for uncontended task-lock acquisition while keeping the refusal ceiling below the default store wait.
Keep production locking order and cancellation behavior unchanged: an expired acquisition context is a legitimate refusal.
Keep the real store and task-lock integration boundaries rather than replacing either with a fake or accepting either error indiscriminately.
The deterministic delayed-open reproduction is a local Linux diagnostic; native filesystem behavior continues to run in the three-OS CI matrix.
