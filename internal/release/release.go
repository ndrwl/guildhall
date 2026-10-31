// Package release owns the Guildhall home: the releases installed side by side under it, each verified against its
// sha256 before it may run, the inventory of copies that use them, and the home lock that serializes changes to
// them (docs/releases.md).
//
// The home is laid out as
//
//	<home>/
//	  bin/                     the PATH launcher (a later slice installs it)
//	  releases/<version>/      one installed release, written once and never changed:
//	    guildhall              the binary (guildhall.exe on Windows)
//	    SHA256                 its verified sha256, lowercase hex and a newline
//	    release.json           what it reported of itself at install, and when it was installed
//	  releases/.incoming/      copies not yet verified; never run as a release
//	  copies                   the inventory: one absolute copy directory per line
//	  lock                     the home lock file
//
// See docs/contracts/release.md.
package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/atomicfile"
)

// HomeEnv names the Guildhall home when set; it wins over every other way of finding it.
const HomeEnv = "GUILDHALL_HOME"

// Contract is the release contract this build implements, which `guildhall version --json` reports as
// release_contract and install records: 1 is the release home, install and status.
const Contract = 1

// Names inside the home and inside a release directory.
const (
	binDir       = "bin"
	releasesDir  = "releases"
	incomingDir  = ".incoming"
	copiesFile   = "copies"
	lockFile     = "lock"
	sha256File   = "SHA256"
	metadataFile = "release.json"
)

// metadataSchema and metadataVersion identify release.json.
const (
	metadataSchema  = "guildhall.release"
	metadataVersion = 1
)

// ErrNoHome is returned by Locate when no home can be found: no GUILDHALL_HOME, the binary is not an installed
// release, and the platform default's variables are unset.
var ErrNoHome = errors.New("no Guildhall home")

// BinaryName is the file name of the guildhall binary on goos: guildhall.exe on Windows, guildhall elsewhere.
func BinaryName(goos string) string {
	if goos == "windows" {
		return "guildhall.exe"
	}
	return "guildhall"
}

// Home is a Guildhall home directory, absolute, and the platform whose binary name its releases use.
type Home struct {
	Dir  string
	GOOS string
}

// Locate finds the home: GUILDHALL_HOME when set, made absolute; else, when exe, the running binary's path, is
// <home>/releases/<version>/<binary> for the running version, that <home>; else the platform default,
// %LOCALAPPDATA%\Guildhall on Windows and, elsewhere, $XDG_DATA_HOME/guildhall when that is absolute, else
// $HOME/.local/share/guildhall. getenv reads the environment; exe may be "".
func Locate(getenv func(string) string, exe, version, goos string) (Home, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if dir := getenv(HomeEnv); dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return Home{}, fmt.Errorf("%s %s: %w", HomeEnv, dir, err)
		}
		return Home{Dir: abs, GOOS: goos}, nil
	}
	if dir, ok := homeOfRelease(exe, version, goos); ok {
		return Home{Dir: dir, GOOS: goos}, nil
	}
	if goos == "windows" {
		if local := getenv("LOCALAPPDATA"); local != "" && filepath.IsAbs(local) {
			return Home{Dir: filepath.Join(local, "Guildhall"), GOOS: goos}, nil
		}
		return Home{}, fmt.Errorf("%w: set %s or LOCALAPPDATA", ErrNoHome, HomeEnv)
	}
	if data := getenv("XDG_DATA_HOME"); data != "" && filepath.IsAbs(data) {
		return Home{Dir: filepath.Join(data, "guildhall"), GOOS: goos}, nil
	}
	if home := getenv("HOME"); home != "" && filepath.IsAbs(home) {
		return Home{Dir: filepath.Join(home, ".local", "share", "guildhall"), GOOS: goos}, nil
	}
	return Home{}, fmt.Errorf("%w: set %s, XDG_DATA_HOME or HOME", ErrNoHome, HomeEnv)
}

// homeOfRelease is the home exe is installed under, when exe is <home>/releases/<version>/<binary>.
func homeOfRelease(exe, version, goos string) (string, bool) {
	if exe == "" || version == "" {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", false
	}
	dir := filepath.Dir(abs)
	rel := filepath.Dir(dir)
	if filepath.Base(abs) != BinaryName(goos) || filepath.Base(dir) != version || filepath.Base(rel) != releasesDir {
		return "", false
	}
	return filepath.Dir(rel), true
}

// BinDir is where the PATH launcher lives.
func (h Home) BinDir() string { return filepath.Join(h.Dir, binDir) }

// ReleasesDir holds one directory per installed release.
func (h Home) ReleasesDir() string { return filepath.Join(h.Dir, releasesDir) }

