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
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// ErrNotEmpty reports an install directory that already holds files; an installed release root is never mixed or replaced.
var ErrNotEmpty = errors.New("install directory is not empty")

//go:embed CREDITS.md guildhall-*
var release embed.FS

// Release returns the embedded root: CREDITS.md and one directory per skill, with exact bytes and LF line endings.
func Release() fs.FS { return release }

// Install writes the release root into dir, which must not exist or must be empty (ErrNotEmpty).
// Every file is written byte for byte; files under a skill's scripts directory are written executable (0755)
// and every other file 0644, so a skill's tree digest is the one Release holds.
// On error nothing Install wrote is left behind.
func Install(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("skills: %w", err)
	case len(entries) > 0:
		return fmt.Errorf("skills: %s: %w", dir, ErrNotEmpty)
	}
	if err := install(dir); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("skills: install %s: %w", dir, err)
	}
	return nil
}

func install(dir string) error {
	return fs.WalkDir(release, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := release.ReadFile(p)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if path.Base(path.Dir(p)) == "scripts" {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
}
