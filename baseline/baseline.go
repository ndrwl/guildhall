// Package baseline is Guildhall's release baseline: the role prompts and stage guides every Director ships with,
// embedded in the binary beside the release skills.
//
// The root holds roles/director.md, roles/artisan.md, roles/judge.md for a delivered task's judge, and one
// stages/<kind>.md for every step kind an Artisan may run.
// It holds no standards: standards are the Guildmaster's, kept in the copy's own baseline/standards.
// A Director copy's baseline/ directory overrides it file by file; see docs/running.md.
package baseline

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ErrNotEmpty reports an install directory that already holds files; an installed release baseline is never mixed or replaced.
var ErrNotEmpty = errors.New("install directory is not empty")

//go:embed roles stages
var release embed.FS

// Release returns the embedded root with exact bytes and LF line endings.
func Release() fs.FS { return release }

// Install writes the release root into dir, which must not exist or must be empty (ErrNotEmpty).
// Every file is written byte for byte with mode 0644; on error nothing Install wrote is left behind.
func Install(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("baseline: %w", err)
	case len(entries) > 0:
		return fmt.Errorf("baseline: %s: %w", dir, ErrNotEmpty)
	}
	if err := os.CopyFS(dir, release); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("baseline: install %s: %w", dir, err)
	}
	return nil
}
