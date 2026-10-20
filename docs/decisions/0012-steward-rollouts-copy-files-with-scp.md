# D12. Steward rollouts copy files with scp and nothing else

Rollouts in `internal/steward` run only `scp -q -B -o BatchMode=yes -- <source> <destination>` from `PATH`, as an argument list, and no remote command.
This leaves access to the user's SSH config, agent, keys and host-key checking, works the same against POSIX and Windows OpenSSH servers, and avoids any remote shell, whose quoting differs between them.
Roster aliases and copy directories are restricted to characters no remote shell or `scp` interprets, so untrusted roster values cannot inject options or commands.
Atomic replacement would need a remote rename command, so publication copies the file in place.
See [apply](../steward.md#apply) for revision preservation, successful-upload verification and failed-transfer limitations.
