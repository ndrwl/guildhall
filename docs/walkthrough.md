# Walkthrough: driving a task by hand

Until the conversational Director is built, you run the steps it will run, yourself, with the `guildhall` commands.
This page is the full manual walkthrough the [README](../README.md#try-it-today) summarises.

Today's commands take the Director's store file with `--store`; no command creates a store yet, so copy the test store, which holds task `t-1` in repository `app`, and never use the committed file:

```sh
cp internal/store/testdata/schema-v1.store demo.store
```

## 1. Set up a Director

A Director's copy holds a `config.toml` naming its repositories, their merge policy and workflow, and its Artisan profiles; see [configuration and workflows](configuration.md).
A Director runs standalone on that config, or managed by a Steward that adds shared settings; see [standalone and managed Directors](director-modes.md) (designed, not built).
Every role will start, stop, report and resume with one grammar, for example `guildhall director start claude`; see [the role grammar](decisions/0053-cli-role-grammar.md).
`director start` is not built yet.
Start a role through Guildhall rather than running the agent directly, so it gets its rendered skills, settings, preflight and recorded identity.

## 2. Give it work

The `task new` and `task start` lines need a Director config, `director.toml`, that declares a `[[repository]]` with `id = "app"` whose `path` is a git checkout; the examples in [configuration and workflows](configuration.md) name other repositories, so change one to `app` before using it.
`task start` also needs a `baseline/` directory beside the config holding `roles/artisan.md` and `stages/<kind>.md`; see [what an advance runs over](running.md#what-an-advance-runs-over).

```sh
./guildhall task new --store demo.store --config director.toml --repo app --title "Add a health check" t-2
./guildhall task start --store demo.store --config director.toml --state state --repo app t-2
```

A task is one unit of work in one repository; creating it records it and binds the exact workflow and configuration it was accepted under.
`task start` launches the task's next ready stage as an owned Artisan attempt.
The engine does not run Director stages yet, and the default workflow's first stage, `grill`, is run by the Director, so to try this give `app` a `[repository.workflow_overrides.grill]` with `actor = "artisan"`.
The override changes only the actor: for the unattended grill, in which the Artisan proposes answers for you to review at acceptance instead of the Director interviewing you, the workflow's grill step must also list `guildhall-grill-unattended` in its `skills`.
Nothing waits in the background yet, so you run `guildhall step done` once an attempt's result file exists; see [tasks](tasks.md) and [running a task](running.md).

Each task follows a workflow.
The shipped [default workflow](../internal/workflow/default.json) is:

```mermaid
flowchart LR
    grill --> research --> plan --> accept{{"Guildmaster<br/>approval"}}
    accept -- "changes requested, max 3" --> plan
    accept --> build --> review["Artisan review"]
    review -- "changes requested, max 3" --> build
    review --> publish --> validate
    validate -- "failed, max 3" --> build
    validate --> deliver
```

A repository can have you review instead of an Artisan without changing any other repository; see [configuration and workflows](configuration.md).

## 3. Watch it

```sh
./guildhall task list --store demo.store --json
./guildhall task timeline --store demo.store --repo app t-1 --json
```

`task show`, `timeline` and `files` read a task's attempts, decisions and documents without changing anything; `guildhall artisan status` classifies one Artisan attempt; see [Artisan commands](artisan.md#commands).
In the planned setup, Herdr, a terminal multiplexer, shows each Director and Artisan in its own tab; a tab is presentation only, so closing it never changes a task.
Directors and Artisans also exchange informational [messages](messaging.md), which never carry a result, a decision or an approval.

## 4. Answer its decisions

Decisions waiting for you are listed per machine; there is no push service.

```sh
./guildhall decide list --store demo.store
./guildhall decide show --store demo.store --repo app --task t-1 --decision <id>
printf 'approve\n\nKeep the migration behind a flag.\n' | ./guildhall decide answer --store demo.store --repo app --task t-1 --decision <id> --answer-file -
```

Your exact words are recorded; for a decision offering `approve` and `reject`, a leading `approve` is taken as the choice and the rest as a note.
Answering never advances a task by itself: continue it with `task start --decision <id>`.
See [decisions](decisions-cli.md).

## 5. Approve or stop work

Approving a plan or a merge is answering its decision, as above.
To stop one running Artisan attempt, use `guildhall artisan stop`; see [Artisan commands](artisan.md#commands).

## More examples

### Send and acknowledge a message

```sh
cp internal/store/testdata/schema-v1.store demo.store
cat > note.json <<'EOF'
{"schema": "guildhall.message/1", "id": "t-1-note-1", "recipient": "director", "repository": "app", "task": "t-1", "type": "note", "text": "Build finished; the plan is ready for review."}
EOF
./guildhall messages send --store demo.store director note.json
./guildhall messages list --store demo.store director
./guildhall messages ack --store demo.store director t-1-note-1
./guildhall store doctor demo.store
```

`send` prints the receipt only after the message is committed, and sending the same envelope again prints the same receipt and stores nothing new:

```json
{"mutation_id":"message:t-1-note-1","operation":"send-message","request_digest":"sha256:8c4f3149fed51b4795c3dedf5bb628671bd249f1c5923c77723be239346f628f","repository":"app","task_id":"t-1","sequence":8,"committed_at":"2026-10-20T19:06:04.839891945Z"}
```

`list` shows the unacknowledged message, and after `ack` the inbox is empty while the message stays readable by id:

```console
$ ./guildhall store doctor demo.store
store: demo.store
schema_version: 1 (supported: 1)
integrity: ok
receipts: 9
workflow approvals: 0
repository app: tasks 1, attempts 1, documents 1, pane bindings 1, decisions 1, rulings 0, owners 0 (0 starting without containment), messages 1 (0 unread)
```

### Preview a Steward rollout

A rollout needs a private config directory with a roster of enrolled machines, which this repository does not ship, and SSH access to those machines.
Use the [dry-run procedure](steward.md#dry-run) to preview changes and save a local plan:

```sh
./guildhall steward rollout --config steward.toml --machine workstation --plan rollout-plan --dry-run
```

See [Steward rollouts](steward.md) for the roster format, the printed plan, approval and `--apply`.
