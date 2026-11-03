package release

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	pseudo     = "v0.0.0-20261101120000-0123456789ab"
	pseudoFull = "0123456789abcdef0123456789abcdef01234567"
	tagFull    = "fedcba9876543210fedcba9876543210fedcba98"
)

// fakeBuilder is a Builder whose source holds commits, by revision; it records each step it ran.
type fakeBuilder struct {
	commits  map[string]string
	fetchErr error
	buildErr error
	steps    []string
}

func (f *fakeBuilder) Fetch(_ context.Context, repo, source string) error {
	f.steps = append(f.steps, "fetch "+source)
	if f.fetchErr != nil {
		return f.fetchErr
	}
	return os.MkdirAll(repo, 0o755)
}

func (f *fakeBuilder) Resolve(_ context.Context, _, rev string) (string, error) {
	f.steps = append(f.steps, "resolve "+rev)
	c, ok := f.commits[rev]
	if !ok {
		return "", ErrNotInSource
	}
	return c, nil
}

func (f *fakeBuilder) Checkout(_ context.Context, repo, commit string) error {
	f.steps = append(f.steps, "checkout "+commit)
	return os.WriteFile(filepath.Join(repo, "tree"), []byte(commit), 0o644)
}

func (f *fakeBuilder) GoVersion(context.Context, string) (string, error) { return "go1.27.2", nil }

func (f *fakeBuilder) Build(_ context.Context, dir, pkg, out string) error {
	f.steps = append(f.steps, "build "+pkg)
	if f.buildErr != nil {
		return f.buildErr
	}
	if strings.HasPrefix(out, dir+string(filepath.Separator)) {
		return errors.New("output inside the work tree")
	}
	return os.WriteFile(out, []byte("built "+out), 0o755)
}

func newFake() *fakeBuilder {
	return &fakeBuilder{commits: map[string]string{"0123456789ab": pseudoFull, "refs/tags/v1.5.0": tagFull}}
}

func buildReq(version string, b Builder, report Report, ran *[]string) BuildRequest {
	return BuildRequest{Version: version, Source: "https://example.com/fork/guildhall", Builder: b, Probe: reports(report, ran), Now: installedAt, LockWait: lockWait}
}

