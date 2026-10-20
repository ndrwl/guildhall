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
| `prompt.md` | The attempt identity (repository, task, step, kind and version, attempt number, profile), the role prompt, the stage guide, the step's `instructions`, the input documents by name and digest, the standards in full, and the skills by path and tree digest. |
| `inputs/<recorded-name>` | A byte-identical, digest-verified copy of each input document. |
| `skills/<name>/` | A copy of each skill the step names in `skills`, in order. |

Input documents are listed rather than inlined: the Artisan reads verified copies from `inputs/<recorded-name>`, relative to the prompt file, without needing access to the original document directory.
The manifest retains the recorded names without the `inputs/` prefix or host-specific paths.
Standards are inlined, because a reviewer measures against those exact words.
An input whose file does not match the sha256 its record names is refused (`ErrDigestMismatch`), so a prompt never cites a document that changed after it was recorded.
On an error nothing `Prompt` wrote is left behind; a crash mid-render can leave files, which the caller removes before rendering that attempt again, since the attempt was not yet recorded.

### Line endings and paths

The prompt file uses LF line endings only.
The role, stage guide, standards and instructions have CRLF and lone CR normalised to LF before they are rendered and digested, so a Windows checkout with CRLF endings yields the same prompt and the same manifest.
Input documents and skill files are digested over their exact bytes, because their content is used as is.
Every path in the manifest is slash-separated; a source name given with backslashes is read with slashes, and a name that is absolute, has a drive letter or has dot segments is refused.

## Skills

A skill is a directory, named in the stage's `skills` setting, that the Artisan's runtime loads as a normal agent skill.
Each name is looked up in the skill roots the caller gives, in order; the Director's are the release, the Steward-managed skills directory and the Director's config area.
The first root holding a directory of that name wins; a name found in none is refused (`ErrUnknownSkill`).
Every file is copied, scripts included, byte for byte and with the permission bits the platform supports, so a POSIX script stays executable.
A symbolic link or other non-regular file inside a skill is refused rather than followed.
Executable content is controlled where skills are distributed, by the Steward's rollout review, not by the renderer ([Rendered prompts, skills and their digests](decisions/rendered-prompts-skills-and-digests.md)).

A skill's tree digest is the sha256 of one `<hex>  <path>` line per file, sorted by path, where `<hex>` is the file's sha256 and `<path>` is relative to the skill directory: the output of `sha256sum` run inside the skill over its sorted files.
File modes are not part of it, so a skill digests the same on every platform.

## Manifest

The manifest, schema `guildhall.prompt-manifest/1`, is returned with the paths written.
Every digest is `sha256:<64 lowercase hex>`.

| Field | Meaning |
|---|---|
| `identity` | `repository`, `task`, `step`, `kind`, `kind_version`, `attempt` and, when set, `profile`. |
| `prompt` | `prompt.md` and the digest of its bytes. |
| `role`, `guide` | The source name and the digest of its normalised text. |
| `standards[]` | The same, in rendering order. |
| `inputs[]` | `port`, the recorded `path` and the digest of the file's bytes. |
| `skills[]` | `name`, the `root` it was resolved from, `path` (`skills/<name>`), the tree `digest` and every file with its digest. |

## Guildmaster questions

`render.Question(stage, inputs)` refuses an Artisan stage (`ErrWrongActor`) and reads no files.
The question names the task, step and attempt, carries the step's `instructions` and lists the inputs by name and recorded digest.
The complete question text has CRLF and lone CR normalised to LF.

| Kind | Options |
|---|---|
| `guildmaster-approval` | `approved`, `changes_requested` |
| `human-review` | `approved`, `changes_requested`, `rejected` |
| `handoff-wait` | `received` |

## Profiles

A step's `profile` setting names a `[profile.<name>]` table in the Director config, which chooses the adapter, model, effort and role prompt of the Artisan that runs it ([configuration](configuration.md#profiles)).
`config.CheckProfiles` refuses a workflow that names a profile the config does not declare.
The renderer records the profile name in the identity; resolving the role prompt from the profile is the caller's choice of `baseline.Role`.
