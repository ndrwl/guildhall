package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/release"
)

const (
	buildVersion = "v0.0.0-20261101120000-0123456789ab"
	buildCommit  = "0123456789abcdef0123456789abcdef01234567"
	buildReport  = `{"version":"` + buildVersion + `","revision":"` + buildCommit + `","modified":false,"store_schema":1,"release_contract":2}`
)

// fakeReleaseBuilder is a release.Builder whose sources each hold the commits listed for them, recording the
// sources it fetched and the builds it ran.
type fakeReleaseBuilder struct {
	sources  map[string][]string
	buildErr error
	fetched  []string
	built    int
	source   string
}

func (f *fakeReleaseBuilder) Fetch(_ context.Context, repo, source string) error {
	f.fetched = append(f.fetched, source)
	f.source = source
	return os.MkdirAll(repo, 0o755)
}

func (f *fakeReleaseBuilder) Resolve(_ context.Context, _, rev string) (string, error) {
	for _, c := range f.sources[f.source] {
		if strings.HasPrefix(c, rev) {
			return c, nil
		}
	}
	return "", release.ErrNotInSource
}

func (f *fakeReleaseBuilder) Checkout(context.Context, string, string) error { return nil }

func (f *fakeReleaseBuilder) GoVersion(context.Context, string) (string, error) {
	return "go1.27.2", nil
}

func (f *fakeReleaseBuilder) Build(_ context.Context, _, _, out string) error {
	f.built++
	if f.buildErr != nil {
		return f.buildErr
	}
	return os.WriteFile(out, []byte("built"), 0o755)
}

func buildTestApp(t *testing.T, report string) (*App, *fakeReleaseBuilder, string, *strings.Builder, *strings.Builder) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	var ran []string
	app, stdout, stderr := releaseTestApp(home, report, &ran)
	f := &fakeReleaseBuilder{sources: map[string][]string{release.DefaultSource: {buildCommit}, "https://example.com/fork/guildhall": {buildCommit}}}
	app.ReleaseBuilder = f
	return app, f, home, stdout, stderr
}

// releasesEmpty fails unless releases/ holds nothing but an empty .incoming.
func releasesEmpty(t *testing.T, home string) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(home, "releases"))
	for _, e := range entries {
		if e.Name() != ".incoming" {
			t.Errorf("releases/ holds %s", e.Name())
		}
	}
	if left, _ := os.ReadDir(filepath.Join(home, "releases", ".incoming")); len(left) > 0 {
		t.Errorf(".incoming holds %v", left)
	}
}

func TestReleaseInstallBuildsAMissingRelease(t *testing.T) {
	for _, c := range []struct {
		name   string
		args   []string
		source string
	}{
		{"from the default source", nil, release.DefaultSource},
		{"from --source", []string{"--source", "https://example.com/fork/guildhall"}, "https://example.com/fork/guildhall"},
	} {
		t.Run(c.name, func(t *testing.T) {
			app, f, home, stdout, stderr := buildTestApp(t, buildReport)
			if code := app.Run(append([]string{"release", "install", buildVersion}, c.args...)); code != 0 {
				t.Fatalf("release install = %d; stderr %s", code, stderr)
			}
			r, err := (release.Home{Dir: home, GOOS: runtime.GOOS}).Check(buildVersion)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, "releases", buildVersion)
			want := "building " + buildVersion + " from " + c.source + " at " + buildCommit + " with go1.27.2\n" +
				"installed " + buildVersion + " (sha256 " + r.SHA256 + ") at " + dir + ", built from " + c.source + " at " + buildCommit + "\n"
			if stdout.String() != want {
				t.Errorf("release install printed\n%s\nwant\n%s", stdout, want)
			}
			if r.Source != c.source || r.Commit != buildCommit || len(f.fetched) != 1 || f.fetched[0] != c.source {
				t.Errorf("release = %+v, fetched %q; want built from %s", r, f.fetched, c.source)
			}

			// Installed, it builds nothing again, with or without --no-build.
			for _, extra := range [][]string{nil, {"--no-build"}} {
				stdout.Reset()
				if code := app.Run(append([]string{"release", "install", buildVersion}, extra...)); code != 0 ||
					!strings.HasSuffix(stdout.String(), "; nothing changed\n") || f.built != 1 {
					t.Errorf("release install again %q = %d, %q, built %d; want already installed", extra, code, stdout, f.built)
				}
			}
		})
	}
}

