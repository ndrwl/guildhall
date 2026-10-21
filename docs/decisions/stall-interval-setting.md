# Stall observation is on by default through a config setting

The interpreter's worker-stalled observation was off in production: `task start` and `step done` built the engine without `Engine.StallInterval`, and the setting had no home in the config, so the one observation the engine makes about a running worker never reached the Director (sustainability review 9, F26).

The Director config's `stall_interval` setting now sets it for every advance, read through `config.StallInterval` and validated by `config.Parse`.
It is an ordinary setting under the existing precedence, so a Steward may manage it and mark it overridable, and a copy sets it under `[local]` otherwise.
The default is 15 minutes, so a worker thinking quietly for a few minutes is not reported; `"0s"` turns the observation off.
The value is a Go duration string, as the `artisan` commands' `--timeout` flags already take, rather than a bare number with an implied unit.
