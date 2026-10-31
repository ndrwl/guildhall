package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// copyMarkerFile is the copy marker, the file at a copy's root that copy discovery looks for (docs/running.md#copies).
const copyMarkerFile = ".guildhall-copy"

// copyMarker is the copy marker's one JSON object.
type copyMarker struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	Name    string `json:"name"`
}

const (
	copyMarkerSchema  = "guildhall.copy"
	copyMarkerVersion = 1
)

// copyFlags is the --copy flag every store-taking command shares (docs/running.md#copies). A copy directory holds
// its copy marker, config.toml, an optional managed.toml, its store, director.store or, for the Steward,
// steward.store (copyStoreFile), and state/.
type copyFlags struct {
	dir string
	// searched is set when copy discovery ran and found no copy, and from is the directory it started from.
	searched bool
	from     string
}

func (c *copyFlags) register(fl *flag.FlagSet) {
	fl.StringVar(&c.dir, "copy", "", "Director copy directory")
}

// apply selects the copy, --copy or else copy discovery from a.Getwd, and defaults each empty path it is given to
// that copy's store (copyStoreFile), config.toml or state; a nil path is a flag the command does not take. With --copy, a
// path already given must name the copy's own file, and a copy with no marker gets one; without it, any path given
// bypasses copy selection entirely, so discovery does not run. The copy selected, if any, is left in c.dir; with no
// copy it changes nothing, and missing is the refusal.
func (c *copyFlags) apply(a *App, store, config, state *string) error {
	dir := c.dir
	if dir == "" {
		if anyGiven(store, config, state) {
			return nil
		}
		found, ok, err := discoverCopy(a.Getwd)
		if err != nil {
			return err
		}
		if !ok {
			c.searched, c.from = true, found
			return nil
		}
		dir = found
	}
	for _, p := range []struct {
		flag, name string
		value      *string
	}{
		{"store", copyStoreFile(dir), store},
		{"config", "config.toml", config},
		{"state", "state", state},
	} {
		if p.value == nil {
			continue
		}
		want := filepath.Join(dir, p.name)
		if *p.value == "" {
			*p.value = want
		} else if !samePath(*p.value, want) {
			return fmt.Errorf("--%s %s is not the copy's %s", p.flag, *p.value, want)
		}
	}
	if c.dir != "" {
		writeCopyMarker(a.Stderr, c.dir)
	}
	c.dir = dir
	return nil
}

// copyStoreFile is the name of the store of the copy in dir: steward.store for a Steward copy, one that holds a
// steward.store and no director.store, and director.store for every other copy, so a Director copy, and a new copy
// with no store yet, is read exactly as before (docs/steward.md#the-steward-session).
func copyStoreFile(dir string) string {
	_, director := os.Lstat(filepath.Join(dir, directorRole.storeFile))
	if _, steward := os.Lstat(filepath.Join(dir, stewardStoreFile)); steward == nil && errors.Is(director, fs.ErrNotExist) {
		return stewardStoreFile
	}
	return directorRole.storeFile
}

// copyStore is the path of the store of the copy in dir (copyStoreFile).
func copyStore(dir string) string { return filepath.Join(dir, copyStoreFile(dir)) }

// missing is the one refusal for a command that found no copy (docs/running.md#copies).
func (c *copyFlags) missing(a *App) error {
	from := c.from
	if from == "" && a.Getwd != nil {
		if wd, err := a.Getwd(); err == nil {
			from = wd
		}
	}
	if from == "" {
		from = "the current directory"
	}
	return fmt.Errorf("no Guildhall copy found from %s; pass --copy <dir> or cd into a copy", from)
}

// discoverCopy is copy discovery: it searches upward from the working directory getwd returns, one parent at a time
// to the filesystem root, for the nearest directory holding a copy marker, and returns it with ok true. A start
// under that copy's state/ finds no copy, and so does a nil getwd; with no copy found, the directory returned is
// the start. A marker that cannot be read is an error naming it.
func discoverCopy(getwd func() (string, error)) (dir string, ok bool, err error) {
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
		path := filepath.Join(d, copyMarkerFile)
		switch _, err := os.Lstat(path); {
		case err == nil:
			// Artisan working and attempt directories lie under the copy's state/, and must not reach its store.
			if within(filepath.Join(d, "state"), start) {
				return start, false, nil
			}
			if err := readCopyMarker(path); err != nil {
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

// readCopyMarker refuses a marker that is unreadable, is not JSON, or has an unknown schema or version.
func readCopyMarker(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("copy marker %s: %w", path, err)
	}
	var m copyMarker
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("copy marker %s is not valid JSON: %w", path, err)
	}
	if m.Schema != copyMarkerSchema || m.Version != copyMarkerVersion {
		return fmt.Errorf("copy marker %s has schema %q version %d, want %q version %d", path, m.Schema, m.Version, copyMarkerSchema, copyMarkerVersion)
	}
	return nil
}

// writeCopyMarker gives dir a copy marker named for its base name unless it has one. The marker is created
// exclusively, so concurrent first uses cannot race and an existing marker is never rewritten. A copy that cannot
// be written gets one warning line on stderr, so a read-only command still runs.
func writeCopyMarker(stderr io.Writer, dir string) {
	abs, err := filepath.Abs(dir)
	if err == nil {
		var b []byte
		if b, err = json.Marshal(copyMarker{Schema: copyMarkerSchema, Version: copyMarkerVersion, Name: filepath.Base(abs)}); err == nil {
			err = createExclusive(filepath.Join(abs, copyMarkerFile), append(b, '\n'))
		}
	}
	if err != nil && !errors.Is(err, fs.ErrExist) && stderr != nil {
		_, _ = fmt.Fprintf(stderr, "guildhall: warning: cannot write the copy marker in %s: %v\n", dir, err)
	}
}

// createExclusive creates path holding data, failing with fs.ErrExist when it is already there.
func createExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

// anyGiven reports whether any path the command takes was given explicitly.
func anyGiven(paths ...*string) bool {
	for _, p := range paths {
		if p != nil && *p != "" {
			return true
		}
	}
	return false
}

// samePath reports whether a and b name the same path, compared absolute and cleaned, ignoring case on Windows.
func samePath(a, b string) bool {
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

// within reports whether p is base or lies under it, compared as samePath compares.
func within(base, p string) bool {
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

// noStore is the refusal for a store path with no file: store init makes one.
func noStore(path string) error {
	return fmt.Errorf("no store at %s; create one with guildhall store init %s", path, path)
}
