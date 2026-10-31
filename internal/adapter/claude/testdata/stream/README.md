# Claude stream-json fixtures

`session.jsonl` is recorded from a live Claude Code 2.1.316 print-mode session, run with `claude -p --output-format stream-json --verbose --model sonnet --setting-sources project --permission-mode acceptEdits` in a scratch directory holding `spec.md`, `go.mod` and a failing `x_test.go`.
The prompt asked for a Markdown plan, then an Agent call and a Read of `spec.md` in one message, then `go test ./...`, then an Edit of `x_test.go`, then a closing sentence.
It holds: the `system` init and task events, assistant text with Markdown, two parallel tool calls (Agent, then Read) whose results arrive in the opposite order, the subagent's own Bash call and result (with `parent_tool_use_id` set), the Agent's array-form result content, a `go test` result with `is_error: true`, an Edit and its result, closing text and the final `result` event.

The recording is kept byte for byte, except that the scratch directory's path is replaced with `/work` (and with `-work` where Claude Code encodes it in a project directory name), and the home directory in `memory_paths` with `/home/user`.

A live session does not emit malformed or oversized lines, so the tests add both: a line over the 4 MiB bound after the second line, and the recorded Read call line cut short after the sixth.

`session.events.jsonl` is the Workbench events the wrapper writes for it, with every time set to the tests' fixed clock.
After re-recording, keeping each case above, regenerate it with `go test ./internal/adapter/claude -run TestReadableStreamFixtureEvents -update` and update that test's expected stdout and event checks.
