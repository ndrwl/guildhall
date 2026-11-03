package releasetest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/release"
)

// FakeGoVersion is the Go version Fake reports for every build.
const FakeGoVersion = "go1.27.2"

// fakeMagic heads the file a Fake build writes, so Fake.Probe refuses any other.
const fakeMagic = "releasetest.Fake build"

// Fake is the release.Builder double: each source holds the commits listed for it, by the source as Fetch is given
// it, and it records what it ran. It never runs git or go.
//
// Resolve answers against the source last fetched: refs/tags/<v> resolves to the commit whose Version is v, and any
// other revision to the one commit whose id starts with it, else release.ErrNotInSource; Resolved, checked first,
// answers a revision as given. Checkout writes the commit into the work tree, and Build refuses an output inside the
// work tree and writes a file naming the checked-out commit's version and revision, which Fake.Probe reads back.
type Fake struct {
	Sources map[string][]Commit
	// Resolved, when it holds a revision, is the id Resolve returns for it, whatever the source holds.
	Resolved map[string]string
	// FetchErr and BuildErr, when set, are what every Fetch and Build return.
	FetchErr, BuildErr error
	// Steps are the steps run, as "fetch <source>", "resolve <rev>", "checkout <commit>" and "build <pkg>".
	Steps []string
	// Fetched are the sources fetched, in order, and Built the builds run, failed ones included.
	Fetched []string
	Built   int
	source  string
}

// treeFile is the file Fake's Checkout writes in the work tree, holding the commit checked out.
const treeFile = "releasetest-tree"

// Fetch records source as the source to resolve against and makes repo.
func (f *Fake) Fetch(_ context.Context, repo, source string) error {
	f.Steps = append(f.Steps, "fetch "+source)
	f.Fetched = append(f.Fetched, source)
	f.source = source
	if f.FetchErr != nil {
		return f.FetchErr
	}
	return os.MkdirAll(repo, 0o755)
}

// Resolve returns the commit rev names in the last-fetched source.
func (f *Fake) Resolve(_ context.Context, _, rev string) (string, error) {
	f.Steps = append(f.Steps, "resolve "+rev)
	if id, ok := f.Resolved[rev]; ok {
		return id, nil
	}
	var found []string
	for _, c := range f.Sources[f.source] {
		if tag, ok := strings.CutPrefix(rev, "refs/tags/"); ok {
			if c.Version == tag {
				found = append(found, c.ID)
			}
		} else if rev != "" && strings.HasPrefix(c.ID, rev) {
			found = append(found, c.ID)
		}
	}
	if len(found) != 1 {
		return "", release.ErrNotInSource
	}
	return found[0], nil
}

// Checkout writes commit into repo's work tree.
func (f *Fake) Checkout(_ context.Context, repo, commit string) error {
	f.Steps = append(f.Steps, "checkout "+commit)
	return os.WriteFile(filepath.Join(repo, treeFile), []byte(commit), 0o644)
}

// GoVersion is FakeGoVersion.
func (f *Fake) GoVersion(context.Context, string) (string, error) { return FakeGoVersion, nil }

// Build writes out naming the version and revision of the commit checked out in dir, refusing an out inside dir.
func (f *Fake) Build(_ context.Context, dir, pkg, out string) error {
	f.Steps = append(f.Steps, "build "+pkg)
	f.Built++
	if f.BuildErr != nil {
		return f.BuildErr
	}
	if strings.HasPrefix(out, dir+string(filepath.Separator)) {
		return errors.New("output inside the work tree")
	}
	commit, err := os.ReadFile(filepath.Join(dir, treeFile))
	if err != nil {
		return fmt.Errorf("nothing checked out: %w", err)
	}
	version := ""
	for _, c := range f.Sources[f.source] {
		if c.ID == string(commit) {
			version = c.Version
		}
	}
	return os.WriteFile(out, []byte(fakeMagic+"\nversion "+version+"\nrevision "+string(commit)+"\n"), 0o755)
}

// Probe is the release.Probe of a Fake build: the version and revision it names, unmodified, store schema 1 and
// this binary's release contract.
func (f *Fake) Probe(_ context.Context, path string) (release.Report, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return release.Report{}, err
	}
	if !bytes.HasPrefix(b, []byte(fakeMagic+"\n")) {
		return release.Report{}, fmt.Errorf("%s is not a releasetest.Fake build", path)
	}
	r := release.Report{StoreSchema: 1, ReleaseContract: release.Contract}
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "version "); ok {
			r.Version = v
		} else if v, ok := strings.CutPrefix(s.Text(), "revision "); ok {
			r.Revision = v
		}
	}
	return r, nil
}

// Fake commits of the fake harness's source.
const (
	fakeTagged   = "fedcba9876543210fedcba9876543210fedcba98"
	fakeUntagged = "0123456789abcdef0123456789abcdef01234567"
)

// NewFakeHarness is a Fake under the contract, over an empty local directory as its source, holding a tagged and
// an untagged commit.
func NewFakeHarness(t *testing.T) Harness {
	t.Helper()
	source := t.TempDir()
	tagged := Commit{ID: fakeTagged, Version: "v1.5.0"}
	untagged := Commit{ID: fakeUntagged, Version: "v0.0.0-20261101120000-" + fakeUntagged[:12]}
	f := &Fake{Sources: map[string][]Commit{source: {untagged, tagged}}}
	return Harness{Builder: f, Probe: f.Probe, Source: source, Tagged: tagged, Untagged: untagged, Missing: AbsentCommit}
}