// A relative --source names a path from the caller's directory, so it is fetched, printed and recorded absolute.
func TestReleaseInstallRelativeSource(t *testing.T) {
	app, f, home, stdout, stderr := buildTestApp(t, buildReport)
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(filepath.Dir(wd), "guildhall")
	f.sources[abs] = []string{buildCommit}
	if code := app.Run([]string{"release", "install", buildVersion, "--source", filepath.Join("..", "guildhall")}); code != 0 {
		t.Fatalf("release install = %d; stderr %s", code, stderr)
	}
	r, err := (release.Home{Dir: home, GOOS: runtime.GOOS}).Check(buildVersion)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != abs || len(f.fetched) != 1 || f.fetched[0] != abs || !strings.HasPrefix(stdout.String(), "building "+buildVersion+" from "+abs+" at ") {
		t.Errorf("release = %+v, fetched %q, printed %q; want %s throughout", r, f.fetched, stdout, abs)
	}
}

// sourceCopy is a marked Director copy whose [local] gives local as its settings and, when managed is not "", whose
// managed.toml sets release_source to it, overridable.
func sourceCopy(t *testing.T, local, managed string) string {
	t.Helper()
	dir := filepath.Dir(writeDirector(t, strings.Replace(directorTOML, "[local]\n", "[local]\n"+local, 1)))
	if managed != "" {
		data, err := config.EncodeManaged(map[string]config.ManagedSetting{config.ReleaseSourceKey: {Value: managed, Overridable: true}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "managed.toml"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return markCopy(t, dir)
}

// TestReleaseInstallSourcePrecedence: the source is --source, else the copy's release_source, local over managed,
// by --copy or discovery, else release.DefaultSource, also with no copy found.
func TestReleaseInstallSourcePrecedence(t *testing.T) {
	const fork, mirror, flagged = "https://example.com/fork/guildhall", "https://example.com/mirror/guildhall", "https://example.com/flag/guildhall"
	local := "release_source = \"" + fork + "\"\n"
	for _, c := range []struct {
		name         string
		local, mngd  string
		args         []string
		discover     bool
		noCopy, want string
	}{
		{name: "no copy found", want: release.DefaultSource},
		{name: "a copy with no setting", discover: true, want: release.DefaultSource},
		{name: "the copy's local setting by discovery", local: local, discover: true, want: fork},
		{name: "the copy's local setting by --copy", local: local, args: []string{"--copy"}, want: fork},
		{name: "the copy's managed setting", mngd: mirror, discover: true, want: mirror},
		{name: "local over managed", local: local, mngd: mirror, discover: true, want: fork},
		{name: "--source over the setting", local: local, discover: true, args: []string{"--source", flagged}, want: flagged},
	} {
		t.Run(c.name, func(t *testing.T) {
			app, f, home, stdout, stderr := buildTestApp(t, buildReport)
			f.sources[fork] = []string{buildCommit}
			f.sources[mirror] = []string{buildCommit}
			f.sources[flagged] = []string{buildCommit}
			wd := t.TempDir()
			args := []string{"release", "install", buildVersion}
			if c.name != "no copy found" {
				dir := sourceCopy(t, c.local, c.mngd)
				if c.discover {
					wd = dir
				}
				for _, a := range c.args {
					args = append(args, a)
					if a == "--copy" {
						args = append(args, dir)
					}
				}
			}
			app.Getwd = func() (string, error) { return wd, nil }
			if code := app.Run(args); code != 0 {
				t.Fatalf("release install %q = %d; stderr %s", args, code, stderr)
			}
			r, err := (release.Home{Dir: home, GOOS: runtime.GOOS}).Check(buildVersion)
			if err != nil {
				t.Fatal(err)
			}
			if r.Source != c.want || len(f.fetched) != 1 || f.fetched[0] != c.want || !strings.HasPrefix(stdout.String(), "building "+buildVersion+" from "+c.want+" at ") {
				t.Errorf("release = %+v, fetched %q, printed %q; want built from %s", r, f.fetched, stdout, c.want)
			}
		})
	}

	// --no-build names the copy's source in the command that would build.
	app, f, _, _, stderr := buildTestApp(t, buildReport)
	dir := sourceCopy(t, local, "")
	app.Getwd = func() (string, error) { return dir, nil }
	if code := app.Run([]string{"release", "install", buildVersion, "--no-build"}); code != 1 || !strings.Contains(stderr.String(), "builds it from "+fork) || len(f.fetched) != 0 {
		t.Errorf("release install --no-build = %d, %q, fetched %q; want a refusal naming %s", code, stderr, f.fetched, fork)
	}
}

// TestReleaseInstallSourceRefusals: a copy whose config cannot be read refuses, naming --source, rather than build
// from the default; --source needs no config; --from takes no --copy.
func TestReleaseInstallSourceRefusals(t *testing.T) {
	broken := sourceCopy(t, "", "")
	if err := os.WriteFile(filepath.Join(broken, "config.toml"), []byte("schema_version = 1\nmode = \"nonsense\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, f, home, _, stderr := buildTestApp(t, buildReport)
	app.Getwd = func() (string, error) { return broken, nil }
	if code := app.Run([]string{"release", "install", buildVersion}); code != 1 || !strings.Contains(stderr.String(), "release_source") ||
		!strings.Contains(stderr.String(), "--source") || len(f.fetched) != 0 {
		t.Errorf("release install from a broken copy = %d, %q, fetched %q; want a refusal naming --source", code, stderr, f.fetched)
	}
	releasesEmpty(t, home)
	if code := app.Run([]string{"release", "install", buildVersion, "--source", release.DefaultSource}); code != 0 || len(f.fetched) != 1 {
		t.Errorf("release install --source from a broken copy = %d, %q, fetched %q; want it built", code, stderr, f.fetched)
	}

	path, sum := releaseFile(t, "binary")
	app, _, _, _, stderr = buildTestApp(t, buildReport)
	if code := app.Run([]string{"release", "install", buildVersion, "--from", path, "--sha256", sum, "--copy", broken}); code != 2 || !strings.Contains(stderr.String(), "--copy") {
		t.Errorf("release install --from --copy = %d, %q; want a usage error", code, stderr)
	}
}

func TestReleaseInstallBuildRefusals(t *testing.T) {
	t.Run("--no-build", func(t *testing.T) {
		app, f, home, stdout, stderr := buildTestApp(t, buildReport)
		code := app.Run([]string{"release", "install", buildVersion, "--no-build"})
		want := "release " + buildVersion + " is not installed, and --no-build builds nothing; guildhall release install " + buildVersion + " builds it from " + release.DefaultSource
		if code != 1 || stdout.String() != "" || !strings.Contains(stderr.String(), want) || len(f.fetched) != 0 {
			t.Errorf("release install --no-build = %d, %q, %q, fetched %q; want a refusal naming the command", code, stdout, stderr, f.fetched)
		}
		releasesEmpty(t, home)
	})
	t.Run("a commit not in the source", func(t *testing.T) {
		app, f, home, _, stderr := buildTestApp(t, buildReport)
		f.sources[release.DefaultSource] = nil
		code := app.Run([]string{"release", "install", buildVersion})
		if code != 1 || !strings.Contains(stderr.String(), "commit 0123456789ab is not in "+release.DefaultSource) || f.built != 0 || len(f.fetched) != 1 {
			t.Errorf("release install = %d, %q, fetched %q; want a refusal naming the source, with no fallback", code, stderr, f.fetched)
		}
		releasesEmpty(t, home)
	})
	t.Run("a failed build", func(t *testing.T) {
		app, f, home, _, stderr := buildTestApp(t, buildReport)
		f.buildErr = errors.New("compile error")
		if code := app.Run([]string{"release", "install", buildVersion}); code != 1 || !strings.Contains(stderr.String(), "compile error") {
			t.Errorf("release install = %d, %q; want the build's failure", code, stderr)
		}
		releasesEmpty(t, home)
	})
	t.Run("a build reporting another revision", func(t *testing.T) {
		app, _, home, _, stderr := buildTestApp(t, strings.Replace(buildReport, buildCommit, strings.Repeat("f", 40), 1))
		if code := app.Run([]string{"release", "install", buildVersion}); code != 1 || !strings.Contains(stderr.String(), "not the commit "+buildCommit) {
			t.Errorf("release install = %d, %q; want a revision refusal", code, stderr)
		}
		releasesEmpty(t, home)
	})
}
