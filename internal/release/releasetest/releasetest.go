// Package releasetest holds the shared release.Builder contract suite, its git fixture and probe, and Fake, the
// Builder double, for tests in other packages (docs/contracts/release-releasetest.md).
package releasetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/release"
)

// Commit is a commit of a harness's source and the version a build of it reports: its tag, or for an untagged
// commit the pseudo-version v0.0.0-<UTC yyyymmddhhmmss>-<ID[:12]>.
type Commit struct {
	ID, Version string
}

// AbsentCommit is a full commit id no fixture source holds, a harness's usual Missing.
const AbsentCommit = "abcdefabcdef0123456789abcdef0123456789ab"

// missingTime is the commit time of the pseudo-version the contract builds Missing as.
const missingTime = "20261101103045"

// Harness is one Builder under the contract: the builder, the probe that reads a binary it built, and its source,
// which holds Tagged and Untagged but not the commit Missing.
type Harness struct {
	Builder  release.Builder
	Probe    release.Probe
	Source   string
	Tagged   Commit
	Untagged Commit
	Missing  string
}

// RunBuilderContract runs the Builder contract against fresh harnesses from newHarness, each case driving
// release.Home.Build on a fresh temporary home.
func RunBuilderContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	build := func(t *testing.T, h Harness, version string) (release.Home, release.InstallResult, error) {
		t.Helper()
		home := release.Home{Dir: filepath.Join(t.TempDir(), "home"), GOOS: runtime.GOOS}
		before := snapshot(t, h.Source)
		res, err := home.Build(ctx, release.BuildRequest{Version: version, Source: h.Source, Builder: h.Builder, Probe: h.Probe, Now: at, LockWait: 10 * time.Second})
		t.Logf("Build(%s) = %+v, %v", version, res, err)
		if diff := changed(before, snapshot(t, h.Source)); len(diff) > 0 {
			t.Errorf("Build(%s) changed the source %s: %q", version, h.Source, diff)
		}
		leftOnly(t, home, version, err == nil)
		return home, res, err
	}
	built := func(t *testing.T, h Harness, c Commit) {
		t.Helper()
		home, res, err := build(t, h, c.Version)
		if err != nil {
			t.Fatalf("Build(%s) = %v", c.Version, err)
		}
		if res.Already || res.Release.Version != c.Version || res.Release.Commit != c.ID || res.Release.Source != h.Source || res.Release.Damage != "" {
			t.Fatalf("Build(%s) = %+v, want a new intact release built from %s at %s", c.Version, res, h.Source, c.ID)
		}
		r, err := h.Probe(ctx, home.Binary(c.Version))
		if err != nil || r.Version != c.Version || r.Revision != c.ID || r.Modified {
			t.Fatalf("installed %s reports %+v, %v; want version %s and revision %s, unmodified", c.Version, r, err, c.Version, c.ID)
		}
	}

	t.Run("a tagged commit builds as its tag", func(t *testing.T) {
		h := newHarness(t)
		built(t, h, h.Tagged)
	})
	t.Run("a commit the source lacks is not in the source", func(t *testing.T) {
		h := newHarness(t)
		version := "v0.0.0-" + missingTime + "-" + h.Missing[:12]
		home, _, err := build(t, h, version)
		if err == nil || !strings.Contains(err.Error(), "commit "+h.Missing[:12]+" is not in "+h.Source) {
			t.Fatalf("Build(%s) = %v, want the commit not in the source", version, err)
		}
		if _, err := os.Stat(home.ReleaseDir(version)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("release %s was installed: %v", version, err)
		}
		// The Builder itself says so with ErrNotInSource.
		repo := filepath.Join(t.TempDir(), "src")
		if err := h.Builder.Fetch(ctx, repo, h.Source); err != nil {
			t.Fatal(err)
		}
		if id, err := h.Builder.Resolve(ctx, repo, h.Missing[:12]); !errors.Is(err, release.ErrNotInSource) {
			t.Fatalf("Resolve(%s) = %q, %v; want ErrNotInSource", h.Missing[:12], id, err)
		}
		if id, err := h.Builder.Resolve(ctx, repo, "refs/tags/v99.0.0"); !errors.Is(err, release.ErrNotInSource) {
			t.Fatalf("Resolve(refs/tags/v99.0.0) = %q, %v; want ErrNotInSource", id, err)
		}
	})
	t.Run("an untagged commit builds as its pseudo-version", func(t *testing.T) {
		h := newHarness(t)
		built(t, h, h.Untagged)
	})
}

// leftOnly fails unless home holds nothing but its lock, an empty .incoming and, when installed, release version.
func leftOnly(t *testing.T, home release.Home, version string, installed bool) {
	t.Helper()
	entries, err := os.ReadDir(home.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if p := filepath.Join(home.Dir, e.Name()); p != home.ReleasesDir() && p != home.LockFile() {
			t.Errorf("the home holds %s", p)
		}
	}
	entries, err = os.ReadDir(home.ReleasesDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch p := filepath.Join(home.ReleasesDir(), e.Name()); {
		case p == home.IncomingDir():
		case installed && p == home.ReleaseDir(version):
		default:
			t.Errorf("releases/ holds %s", p)
		}
	}
	if left, _ := os.ReadDir(home.IncomingDir()); len(left) > 0 {
		t.Errorf(".incoming holds %v, want nothing", left)
	}
}

// snapshot is every file and directory under dir, with its mode and, for a file, the sha256 of its content; nil
// when dir is not a local directory.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil
	}
	got := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		v := info.Mode().String()
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			v += " " + hex.EncodeToString(sum[:])
		}
		got[path] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// changed is each path whose entry differs between snapshots a and b, sorted.
func changed(a, b map[string]string) []string {
	var paths []string
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			paths = append(paths, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			paths = append(paths, k)
		}
	}
	slices.Sort(paths)
	return paths
}
