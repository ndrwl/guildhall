package cli

import (
	"flag"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// copyEnvVar selects a Director copy when --copy is absent (docs/running.md#copies).
const copyEnvVar = "GUILDHALL_COPY"

// copyFlags is the --copy flag every store-taking command shares (docs/running.md#copies). A copy directory holds
// config.toml, an optional managed.toml, director.store and state/.
type copyFlags struct{ dir string }

func (c *copyFlags) register(fl *flag.FlagSet) {
	fl.StringVar(&c.dir, "copy", "", "Director copy directory")
}

// apply selects the copy, --copy or else GUILDHALL_COPY from getenv (nil is an empty environment), and defaults
// each empty path it is given to that copy's director.store, config.toml or state; a nil path is a flag the
// command does not take. With --copy, a path already given must name the copy's own file; without it, any path
// given overrides GUILDHALL_COPY entirely. The copy selected, if any, is left in c.dir; with no copy it changes nothing.
func (c *copyFlags) apply(getenv func(string) string, store, config, state *string) error {
	dir := c.dir
	if dir == "" && getenv != nil && !anyGiven(store, config, state) {
		// An inherited copy must not change meaning with a child's working directory.
		if dir = getenv(copyEnvVar); dir != "" && !filepath.IsAbs(dir) {
			return fmt.Errorf("%s %q must be an absolute path", copyEnvVar, dir)
		}
	}
	if dir == "" {
		return nil
	}
	c.dir = dir
	for _, p := range []struct {
		flag, name string
		value      *string
	}{
		{"store", "director.store", store},
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
	return nil
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

// noStore is the refusal for a store path with no file: store init makes one.
func noStore(path string) error {
	return fmt.Errorf("no store at %s; create one with guildhall store init %s", path, path)
}
