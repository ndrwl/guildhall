# Scripted mock agent

The interpreter launches Artisan attempts through one seam, `interp.Launcher`, declared in `internal/interp` beside the code that calls it.
`interptest.Script` is its scripted double, the mock agent: tests describe the attempts they expect the interpreter to launch and what each attempt should return.
The mock checks the prompts, skills, artefact revisions and standards it is given and writes the scripted response.
It makes no model calls and is not evidence of semantic intelligence: it checks the launched attempt and its files and returns the scripted answer.
The [interpreter](interpreter.md) launches through it; result intake from a real runtime is separate work.

## The launch seam

| Operation | Contract |
|---|---|
| `Launch(ctx, spec)` | Starts the attempt and returns a `Handle` at once, without waiting for it to finish. Refuses a spec that fails `Spec.Validate`, an adapter the launcher does not run, or an ended context, and then starts nothing. |
| `Stop(handle)` | Stops the attempt; one stopped before writing its result leaves none. Stopping again, or after the attempt finished, is not an error. A handle the launcher never returned is refused. |

Completion comes only from a valid result file at `spec.ResultFile` naming `spec.Identity` ([result file](adapters.md)), never from `Launch` returning or from a process or terminal.
An existing result file is never replaced.
Both operations are safe for concurrent use.

`interp.Spec` carries the attempt identity, the adapter name from the stage's profile, the absolute working directory, prompt file and result file, and the manifest `render.Prompt` returned for that prompt ([prompt rendering](rendering.md#manifest)).
The prompt's `inputs` and `skills` directories are beside the prompt file.
It is the interpreter's own type; `Spec.AdapterSpec` is the reference mapping onto an `adapter.Spec` that the adapter conformance suite runs runtimes on: the same identity, working directory, prompt file and result file, and no extra environment.
Production launch does not use it today: `launch.Process` passes the prompt and working directory as flags and `artisan start` builds its own spec.

## Adapter guard

Every implementation of `interp.Launcher` must call `interp.CheckAdapter` before it constructs or runs an adapter; `Script` and `launch.Process`, the real launcher, are its implementations.
The guard's scope and allowed adapters are defined by [`CheckAdapter`](../internal/interp/launcher.go); it does not intercept direct adapter construction outside this seam.

## Scripts

A script is an ordered list of expectations; each launch must match the next one.
`interptest.Expect(spec, response)` builds the expectation a rendered spec meets, which a test then edits to describe what it requires.

| Expectation field | Matched against the manifest |
|---|---|
| `Step` | The identity's step. |
| `Attempt` | The identity's attempt number; 0 matches any. |
| `Profile` | The identity's profile, empty for none. |
| `Inputs` | Port to digest: every port present, no other port, and each carrying exactly that artefact revision. |
| `Standards`, `Skills` | Names: every one present, no others, in this order. |

Beyond the expectation, the mock checks that the manifest names the prompt, role and guide, that the prompt file's digest is the manifest's, and that every input copy and every skill file beside the prompt has the digest the manifest records.

The script fails the test with `t.Errorf` and refuses the launch with `ErrScript` on:

- a spec whose adapter is not `mock`, including any adapter the guard refuses;
- a launch after the script is exhausted;
- a launch for another step, attempt or profile;
- a manifest that lacks a required input, standard or skill, or carries one not expected, or carries them in another order;
- an input carrying another artefact revision;
- a prompt, input copy or skill file that does not match its manifest.

Once matching begins, a refused launch consumes its expectation, so one wrong launch is reported once.
Expectations still unlaunched when the test ends fail it.
An ended context or a spec that fails `Spec.Validate` is refused before consuming an expectation and without failing the test, because every launcher refuses it.

## Responses

| Response | Writes into the working directory | Result |
|---|---|---|
| `Succeed(summary, files...)` | The given files | `succeeded`, listing them as outputs |
| `Fail(summary)` | Nothing | `failed`, no outputs |
| `Review(outcome)` | `review-outcome.json`, a valid [review outcome](configuration.md#review-outcome) | `succeeded`, listing it |
| `Blocked(question, options...)` | `blocked-question.json`, schema `guildhall.blocked-question/1` | `failed`, listing it |
| `Hang()` | Nothing | None, until stopped and after |

Before writing any response files, the mock rejects empty or absolute output paths, dot segments, empty path segments, backslashes and paths that are not local to the working directory.
It also rejects an output whose cleaned absolute target equals `ResultFile` under `strings.EqualFold` on every platform, or whose existing target is the same file as an existing result according to `os.SameFile`.
These refusals return a launch error without writing any response files; regression coverage is in `TestResponsePathsValidatedBeforeWriting` and `TestResponseRejectsExistingResultAlias`.
Each accepted file is written to a temporary file beside its target and renamed into place, and then the result is published with `adapter.WriteResult`, so a reader sees no result or the whole one and the outputs exist before it.
The mock runs no process: `Launch` returns after writing.

The blocked question file is provisional: no runtime writes it yet, and the interpreter's intake of it is separate work.

## Fixtures

`internal/interp/testdata` is the one fixture tree for launch tests, which the interpreter and the [adapter conformance suite](adapters.md#conformance) reuse.
`interptest.Attempt.Render` renders a prompt from its baseline, documents and skills into a temporary attempt directory and returns the `Spec`; `interptest.Build` and `interptest.AutomatedReview` are the two attempts it describes, and `docs/spec-v1.md` and `docs/spec-v2.md` are two revisions of one artefact.
`AutomatedReview` uses the `review` kind with actor `artisan` and the `stages/review.md` guide.
`interptest.AdapterSpec` renders the build attempt with given step instructions and maps it through `Spec.AdapterSpec`, the attempt the adapter conformance suite runs a runtime on.
`manifests/` pins the manifests those renders produce, `results/` the files the mock writes and `reviews/` the review outcomes it returns. Review outcomes bind the reviewed code head at `revisions.subject.code_head`, alongside `revisions.spec_digest` and `revisions.standards_revision`.

## Shared contract suite

`interptest.RunLauncherContract(t, newHarness)` is the `Launcher` contract as a test suite: invalid specs refused with nothing written, completion from the result file for succeeded and failed outcomes with no temporary files left, a stopped attempt leaving no result, repeated stops, and a foreign handle refused.
A harness renders each attempt with the step instructions its launcher needs, so the fake adapter can carry its prompt directives in the same suite.

## Against the real launcher

The suite runs against the mock and against `launch.Process` with the fake adapter (`internal/launch/process_test.go`), so the mock's agreement with real launches is proven for what the suite checks.
The suite's stop case accepts a launcher that marks the stopped attempt cancelled, as the real one does, so the wait refuses it with `adapter.ErrAttemptCancelled` rather than reporting no result yet; either way no result exists.
