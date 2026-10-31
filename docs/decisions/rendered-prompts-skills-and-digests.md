---
status: accepted
date: 2026-10-21
topic: engine
formerly: D25, 0025-rendered-prompts-skills-and-digests.md
---

# Attempt prompts and skills are rendered once into self-contained files with platform-independent digests

## Context

An attempt must be reproducible and checkable from its own files, on every platform Guildhall runs on, including Windows checkouts with CRLF endings.

## Decision

The [rendering contract](../rendering.md) defines the self-contained attempt files and returned manifest.
Text sources are normalised to LF before they are rendered and digested, so CRLF checkouts pin the same digests; input documents and skill files keep their exact bytes because they are used as is.
Skills are normal agent skills and may include scripts (audit decision 3): the renderer copies every file and leaves control of executable content to the Steward's rollout review.
It refuses symbolic links and other non-regular files, so a copy never reaches outside the skill directory.
A skill's tree digest uses the `sha256sum` line format over its sorted files and excludes file modes, so it is reproducible with standard tools and identical on every platform.
An attempt directory is rendered once and never replaced, so a recorded manifest always describes the files beside it.

## Consequences

Executable skill content is trusted on the strength of the Steward's rollout review, not checked by the renderer.
A skill that relies on file modes or symbolic links cannot be rendered.
