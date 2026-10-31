// Package copydir is copy discovery and the copy marker (docs/running.md#copies), shared by the CLI and the PATH
// launcher so the two cannot disagree on which copy a command selects (docs/releases.md#the-path-launcher).
package copydir

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// MarkerFile is the copy marker, the file at a copy's root that copy discovery looks for.
const MarkerFile = ".guildhall-copy"

// Marker is the copy marker's one JSON object.
type Marker struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	Name    string `json:"name"`
}

// The copy marker's schema and version.
const (
	MarkerSchema  = "guildhall.copy"
	MarkerVersion = 1
)

// Discover is copy discovery: it searches upward from the working directory getwd returns, one parent at a time to
// the filesystem root, for the nearest directory holding a copy marker, and returns it with ok true. A start under
// that copy's state/ finds no copy, and so does a nil getwd; with no copy found, the directory returned is the
// start. A marker that cannot be read is an error naming it.
func Discover(getwd func() (string, error)) (dir string, ok bool, err error) {
	if getwd == nil {
		return "", false, nil
	}
	wd, err := getwd()
	if err != nil {
		return "", false, fmt.Errorf("copy discovery: %w", err)
	}
	start, err := filepath.Abs(wd)
	if err != nil {
		return "", false, fmt.Errorf("copy discovery: %w", err)
	}
	for d := start; ; {
		path := filepath.Join(d, MarkerFile)
		switch _, err := os.Lstat(path); {
		case err == nil:
			// Artisan working and attempt directories lie under the copy's state/, and must not reach its store.
			if Within(filepath.Join(d, "state"), start) {
				return start, false, nil
			}
			if err := ReadMarker(path); err != nil {
				return "", false, err
			}
			return d, true, nil
		case !errors.Is(err, fs.ErrNotExist):
			return "", false, fmt.Errorf("copy marker %s: %w", path, err)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return start, false, nil
		}
		d = parent
	}
}

// ReadMarker refuses a marker that is unreadable, is not JSON, or has an unknown schema or version.
func ReadMarker(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("copy marker %s: %w", path, err)
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("copy marker %s is not valid JSON: %w", path, err)
	}
	if m.Schema != MarkerSchema || m.Version != MarkerVersion {
		return fmt.Errorf("copy marker %s has schema %q version %d, want %q version %d", path, m.Schema, m.Version, MarkerSchema, MarkerVersion)
	}
	return nil
}

// IsCopy reports whether dir holds a readable copy marker.
func IsCopy(dir string) bool { return ReadMarker(filepath.Join(dir, MarkerFile)) == nil }

// SamePath reports whether a and b name the same path, compared absolute and cleaned, ignoring case on Windows.
func SamePath(a, b string) bool {
	a, errA := filepath.Abs(a)
	b, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Within reports whether p is base or lies under it, compared as SamePath compares.
func Within(base, p string) bool {
	base, errB := filepath.Abs(base)
	p, errP := filepath.Abs(p)
	if errB != nil || errP != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		base, p = strings.ToLower(base), strings.ToLower(p)
	}
	return p == base || strings.HasPrefix(p, strings.TrimSuffix(base, string(filepath.Separator))+string(filepath.Separator))
}