// IncomingDir holds copies not yet verified.
func (h Home) IncomingDir() string { return filepath.Join(h.Dir, releasesDir, incomingDir) }

// CopiesFile is the inventory of copy directories.
func (h Home) CopiesFile() string { return filepath.Join(h.Dir, copiesFile) }

// LockFile is the home lock's file.
func (h Home) LockFile() string { return filepath.Join(h.Dir, lockFile) }

// ReleaseDir is the directory of release version.
func (h Home) ReleaseDir(version string) string { return filepath.Join(h.ReleasesDir(), version) }

// Binary is the path of release version's binary.
func (h Home) Binary(version string) string {
	return filepath.Join(h.ReleaseDir(version), BinaryName(h.GOOS))
}

// versionName is a release version as a directory may be named: a v, then letters, digits, '.', '-' and '+'.
var versionName = regexp.MustCompile(`^v[0-9A-Za-z][0-9A-Za-z.+-]*$`)

// ValidVersion refuses a version that cannot be a release: dev, the version of a plain go build, which two
// different builds share; and anything that is not a v followed by letters, digits, '.', '-' and '+', so a
// version is always one plain directory name.
func ValidVersion(version string) error {
	switch {
	case version == "dev":
		return errors.New(`version "dev" is a plain go build's and is never a release; install a tagged or pseudo-version build`)
	case !versionName.MatchString(version) || strings.Contains(version, ".."):
		return fmt.Errorf("version %q is not a release version (v, then letters, digits, '.', '-' and '+')", version)
	}
	return nil
}

// sha256Hex matches a sha256 written as 64 hex digits.
var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// NormalizeSHA256 is sum in lowercase, refusing anything that is not 64 hex digits.
func NormalizeSHA256(sum string) (string, error) {
	if !sha256Hex.MatchString(sum) {
		return "", fmt.Errorf("sha256 %q is not 64 hex digits", sum)
	}
	return strings.ToLower(sum), nil
}

// Report is what a binary says of itself in `guildhall version --json`, the fields install checks and records.
type Report struct {
	Version         string `json:"version"`
	Modified        bool   `json:"modified"`
	StoreSchema     int    `json:"store_schema"`
	ReleaseContract int    `json:"release_contract"`
}

// Probe runs the binary at path as `guildhall version --json` and returns its report.
type Probe func(ctx context.Context, path string) (Report, error)

// Release is one directory under releases/ as Check found it.
type Release struct {
	Version         string    `json:"version"`
	SHA256          string    `json:"sha256"`
	StoreSchema     int       `json:"store_schema"`
	ReleaseContract int       `json:"release_contract"`
	InstalledAt     time.Time `json:"installed_at"`
	// Damage says why the release must never run; "" for an intact one.
	Damage string `json:"damage"`
}

// metadata is release.json.
type metadata struct {
	Schema          string    `json:"schema"`
	Version         int       `json:"version"`
	Release         string    `json:"release"`
	SHA256          string    `json:"sha256"`
	StoreSchema     int       `json:"store_schema"`
	ReleaseContract int       `json:"release_contract"`
	InstalledAt     time.Time `json:"installed_at"`
}

