// Package skills is Guildhall's release skills root: the agent skills every Director ships with,
// embedded in the binary with their attribution in CREDITS.md.
//
// Each skill is a directory named guildhall-<name> holding a SKILL.md and any reference files or scripts.
// Install writes the root to a directory that the caller gives the renderer as its first skill root,
// named "release", ahead of the Steward-managed and Director roots.
// See docs/rendering.md and skills/CREDITS.md.
package skills

import (
	"embed"
	"errors"
	"io/fs"
)

// ErrNotEmpty reports an install directory that already holds files; an installed release root is never mixed or replaced.
var ErrNotEmpty = errors.New("install directory is not empty")

//go:embed CREDITS.md
var release embed.FS

// Release returns the embedded root: CREDITS.md and one directory per skill, with exact bytes and LF line endings.
func Release() fs.FS { return release }

// Install writes the release root into dir, which must not exist or must be empty (ErrNotEmpty).
// Every file is written byte for byte; files under a skill's scripts directory are written executable (0755)
// and every other file 0644, so a skill's tree digest is the one Release holds.
// On error nothing Install wrote is left behind.
func Install(dir string) error {
	return errors.New("skills: install not implemented")
}
