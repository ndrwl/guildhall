package release

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultSource is the source repository a release missing from the home is built from when nothing names
// another (docs/releases.md#building-a-missing-release).
const DefaultSource = "https://github.com/ndrwl/guildhall"

// BuildPackage is the package a release is built from, relative to the source repository's root.
const BuildPackage = "./cmd/guildhall"

// buildPrefix names the scratch directory under .incoming/ a build fetches, checks out and builds in, so one a
// crash leaves is removed as stale by PruneIncomingLocked.
const buildPrefix = ".build-"

// ErrNotInSource is returned by a Builder's Resolve for a revision the fetched repository does not have, or names
// ambiguously.
var ErrNotInSource = errors.New("not in the source")

// Builder fetches a source repository and builds a release from it; ExecBuilder runs git and go.
type Builder interface {
	// Fetch makes a new git repository at repo, a directory that does not exist yet, and fetches every branch and
	// tag of source into it.
	Fetch(ctx context.Context, repo, source string) error
	// Resolve returns the full commit id rev names in the repository at repo, or ErrNotInSource when it names
	// none, or more than one.
	Resolve(ctx context.Context, repo, rev string) (string, error)
	// Checkout checks out commit, detached and clean, as the repository at repo's work tree.
	Checkout(ctx context.Context, repo, commit string) error
	// GoVersion is the version of Go that builds in dir, such as go1.27.2.
	GoVersion(ctx context.Context, dir string) (string, error)
	// Build builds pkg in dir, the work tree, into the new file out, outside it.
	Build(ctx context.Context, dir, pkg, out string) error
}

// BuildRequest is a release to build from source and install.
type BuildRequest struct {
	// Version is the release: a tag, which is built at refs/tags/<version>, or a pseudo-version, which is built at
	// the commit it names.
	Version string
	// Source is the git repository to fetch, a URL or a path.
	Source   string
	Builder  Builder
	Probe    Probe
	Now      time.Time
	LockWait time.Duration
	// Starting, when set, is called once the commit is resolved and checked out, before go build runs.
	Starting func(commit, goVersion string)
}

