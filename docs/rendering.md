# Prompt rendering

`internal/render` turns one workflow stage into what its actor reads.
For an Artisan stage, `render.Prompt` writes a prompt file, an inputs directory and a skills directory and returns a manifest of every file it used.
For a Guildmaster stage, `render.Question` returns the typed question a decision opens.
Both are pure over files: they read the sources they are given, write only inside the attempt directory they are given, run nothing and record nothing.
Choosing which role, stage guide, standards and skill roots apply, and recording the manifest with the attempt, belong to the caller.

## Artisan prompts

`render.Prompt(stage, inputs, baseline, roots)` refuses a Guildmaster stage (`ErrWrongActor`) and writes into the stage's attempt directory, which must not exist yet or must be empty (`ErrNotEmpty`), so a rendered attempt is never replaced.

| Written | Contents |
|---|---|
| `prompt.md` | The attempt identity (repository, task, step, kind and version, attempt number, profile), then for a retried attempt (`Stage.Retry`) a "Why this is a retry" section, the role prompt, the stage guide, the step's `instructions`, the input documents by name and digest, the standards in full, the skills by path and tree digest, and the [Artisan output instructions](#required-outputs-and-result-template). |
| `inputs/<recorded-name>` | A byte-identical, digest-verified copy of each input document. |
| `skills/<name>/` | A copy of each skill the step names in `skills`, in order. |