func TestBuild(t *testing.T) {
	for _, c := range []struct{ version, commit string }{{pseudo, pseudoFull}, {"v1.5.0", tagFull}} {
		t.Run(c.version, func(t *testing.T) {
			h := newHome(t)
			f := newFake()
			var ran []string
			report := good(c.version)
			report.Revision = c.commit
			req := buildReq(c.version, f, report, &ran)
			var started []string
			req.Starting = func(commit, goVersion string) { started = append(started, commit+" "+goVersion) }
			res, err := h.Build(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if res.Already || res.Release.Source != req.Source || res.Release.Commit != c.commit || res.Release.Damage != "" {
				t.Errorf("Build = %+v, want a new intact release from %s at %s", res, req.Source, c.commit)
			}
			if len(started) != 1 || started[0] != c.commit+" go1.27.2" {
				t.Errorf("Starting saw %q", started)
			}
			if len(ran) != 1 || !strings.HasPrefix(ran[0], h.IncomingDir()) {
				t.Errorf("probe ran %q, want the verified copy under .incoming", ran)
			}
			b, err := os.ReadFile(filepath.Join(h.ReleaseDir(c.version), metadataFile))
			if err != nil {
				t.Fatal(err)
			}
			var m metadata
			if err := json.Unmarshal(b, &m); err != nil || m.Version != 1 || m.Source != req.Source || m.Commit != c.commit {
				t.Errorf("release.json = %s, %v; want version 1 with source and commit", b, err)
			}
			incomingEmpty(t, h)

			// Built again, it is already installed and nothing is fetched or built.
			f.steps = nil
			res, err = h.Build(context.Background(), req)
			if err != nil || !res.Already || len(f.steps) != 0 {
				t.Errorf("second Build = %+v, %v, steps %q; want Already with nothing run", res, err, f.steps)
			}
		})
	}
}

func TestAbsSource(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ source, want string }{
		{"https://github.com/ndrwl/guildhall", "https://github.com/ndrwl/guildhall"},
		{"file:///srv/guildhall.git", "file:///srv/guildhall.git"},
		{"git@github.com:fork/guildhall.git", "git@github.com:fork/guildhall.git"},
		{"host:guildhall", "host:guildhall"},
		{"guildhall", filepath.Join(wd, "guildhall")},
		{"../guildhall", filepath.Join(filepath.Dir(wd), "guildhall")},
		{filepath.Join("src", "a:b"), filepath.Join(wd, "src", "a:b")},
		{wd, wd},
	}
	for _, c := range cases {
		if got, err := AbsSource(c.source); err != nil || got != c.want {
			t.Errorf("AbsSource(%q) = %q, %v; want %q", c.source, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "-uhack", "a\nb"} {
		if _, err := AbsSource(bad); err == nil {
			t.Errorf("AbsSource(%q) accepted it", bad)
		}
	}
}

func TestBuildRelativeSource(t *testing.T) {
	h := newHome(t)
	t.Chdir(t.TempDir())
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	f := newFake()
	var ran []string
	report := good(pseudo)
	report.Revision = pseudoFull
	req := buildReq(pseudo, f, report, &ran)
	req.Source = filepath.Join("..", "fork")
	want := filepath.Join(filepath.Dir(wd), "fork")
	res, err := h.Build(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Release.Source != want || len(f.steps) == 0 || f.steps[0] != "fetch "+want {
		t.Errorf("Build recorded source %q, steps %q; want %s fetched and recorded", res.Release.Source, f.steps, want)
	}
}

func TestBuildRefusals(t *testing.T) {
	revised := func(r Report, rev string) Report { r.Revision = rev; return r }
	modified := revised(good(pseudo), pseudoFull)
	modified.Modified = true
	cases := []struct {
		name    string
		version string
		source  string
		fake    func(*fakeBuilder)
		report  Report
		want    string
		probed  bool
	}{
		{"an unknown commit", "v0.0.0-20261101120000-aaaaaaaaaaaa", "", nil, good(pseudo), "commit aaaaaaaaaaaa is not in https://example.com/fork/guildhall", false},
		{"an unknown tag", "v9.9.9", "", nil, good("v9.9.9"), "tag v9.9.9 is not in https://example.com/fork/guildhall", false},
		{"a failed fetch", pseudo, "", func(f *fakeBuilder) { f.fetchErr = errors.New("unreachable") }, good(pseudo), "fetching https://example.com/fork/guildhall: unreachable", false},
		{"a failed build", pseudo, "", func(f *fakeBuilder) { f.buildErr = errors.New("compile error") }, good(pseudo), "compile error", false},
		{"a commit that does not match the version", pseudo, "", func(f *fakeBuilder) { f.commits["0123456789ab"] = tagFull }, good(pseudo), "not a commit it names", false},
		{"another version", pseudo, "", nil, revised(good("v0.0.0-20261101120000-ffffffffffff"), pseudoFull), "reports version", true},
		{"another revision", pseudo, "", nil, revised(good(pseudo), tagFull), "reports revision \"" + tagFull + "\", not the commit " + pseudoFull, true},
		{"no revision", pseudo, "", nil, good(pseudo), "reports revision \"\"", true},
		{"a modified build", pseudo, "", nil, modified, "modified", true},
		{"a source that is an option", pseudo, "--upload-pack=x", nil, good(pseudo), "is not a git repository URL or path", false},
		{"a dirty version", pseudo + "+dirty", "", nil, good(pseudo), "never built from source", false},
		{"dev", "dev", "", nil, good("dev"), "never a release", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHome(t)
			f := newFake()
			if c.fake != nil {
				c.fake(f)
			}
			var ran []string
			req := buildReq(c.version, f, c.report, &ran)
			if c.source != "" {
				req.Source = c.source
			}
			_, err := h.Build(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Build = %v, want a refusal containing %q", err, c.want)
			}
			if (len(ran) > 0) != c.probed {
				t.Errorf("probe ran %q, want run %v", ran, c.probed)
			}
			if _, err := os.Stat(h.ReleaseDir(c.version)); err == nil {
				t.Errorf("release %s was installed", c.version)
			}
			incomingEmpty(t, h)
		})
	}
}

func TestBuildOverADamagedRelease(t *testing.T) {
	h := newHome(t)
	if err := os.MkdirAll(h.ReleaseDir(pseudo), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	var ran []string
	if _, err := h.Build(context.Background(), buildReq(pseudo, f, good(pseudo), &ran)); err == nil || !strings.Contains(err.Error(), "damaged") || len(f.steps) != 0 {
		t.Errorf("Build over a damaged release = %v, steps %q; want a refusal with nothing run", err, f.steps)
	}
}

// TestReleaseJSONOrigin sees a release.json without source and commit, as every release installed from a file has,
// still load as version 1, and one with them pass Check unchanged.
func TestReleaseJSONOrigin(t *testing.T) {
	h := newHome(t)
	from, sum := binary(t, "release v1.4.0")
	var ran []string
	if _, err := h.Install(context.Background(), "v1.4.0", from, sum, reports(good("v1.4.0"), &ran), installedAt, lockWait); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(h.ReleaseDir("v1.4.0"), metadataFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "source") || strings.Contains(string(b), "commit") {
		t.Errorf("release.json of a file install = %s, want no source or commit", b)
	}
	r, err := h.Check("v1.4.0")
	if err != nil || r.Damage != "" || r.Source != "" || r.Commit != "" {
		t.Errorf("Check = %+v, %v", r, err)
	}

	// An older release reads release.json into the version-1 shape it knows, ignoring the new fields.
	report := good(pseudo)
	report.Revision = pseudoFull
	if _, err := h.Build(context.Background(), buildReq(pseudo, newFake(), report, &ran)); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(h.ReleaseDir(pseudo), metadataFile))
	if err != nil {
		t.Fatal(err)
	}
	var older struct {
		Schema  string `json:"schema"`
		Version int    `json:"version"`
		Release string `json:"release"`
	}
	if err := json.Unmarshal(b, &older); err != nil || older.Schema != metadataSchema || older.Version != 1 || older.Release != pseudo {
		t.Errorf("release.json read as an older release = %+v, %v", older, err)
	}
	if r, err := h.Check(pseudo); err != nil || r.Damage != "" || r.Commit != pseudoFull {
		t.Errorf("Check of a built release = %+v, %v", r, err)
	}
}

// TestReadOrigin reads the source and commit of a release built from source, and none for a release installed
// from a file, one not installed or one whose release.json names another release.
func TestReadOrigin(t *testing.T) {
	h := newHome(t)
	from, sum := binary(t, "release v1.4.0")
	var ran []string
	if _, err := h.Install(context.Background(), "v1.4.0", from, sum, reports(good("v1.4.0"), &ran), installedAt, lockWait); err != nil {
		t.Fatal(err)
	}
	report := good(pseudo)
	report.Revision = pseudoFull
	req := buildReq(pseudo, newFake(), report, &ran)
	if _, err := h.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if o, ok := h.ReadOrigin(pseudo); !ok || o.Source != req.Source || o.Commit != pseudoFull {
		t.Errorf("ReadOrigin(built) = %+v, %v; want %s at %s", o, ok, req.Source, pseudoFull)
	}
	for _, v := range []string{"v1.4.0", "v9.9.9", "../x"} {
		if o, ok := h.ReadOrigin(v); ok {
			t.Errorf("ReadOrigin(%s) = %+v, want none", v, o)
		}
	}
	// A release.json naming another release gives none.
	dir := h.ReleaseDir("v2.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(h.ReleaseDir(pseudo), metadataFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, metadataFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if o, ok := h.ReadOrigin("v2.0.0"); ok {
		t.Errorf("ReadOrigin of a release.json naming %s = %+v, want none", pseudo, o)
	}
}
