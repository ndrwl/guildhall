# CI logs

`guildhall ci log` prints the full log of a failed GitHub Actions job that a task's [validate](interpreter.md#validate) results record.
It is how an Artisan reads more than the log excerpt in its feedback without being granted `gh` ([decision](decisions/failed-job-logs.md)).

```sh
guildhall ci log [--copy <dir>] [--tail <lines>] [--out <file>] <repo>/<task> <job|check>
guildhall ci log --help
```

The copy is `--copy`, or else the copy [copy discovery](running.md#copies) finds from the current directory.
An Artisan runs under the copy's `state/`, where discovery finds no copy, so the command a validate result's `logs` names carries `--copy '<copy>'`, single-quoted for a POSIX shell, when the advance ran from a copy.
The second argument is a job id, or a check name, meaning that check's latest failed job.
The command reads the store read-only and takes no task lock, so it runs while the task advances.
It serves only jobs the task's failed validate results record in their `logs`, so an Artisan cannot read another task's or another repository's logs through it.

It reads the job's log from the [CI log cache](interpreter.md#validate), `<state>/<repo>/<task>/ci-logs/<job>.log`, which validate filled when it recorded the round; when the cache does not have it, because it was pruned or never filled, it fetches the log with Guildhall's own `gh`, as `forge.GitHub.CheckLog` does, and caches it again.
The config's [`ci_log_limit`](configuration.md#settings) caps the log, keeping its tail; a log cut short starts with `[guildhall: <N> bytes dropped from the head of this log]`.
`"0"` turns fetching off.

- By default the log goes to standard output.
- `--tail <lines>` prints only its last lines; the dropped-bytes line comes first only when they reach the head of the kept log.
- `--out <file>` writes it to the file and prints the file's path, its size and the bytes dropped, so an Artisan can read or search the file instead of spending tokens on standard output.

Exit status:

- 0: the log was served.
- 1: the log is unavailable, with one line naming why; a log GitHub has expired prints `GitHub no longer keeps the log of job <id> (expired); only the excerpt in <result path> remains`.
- 2: a usage error, including a job or check the task records no failed Actions job for, which lists the task's failed checks and their jobs.