Code-review prompts also include `Review base: <commit>` alongside the attempt identity, using `Stage.ReviewBase` verbatim; an empty value renders as `Review base: unavailable`.
Other step kinds omit this field.
The [interpreter](interpreter.md#rounds-and-loops) owns selecting the value, and the [review skill](../skills/guildhall-review/SKILL.md#what-you-review) owns how the reviewer uses it.

The "Why this is a retry" section, for Artisan and Director prompts alike, names the retried attempt and how it ended, then quotes the Director's reason ("None given." when empty), the intake rejection (only when there is one) and the previous result summary ("None recorded." when empty), and closes by saying the stage guide and required outputs still apply.
Each quoted text sits in a `text` fence longer than any backtick run inside it, so agent-written text cannot read as stage instructions.
Prompts without `Stage.Retry` have no such section.

Input documents are listed rather than inlined: the Artisan reads verified copies from `inputs/<recorded-name>`, relative to the prompt file, without needing access to the original document directory.
The manifest retains the recorded names without the `inputs/` prefix or host-specific paths.
Standards are inlined, because a reviewer measures against those exact words.
An input whose file does not match the sha256 its record names is refused (`ErrDigestMismatch`), so a prompt never cites a document that changed after it was recorded.
On an error nothing `Prompt` wrote is left behind; a crash mid-render can leave files, which the caller removes before rendering again only while no launch has been given them (the interpreter renders into a staging directory and [publishes it once](interpreter.md#attempt-preparation)).

### Required outputs and result template

Every rendered Artisan prompt includes the kind's required output ports, work-directory-relative file names, exact formats and minimal valid examples, even when a copy overrides its stage guide.
Document ports use `<port>.md` UTF-8 Markdown; review outcomes use `outcome.json` and the strict `guildhall.review-outcome/1` format.
Review and document-review sections state the finding-id rule (lowercase letters, digits and hyphens, starting with a letter, at most 63 characters, kept across rounds) and show, after the approval example, an example with one finding whose id intake accepts; intake still refuses any other id.
The interpreter supplies the attempt's recorded review revisions through `Stage.Revisions`, so its example has the exact subject, accepted spec digest (for code review) and standards revision that intake requires.
Standalone rendering without revisions shows explicitly illustrative values to replace, never a claim that zero hashes identify the real subject.
A review must write the outcome file and list it in result `outputs`; a verdict in summary alone is insufficient.
Code-head ports need no file: Guildhall records the final committed Git HEAD automatically, and the result uses `outputs: []`.
The generated Result template carries the task and attempt identity and the matching file entries.
Adapters name where to write that result and refer to this template instead of supplying an empty-output default.
Director-actor prompts omit these generated sections and retain [command-based completion](running.md#step-finish).
Engine-only and Guildmaster-only kinds do not launch an Artisan and have no Artisan guide or result template.

### Line endings and paths

The prompt file uses LF line endings only.
The role, stage guide, standards and instructions have CRLF and lone CR normalised to LF before they are rendered and digested, so a Windows checkout with CRLF endings yields the same prompt and the same manifest.
Input documents and skill files are digested over their exact bytes, because their content is used as is.
Every path in the manifest is slash-separated; a source name given with backslashes is read with slashes, and a name that is absolute, has a drive letter or has dot segments is refused.

## Skills

A skill is a directory, named in the stage's `skills` setting, that the Artisan's runtime loads as a normal agent skill.
Each name is looked up in the skill roots the caller gives, in order; the Director's are the release, the Steward-managed skills directory and the Director's config area.
The first root holding a directory of that name wins; a name found in none is refused (`ErrUnknownSkill`).
The release root is the binary's embedded `skills` directory, written out by `skills.Install` ([contract](contracts/skills.md)).
`guildhall render <request.json>` installs it temporarily and always supplies it first as `release`, followed by optional `Managed` and `Director` directories.
The JSON request contains `Stage`, `Inputs` and `Baseline` using the [renderer types' Go field names](../internal/render/render.go), plus those optional directory paths; `Stage.Step` uses the [workflow JSON fields](configuration.md#workflow-definitions), and relative paths resolve from the command's working directory.
Strict JSON decoding, request validation and refusal behavior are specified in the [CLI contract](contracts/cli.md).
The command writes the manifest as JSON to stdout and leaves the self-contained prompt at `Stage.Dir/prompt.md`, ready for `artisan start --prompt`.
The temporary release root is removed on success or failure.
A request whose `Baseline` has no `Role` or `Guide` path takes the [release baseline](contracts/baseline.md)'s `roles/artisan.md` or `stages/<kind>.md`, installed temporarily and recorded with root `release`.
Each adapted skill includes its own `CREDITS.md`, preserving the upstream MIT notice in the attempt.
Release embedding is delivered here; Steward distribution of skill bundles follows in a later slice.
Every file is copied, scripts included, byte for byte and with the permission bits the platform supports, so a POSIX script stays executable.
A symbolic link or other non-regular file inside a skill is refused rather than followed.
Executable content is controlled where skills are distributed, by the Steward's rollout review, not by the renderer ([Rendered prompts, skills and their digests](decisions/0025-rendered-prompts-skills-and-digests.md)).

A skill's tree digest is the sha256 of one `<hex>  <path>` line per file, sorted by path, where `<hex>` is the file's sha256 and `<path>` is relative to the skill directory: the output of `sha256sum` run inside the skill over its sorted files.
File modes are not part of it, so a skill digests the same on every platform.

## Manifest

The manifest, schema `guildhall.prompt-manifest/1`, is returned with the paths written.
Every digest is `sha256:<64 lowercase hex>`.

| Field | Meaning |
|---|---|
| `identity` | `repository`, `task`, `step`, `kind`, `kind_version`, `attempt` and, when set, `profile`. |
| `prompt` | `prompt.md` and the digest of its bytes. |
| `role`, `guide` | The source name, the digest of its normalised text and, when the caller names one, the baseline `root` it came from, such as `copy` or `release`. |
| `standards[]` | The same, in rendering order. |
| `inputs[]` | `port`, the recorded `path` and the digest of the file's bytes. |
| `skills[]` | `name`, the `root` it was resolved from, `path` (`skills/<name>`), the tree `digest` and every file with its digest. |

## Director sessions

`render.Session(input, roots)` renders one Director session into its session directory, which must not exist or be empty (`ErrNotEmpty`), for [`director start`](director.md#start).
It writes, and lists with digests in `manifest.json`, schema `guildhall.session-manifest/1`:

| Field | Meaning |
|---|---|
| `session` | The session number. |
| `role` | `role.md`, the role prompt with line endings normalised to LF, the digest of that text and the baseline `root` it came from, so a CRLF checkout renders the same digest. |
| `startup` | `startup.md` and the digest of its bytes: the session is starting up and recovering, the newest handoff note's path or `None.`, and the in-flight snapshot one item per line or `None.`. |
| `files[]` | The runtime's own files, such as Claude Code's `.claude/settings.json`, by slash path, with the digest of their bytes. |
| `skills[]` | Every skill of the roots, in name order: `name`, the first `root` holding it, `path` under the runtime's skills directory, the tree `digest` and every file with its digest, copied as [skills](#skills) are for an attempt. |

A root directory that does not exist holds no skills, and a root's files, such as `CREDITS.md`, are not skills.
A skills directory or runtime file path that is not relative and slash-separated, or that collides with another file the session writes, and an in-flight item of more than one line, are refused before anything is written; on any error nothing `Session` wrote is left behind.
`render.StartupMessage` names `startup.md` relative to the rendered session directory.
For runtimes launched elsewhere, the supervisor supplies an absolute startup-file path instead ([Director start](director.md#start)): for Claude Code the first message is `/version startup "<absolute startup.md path>"`, the `/version` command at `.claude/commands/version.md`, one of the runtime's files listed in `files[]`, which shows the binary's version and then has the model follow `startup.md`.

## Guildmaster questions

`render.Question(stage, inputs)` refuses an Artisan stage (`ErrWrongActor`) and reads no files.
The question names the task, step and attempt, carries the step's `instructions` and lists the inputs by name and recorded digest.
The complete question text has CRLF and lone CR normalised to LF.

| Kind | Options |
|---|---|
| `guildmaster-approval` | `approved`, `changes_requested` |
| `review` or `document-review` with actor `guildmaster` | `approved`, `changes_requested`, `rejected` |
| `handoff-wait` | `received` |

## Profiles

A step's `profile` setting names a `[profile.<name>]` table in the Director config, which chooses the adapter, model, effort and role prompt of the Artisan that runs it ([configuration](configuration.md#profiles)).
`config.CheckProfiles` refuses a workflow that names a profile the config does not declare.
The renderer records the profile name in the identity; resolving the role prompt from the profile is the caller's choice of `baseline.Role`.