// pseudoVersion matches a Go pseudo-version in its three forms, vX.0.0-yyyymmddhhmmss-abcdef123456,
// vX.Y.Z-pre.0.yyyymmddhhmmss-abcdef123456 and vX.Y.(Z+1)-0.yyyymmddhhmmss-abcdef123456.
var pseudoVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-(?:(?:[0-9A-Za-z-]+\.)*0\.)?[0-9]{14}-([0-9a-f]{12})$`)

// buildRevision is what version is built at: for a pseudo-version, the abbreviated commit it names; else the tag
// refs/tags/<version>. what names it in a refusal.
func buildRevision(version string) (rev, what string, err error) {
	if strings.Contains(version, "+") {
		return "", "", fmt.Errorf("version %s has build metadata, such as a modified build's +dirty, so it is never built from source", version)
	}
	if m := pseudoVersion.FindStringSubmatch(version); m != nil {
		return m[1], "commit " + m[1], nil
	}
	return "refs/tags/" + version, "tag " + version, nil
}

// ValidSource refuses a source that is empty, starts with "-", which git would read as an option, or holds a line
// break.
func ValidSource(source string) error {
	if source == "" || strings.HasPrefix(source, "-") || strings.ContainsAny(source, "\r\n") {
		return fmt.Errorf("source %q is not a git repository URL or path", source)
	}
	return nil
}

// AbsSource is source with a local path made absolute against the working directory, so git, which fetches from a
// scratch repository, and release.json, read later from anywhere, both mean the repository the caller named. A URL
// (scheme://…) and an scp-style host:path remote, which git reads as remote whenever a colon comes before any
// path separator and the path has no drive letter, are returned unchanged.
func AbsSource(source string) (string, error) {
	if err := ValidSource(source); err != nil {
		return "", err
	}
	if !isLocalSource(source) {
		return source, nil
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("source %s: %w", source, err)
	}
	return abs, nil
}

// isLocalSource reports whether git reads source as a local path rather than a URL or an scp-style remote.
func isLocalSource(source string) bool {
	if strings.Contains(source, "://") {
		return false
	}
	colon := strings.IndexByte(source, ':')
	if colon < 0 || filepath.VolumeName(source) != "" {
		return true
	}
	for i := 0; i < colon; i++ {
		if os.IsPathSeparator(source[i]) {
			return true
		}
	}
	return false
}

// Build builds release req.Version from source and installs it, side by side with the others.
//
// A release of that version already installed intact is reported Already, building and changing nothing; a damaged
// one is refused. Otherwise, in a new scratch directory under .incoming/, it fetches req.Source's branches and tags,
// resolves the version's commit, refusing one the source lacks with no fallback, checks it out, and builds
// BuildPackage outside the work tree. The binary and its own sha256 then go through Install, which also requires
// the probe to report the resolved commit as its revision and records the source and commit in release.json.
// A source that is a local path is made absolute first (AbsSource), so a relative one means the caller's directory.
// Every refusal leaves releases/ unchanged and removes the scratch directory.
func (h Home) Build(ctx context.Context, req BuildRequest) (InstallResult, error) {
	if res, ok, err := h.Installed(req.Version); ok || err != nil {
		return res, err
	}
	source, err := AbsSource(req.Source)
	if err != nil {
		return InstallResult{}, err
	}
	req.Source = source
	rev, what, err := buildRevision(req.Version)
	if err != nil {
		return InstallResult{}, err
	}
	if err := os.MkdirAll(h.IncomingDir(), 0o755); err != nil {
		return InstallResult{}, err
	}
	scratch, err := os.MkdirTemp(h.IncomingDir(), buildPrefix+req.Version+"-")
	if err != nil {
		return InstallResult{}, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	repo := filepath.Join(scratch, "src")
	if err := req.Builder.Fetch(ctx, repo, req.Source); err != nil {
		return InstallResult{}, fmt.Errorf("fetching %s: %w; nothing was installed", req.Source, err)
	}
	commit, err := req.Builder.Resolve(ctx, repo, rev)
	switch {
	case errors.Is(err, ErrNotInSource):
		return InstallResult{}, fmt.Errorf("%s is not in %s; nothing was installed", what, req.Source)
	case err != nil:
		return InstallResult{}, fmt.Errorf("resolving %s in %s: %w; nothing was installed", what, req.Source, err)
	case !isCommitID(commit) || (!strings.HasPrefix(rev, "refs/") && !strings.HasPrefix(commit, rev)):
		return InstallResult{}, fmt.Errorf("%s resolved to %q in %s, not a commit it names; nothing was installed", what, commit, req.Source)
	}
	if err := req.Builder.Checkout(ctx, repo, commit); err != nil {
		return InstallResult{}, fmt.Errorf("checking out %s from %s: %w; nothing was installed", commit, req.Source, err)
	}
	goVersion, err := req.Builder.GoVersion(ctx, repo)
	if err != nil {
		return InstallResult{}, fmt.Errorf("go version: %w; nothing was installed", err)
	}
	if req.Starting != nil {
		req.Starting(commit, goVersion)
	}
	outDir := filepath.Join(scratch, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		return InstallResult{}, err
	}
	out := filepath.Join(outDir, BinaryName(h.GOOS))
	if err := req.Builder.Build(ctx, repo, BuildPackage, out); err != nil {
		return InstallResult{}, fmt.Errorf("building %s from %s at %s: %w; nothing was installed", req.Version, req.Source, commit, err)
	}
	sum, err := FileSHA256(out)
	if err != nil {
		return InstallResult{}, err
	}
	label := fmt.Sprintf("the build of %s at %s", req.Source, commit)
	return h.install(ctx, req.Version, out, label, sum, req.Probe, req.Now, req.LockWait, &Origin{Source: req.Source, Commit: commit})
}

// isCommitID reports whether s is a full commit id: 40 or 64 lowercase hex digits.
func isCommitID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
