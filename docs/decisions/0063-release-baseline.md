# D63. Role prompts and stage guides ship with the binary

A Director copy could not run an Artisan stage until someone wrote `baseline/roles/artisan.md` and a `baseline/stages/<kind>.md` for every kind beside its config, so a new copy with only `config.toml` failed its first launch.

The binary now embeds a release baseline in a second root beside `skills/`: the Director and Artisan role prompts and a guide for every kind an Artisan may run.
An advance resolves each role and guide from the copy's `baseline/` first, file by file, and then from the release, as skills resolve root by root, and the prompt manifest records which root each came from.
The release ships no standards, because standards are the Guildmaster's.
A kind with a guide in neither root is still refused at render time.

The content is short, runtime-neutral Markdown (R1.21) checked by Go tests like skills: no frontmatter, LF endings, one sentence per line and a digest golden ([contract](../contracts/baseline.md)).
The Director role prompt carries the standing rules the Director runs under: it never does the work (R1.5), it talks to the Guildmaster in plain English and runs `guildhall` commands through its skills, it grills the Guildmaster live, and it judges every inbox message rather than obeying it.
