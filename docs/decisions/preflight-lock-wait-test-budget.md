# Budget for uncontended task-lock acquisition in the preflight test

`TestAdvancePreflightLockWait` tests propagation of the configured wait to store opens by holding the real store lock.
An advance must first acquire its task lock, whose deadline includes opening `attempt.lock` and scheduling its initial nonblocking acquisition.
Delaying that OS open by two seconds deterministically reproduces the reported task-lock deadline with the original one-second configuration, without another task-lock holder or a duplicate acquisition by retry.
This establishes a sufficient trigger for the symptom, not proof of which operation stalled on the Windows runner.

Use a five-second configured wait and a twenty-second refusal ceiling in this test only.
This allows the delayed uncontended task lock to succeed and the command to reach the expected store refusal, while a regression to the default thirty-second store wait still exceeds the ceiling.
Keep production locking order and cancellation behavior unchanged: an expired acquisition context is a legitimate refusal.
Keep the real store and task-lock integration boundaries rather than replacing either with a fake or accepting either error indiscriminately.
The deterministic delayed-open reproduction is a local Linux diagnostic; native filesystem behavior continues to run in the three-OS CI matrix.
