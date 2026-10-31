package release

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/atomicfile"
)

// PinFile is a copy's release pin, the version its new work and Director commands run, one line in the copy
// directory: a cache of the copy's newest release-pin store event, written after it (docs/releases.md#the-pin).
const PinFile = "release.pin"

// Env is the variable the PATH launcher sets to the release it selected, replacing any inherited value; a release
// refuses to run when it names another version (docs/releases.md#the-path-launcher).
const Env = "GUILDHALL_RELEASE"

// LauncherProbeEnv, set to "1", makes the PATH launcher print its LauncherReport instead of dispatching, so
// install and adopt can tell a launcher from a release; the launcher never passes it on.
const LauncherProbeEnv = "GUILDHALL_LAUNCHER_PROBE"

// LauncherReport is what a launcher prints under LauncherProbeEnv.
type LauncherReport struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
}

// The LauncherReport's schema and version.
const (
	LauncherSchema  = "guildhall.launcher"
	LauncherVersion = 1
)

// LauncherName is the PATH launcher's own build name, guildhall-launcher (.exe on Windows), which `go install
// …/cmd/guildhall-launcher` gives it before install renames it to guildhall.
func LauncherName(goos string) string {
	if goos == "windows" {
		return "guildhall-launcher.exe"
	}
	return "guildhall-launcher"
}

// Launcher is the path of the PATH launcher, bin/guildhall (guildhall.exe on Windows).
func (h Home) Launcher() string { return filepath.Join(h.BinDir(), BinaryName(h.GOOS)) }

// PinPath is the release pin of the copy in dir.
func PinPath(dir string) string { return filepath.Join(dir, PinFile) }

// ReadPin reads the release pin of the copy in dir: its version, refused when it is not one line holding a valid
// release version. fs.ErrNotExist is returned when the copy has no pin, as a copy that has not adopted.
func ReadPin(dir string) (string, error) {
	b, err := atomicfile.Read(PinPath(dir))
	if err != nil {
		return "", err
	}
	v := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if err := ValidVersion(v); err != nil {
		return "", fmt.Errorf("%s: %w", PinPath(dir), err)
	}
	return v, nil
}

// HasPin reports whether the copy in dir has a release pin file, readable or not: a copy with one has adopted.
func HasPin(dir string) bool {
	_, err := ReadPin(dir)
	return !errors.Is(err, fs.ErrNotExist)
}

// WritePin writes version as the release pin of the copy in dir, atomically by rename.
func WritePin(dir, version string) error {
	if err := ValidVersion(version); err != nil {
		return err
	}
	return atomicfile.Write(PinPath(dir), []byte(version+"\n"))
}

// Newest is the newest intact installed release by install time, for recovery dispatch; ok is false when none is.
func (h Home) Newest() (Release, bool, error) {
	all, err := h.List()
	if err != nil {
		return Release{}, false, err
	}
	for _, r := range all {
		if r.Damage == "" {
			return r, true, nil
		}
	}
	return Release{}, false, nil
}

// Runnable checks release version for running: its path when it is installed and intact, else an error saying
// it is missing or damaged, naming the path checked.
func (h Home) Runnable(version string) (Release, error) {
	r, err := h.Check(version)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Release{}, fmt.Errorf("release %s is not installed: %s does not exist", version, h.Binary(version))
	case err != nil:
		return Release{}, fmt.Errorf("release %s: %w", version, err)
	case r.Damage != "":
		return Release{}, fmt.Errorf("release %s at %s is damaged and never run: %s", version, h.ReleaseDir(version), r.Damage)
	}
	return r, nil
}
