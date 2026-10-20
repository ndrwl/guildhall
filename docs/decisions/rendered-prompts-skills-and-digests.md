# Rendered prompts, skills and their digests

An Artisan's prompt is one Markdown file per attempt, written by pure functions from a role prompt, a stage guide, the step's instructions, input documents cited by name and digest, standards inlined in full and skills copied beside it; the returned manifest lists every file and digest so the attempt can be reproduced and checked.
Text sources are normalised to LF before they are rendered and digested, so Windows checkouts with CRLF endings pin the same digests; input documents and skill files keep their exact bytes because they are used as is.
Skills are normal agent skills and may include scripts (audit decision 3): the renderer copies every file and leaves control of executable content to the Steward's rollout review.
It refuses symbolic links and other non-regular files so a copy never reaches outside the skill directory.
A skill's tree digest uses the `sha256sum` line format over its sorted files and excludes file modes, so it is reproducible with standard tools and identical on every platform.
An attempt directory is rendered once and never replaced, so a recorded manifest always describes the files beside it.
