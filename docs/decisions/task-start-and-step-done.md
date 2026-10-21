# Task start and step done join the interpreter to the owned launcher

`guildhall task start` and `guildhall step done` each run one interpreter advance, and the real `interp.Launcher`, `launch.Process`, runs every Artisan attempt as its own detached `guildhall artisan run` process over the directory the interpreter prepared.
The attempt layout has one owner, `artisan.AttemptKey` (`Dir`, `AttemptID`, `ResultFile`), so the two sides cannot disagree about where an attempt lives (review 7 F19).

The advance opens the store for each record operation, as the launcher does, instead of holding it for the whole advance.
`Process.Launch` returns only once the launcher process owns the attempt, so `Stop` can reach it; that process needs the store's single-writer lock to take ownership, and an advance holding the store would deadlock with it.
Two advances of one task are serialised by the task's own lock, `attempt.lock` in `<state>/<repository>/<task>`, which the interpreter's "not safe for concurrent use on one task" requires once the store no longer serialises them.

The launcher and the result intake both record an attempt's store outcome, because either may happen first: the launcher when it decides the attempt, the intake when the result file is taken.
The first record stands.
A succeeded outcome the launcher recorded first still has its outputs recorded, by the next advance whatever its event, and the launcher accepts an identical outcome the intake recorded first; a conflicting one, such as a stop racing a result, keeps the store's and the launcher reports it.

`task start --decision <id>` feeds `decision-answered`, because answering a decision never advances a task and nothing else would feed that event until the Director runs.
The Director's inputs that have no home yet come from beside its config: workflows from `workflows/` as `task new` reads them, and the prompt baseline from `baseline/`; documents and attempts live under `--state`.
The default adapter is `fake`, the only one `artisan run` runs, until the Claude adapter lands.

The acceptance run uses a Director-local workflow, `fake-straight`, rather than a shipped one: both shipped workflows begin with a Director grill and include publish and validate, which the engine refuses as unsupported, and those are out of this slice.
Its Artisan stage guides carry the fake adapter's `output` directive, because workflow validation refuses `instructions` on plan and build steps.

The per-task watcher that waits on the result file and calls `step done` (R1.29) is the next slice, S7b; until it lands the caller runs `step done` once the result exists.