// Check reads release version's directory and verifies it: its binary must hash to its SHA256 file, and its
// release.json must name the same version and sha256. Anything else, including a missing piece, is Damage.
// fs.ErrNotExist is returned when no directory of that name exists.
func (h Home) Check(version string) (Release, error) {
	dir := h.ReleaseDir(version)
	info, err := os.Lstat(dir)
	if err != nil {
		return Release{}, err
	}
	r := Release{Version: version}
	damaged := func(format string, args ...any) (Release, error) {
		r.Damage = fmt.Sprintf(format, args...)
		return r, nil
	}
	if !info.IsDir() {
		return damaged("%s is not a directory", dir)
	}
	if err := ValidVersion(version); err != nil {
		return damaged("%v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, sha256File))
	if err != nil {
		return damaged("%s: %v", sha256File, err)
	}
	want, err := NormalizeSHA256(strings.TrimSpace(string(b)))
	if err != nil {
		return damaged("%s: %v", sha256File, err)
	}
	r.SHA256 = want
	if b, err = os.ReadFile(filepath.Join(dir, metadataFile)); err != nil {
		return damaged("%s: %v", metadataFile, err)
	}
	var m metadata
	if err := json.Unmarshal(b, &m); err != nil {
		return damaged("%s: %v", metadataFile, err)
	}
	r.StoreSchema, r.ReleaseContract, r.InstalledAt = m.StoreSchema, m.ReleaseContract, m.InstalledAt
	switch {
	case m.Schema != metadataSchema || m.Version != metadataVersion:
		return damaged("%s: schema %q version %d, want %q version %d", metadataFile, m.Schema, m.Version, metadataSchema, metadataVersion)
	case m.Release != version:
		return damaged("%s names release %q", metadataFile, m.Release)
	case m.SHA256 != want:
		return damaged("%s names sha256 %s, %s names %s", metadataFile, m.SHA256, sha256File, want)
	}
	got, err := FileSHA256(h.Binary(version))
	if err != nil {
		return damaged("binary: %v", err)
	}
	if got != want {
		return damaged("binary hashes to %s, %s names %s", got, sha256File, want)
	}
	return r, nil
}

// List checks every release directory, newest install first, then by version; a directory whose release.json
// cannot be read sorts last. The hidden .incoming directory and other dot entries are not releases and are left
// out. With no releases directory the list is empty.
func (h Home) List() ([]Release, error) {
	entries, err := os.ReadDir(h.ReleasesDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Release
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		r, err := h.Check(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].InstalledAt.Equal(out[j].InstalledAt) {
			return out[i].InstalledAt.After(out[j].InstalledAt)
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

// InstallResult is what Install did: the release now installed, and Already when it was installed before, with
// the same sha256, so Install changed nothing.
type InstallResult struct {
	Release Release
	Already bool
}

// Install verifies the binary at from and installs it as release version, side by side with the others.
//
// version must be a valid release version and sum the binary's sha256. A release of that version already
// installed intact with that sha256 is reported Already and nothing changes; one with another sha256, or damaged,
// is refused, as a release directory is never changed. Otherwise the binary is copied into a new directory under
// .incoming/ and hashed as it is copied; a sha256 other than sum is refused. Only then is the verified copy run
// through probe, which must report the same version, not modified, and a release contract of at least 1. Its
// SHA256 and release.json are written beside it, and, under the home lock, the directory is renamed into
// releases/<version> in one step. Every refusal leaves releases/ unchanged and removes the .incoming copy.
func (h Home) Install(ctx context.Context, version, from, sum string, probe Probe, now time.Time, lockWait time.Duration) (InstallResult, error) {
	if err := ValidVersion(version); err != nil {
		return InstallResult{}, err
	}
	sum, err := NormalizeSHA256(sum)
	if err != nil {
		return InstallResult{}, err
	}
	if res, done, err := h.installed(version, sum); done || err != nil {
		return res, err
	}
	if err := os.MkdirAll(h.IncomingDir(), 0o755); err != nil {
		return InstallResult{}, err
	}
	tmp, err := os.MkdirTemp(h.IncomingDir(), ".install-"+version+"-")
	if err != nil {
		return InstallResult{}, err
	}
	// Removing the copy after a successful rename finds nothing to remove.
	defer func() { _ = os.RemoveAll(tmp) }()
	bin := filepath.Join(tmp, BinaryName(h.GOOS))
	got, err := copyHashing(from, bin)
	if err != nil {
		return InstallResult{}, err
	}
	if got != sum {
		return InstallResult{}, fmt.Errorf("%s hashes to %s, not the expected %s; nothing was installed", from, got, sum)
	}
	report, err := probe(ctx, bin)
	if err != nil {
		return InstallResult{}, fmt.Errorf("%s: version --json: %w", from, err)
	}
	switch {
	case report.Version == "dev":
		return InstallResult{}, fmt.Errorf("%s reports version dev, a plain go build's, which is never a release; install a tagged or pseudo-version build", from)
	case report.Version != version:
		return InstallResult{}, fmt.Errorf("%s reports version %q, not %q", from, report.Version, version)
	case report.Modified:
		return InstallResult{}, fmt.Errorf("%s was built with uncommitted or untracked files (modified), so it is never a release", from)
	case report.ReleaseContract < 1:
		return InstallResult{}, fmt.Errorf("%s reports no release_contract, so it predates release installs", from)
	}
	m := metadata{Schema: metadataSchema, Version: metadataVersion, Release: version, SHA256: sum,
		StoreSchema: report.StoreSchema, ReleaseContract: report.ReleaseContract, InstalledAt: now.UTC().Truncate(time.Second)}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return InstallResult{}, err
	}
	if err := writeFile(filepath.Join(tmp, sha256File), []byte(sum+"\n")); err != nil {
		return InstallResult{}, err
	}
	if err := writeFile(filepath.Join(tmp, metadataFile), append(mb, '\n')); err != nil {
		return InstallResult{}, err
	}
	unlock, err := h.Lock(lockWait)
	if err != nil {
		return InstallResult{}, err
	}
	defer func() { _ = unlock() }()
	if res, done, err := h.installed(version, sum); done || err != nil {
		return res, err
	}
	if err := os.Rename(tmp, h.ReleaseDir(version)); err != nil {
		return InstallResult{}, err
	}
	r, err := h.Check(version)
	if err != nil {
		return InstallResult{}, err
	}
	if r.Damage != "" {
		return InstallResult{}, fmt.Errorf("release %s was installed damaged: %s", version, r.Damage)
	}
	return InstallResult{Release: r}, nil
}

// installed reports, done, an existing release of version: Already when it is intact with sum, else a refusal.
func (h Home) installed(version, sum string) (InstallResult, bool, error) {
	r, err := h.Check(version)
	if errors.Is(err, fs.ErrNotExist) {
		return InstallResult{}, false, nil
	}
	switch {
	case err != nil:
		return InstallResult{}, true, err
	case r.Damage != "":
		return InstallResult{}, true, fmt.Errorf("release %s is already installed at %s but damaged (%s); a release directory is never changed, so remove it by hand and install again", version, h.ReleaseDir(version), r.Damage)
	case r.SHA256 != sum:
		return InstallResult{}, true, fmt.Errorf("release %s is already installed with sha256 %s, not %s; a release directory is never changed", version, r.SHA256, sum)
	}
	return InstallResult{Release: r, Already: true}, true, nil
}

// copyHashing copies the file at from to a new executable file at to and returns the sha256 of what it copied.
func copyHashing(from, to string) (string, error) {
	src, err := os.Open(from)
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()
	if info, err := src.Stat(); err != nil {
		return "", err
	} else if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", from)
	}
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, cerr := io.Copy(io.MultiWriter(dst, h), src)
	if err := errors.Join(cerr, dst.Sync(), dst.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeFile writes a new file at path, synced, read-only to others.
func writeFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	return errors.Join(werr, f.Sync(), f.Close())
}

// FileSHA256 is the lowercase hex sha256 of the file at path.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Copies reads the inventory: the absolute copy directories registered in this home, in file order, which
// Register keeps sorted. A home with no inventory has none.
func (h Home) Copies() ([]string, error) {
	b, err := atomicfile.Read(h.CopiesFile())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for line := range strings.Lines(string(b)) {
		if line = strings.TrimRight(line, "\r\n"); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// Register adds the copy directory dir, made absolute and clean, to the inventory under the home lock, keeping it
// sorted and without duplicates; registering a copy already there changes nothing.
func (h Home) Register(dir string, lockWait time.Duration) error {
	if _, err := inventoryName(dir); err != nil {
		return err
	}
	unlock, err := h.Lock(lockWait)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	return h.RegisterLocked(dir)
}

// inventoryName is dir as the inventory lists it: absolute and clean, with no line break.
func inventoryName(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(abs, "\r\n") {
		return "", fmt.Errorf("copy directory %q contains a line break", abs)
	}
	return abs, nil
}

// RegisterLocked is Register for a caller already holding the home lock, such as a pin change, which registers
// its copy and records the pin under one hold.
func (h Home) RegisterLocked(dir string) error {
	abs, err := inventoryName(dir)
	if err != nil {
		return err
	}
	copies, err := h.Copies()
	if err != nil {
		return err
	}
	for _, c := range copies {
		if c == abs {
			return nil
		}
	}
	copies = append(copies, abs)
	sort.Strings(copies)
	var b bytes.Buffer
	for _, c := range copies {
		b.WriteString(c + "\n")
	}
	return atomicfile.Write(h.CopiesFile(), b.Bytes())
}

// ErrLocked is returned by Lock when another process held the home lock for the whole wait.
var ErrLocked = errors.New("the Guildhall home is locked by another process")

// lockPoll is how often Lock tries again while another process holds the lock.
const lockPoll = 20 * time.Millisecond

// Lock takes the home lock, an exclusive lock on the home's lock file held by this process until unlock, creating
// the home and the file when missing; it waits up to wait for another holder, then returns ErrLocked. The lock is
// released when the process ends, however it ends. It is not reentrant: a second Lock in the same process waits.
func (h Home) Lock(wait time.Duration) (unlock func() error, err error) {
	if err := os.MkdirAll(h.Dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(h.LockFile(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		ok, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("home lock %s: %w", h.LockFile(), err)
		}
		if ok {
			return func() error { return errors.Join(unlockFile(f), f.Close()) }, nil
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%w: %s, waited %s", ErrLocked, h.LockFile(), wait)
		}
		time.Sleep(lockPoll)
	}
}
