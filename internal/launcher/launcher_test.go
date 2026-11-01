package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/copydir"
	"github.com/ndrwl/guildhall/internal/release"
)

// roleEnv makes this test binary run as the launcher; a copy of it installed as a release under releases/ runs as
// the fake release instead, whatever the variable says, as the launcher passes its environment on.
const roleEnv = "LAUNCHER_TEST_ROLE"

func TestMain(m *testing.M) {
	exe, _ := os.Executable()
	if filepath.Base(filepath.Dir(filepath.Dir(exe))) == "releases" {
		fakeRelease(exe)
	}
	if os.Getenv(roleEnv) == "launcher" {
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// ran is what the fake release prints of its run.
type ran struct {
	Args    []string `json:"args"`
	Release string   `json:"release"`
	Probe   string   `json:"probe"`
	Exe     string   `json:"exe"`
	Stdin   string   `json:"stdin"`
}

// fakeRelease prints its run as one JSON line, reading stdin first under FAKE_STDIN, and exits with FAKE_EXIT.
// Under FAKE_INTERRUPT it first prints "ready" once it is listening for an interrupt, and waits for one.
func fakeRelease(exe string) {
	r := ran{Args: os.Args[1:], Release: os.Getenv(release.Env), Probe: os.Getenv(release.LauncherProbeEnv), Exe: exe}
	if os.Getenv("FAKE_INTERRUPT") == "1" {
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt)
		_, _ = os.Stdout.WriteString("ready\n")
		<-interrupts
	}
	if os.Getenv("FAKE_STDIN") == "1" {
		b, _ := io.ReadAll(os.Stdin)
		r.Stdin = string(b)
	}
	_ = json.NewEncoder(os.Stdout).Encode(r)
	code, _ := strconv.Atoi(os.Getenv("FAKE_EXIT"))
	os.Exit(code)
}

// fixture is a Guildhall home with copies under a temporary root.
type fixture struct {
	t    *testing.T
	root string
	home release.Home
	self string
	at   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// The launcher reports a copy by its resolved path, so on macOS, where the temporary directory is under the
	// /var symlink to /private/var, the fixture works in resolved paths too.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, root: root, home: release.Home{Dir: filepath.Join(root, "home"), GOOS: runtime.GOOS}, self: self,
		at: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
}

// install installs a copy of this test binary as release version, each one installed a minute after the last.
func (f *fixture) install(version string) {
	f.t.Helper()
	sum, err := release.FileSHA256(f.self)
	if err != nil {
		f.t.Fatal(err)
	}
	probe := func(context.Context, string) (release.Report, error) {
		return release.Report{Version: version, ReleaseContract: release.Contract}, nil
	}
	f.at = f.at.Add(time.Minute)
	if _, err := f.home.Install(context.Background(), version, f.self, sum, probe, f.at, time.Second); err != nil {
		f.t.Fatal(err)
	}
}

// copyDir makes a copy directory with a marker and, when pin is not "", a release.pin.
func (f *fixture) copyDir(name, pin string) string {
	f.t.Helper()
	dir := filepath.Join(f.root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	b, _ := json.Marshal(copydir.Marker{Schema: copydir.MarkerSchema, Version: copydir.MarkerVersion, Name: name})
	if err := os.WriteFile(filepath.Join(dir, copydir.MarkerFile), b, 0o644); err != nil {
		f.t.Fatal(err)
	}
	if pin != "" {
		if err := release.WritePin(dir, pin); err != nil {
			f.t.Fatal(err)
		}
	}
	return dir
}

// result is one launcher run.
type result struct {
	code           int
	stdout, stderr string
	ran            ran
}

// launch runs the launcher in dir with args, an inherited GUILDHALL_RELEASE the launcher must replace, and extra
// environment entries.
func (f *fixture) launch(dir, stdin string, extra []string, args ...string) result {
	f.t.Helper()
	cmd := exec.Command(f.self, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), roleEnv+"=launcher", release.HomeEnv+"="+f.home.Dir, release.Env+"=v9.9.9-inherited")
	cmd.Env = append(cmd.Env, extra...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errOut.String()}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.code = exit.ExitCode()
	case err != nil:
		f.t.Fatal(err)
	}
	if r.stdout != "" {
		if err := json.Unmarshal([]byte(r.stdout), &r.ran); err != nil {
			f.t.Fatalf("launcher output %q: %v", r.stdout, err)
		}
	}
	f.t.Logf("guildhall %s in %s: exit %d\nstdout: %sstderr: %s", strings.Join(args, " "), dir, r.code, r.stdout, r.stderr)
	return r
}

// TestLauncherRunsTheCopyPin: the launcher runs the selected copy's pinned release with the same arguments, standard
// streams and exit code, and GUILDHALL_RELEASE replaced by the pin; copy A's pin leaves copy B on its own.
func TestLauncherRunsTheCopyPin(t *testing.T) {
	f := newFixture(t)
	f.install("v1.0.0")
	f.install("v2.0.0")
	a := f.copyDir("a", "v1.0.0")
	b := f.copyDir("b", "v2.0.0")
	for _, tc := range []struct {
		name, dir string
		args      []string
		want      string
	}{
		{"--copy", f.root, []string{"task", "list", "--copy", a}, "v1.0.0"},
		{"--copy=", f.root, []string{"director", "status", "--copy=" + b}, "v2.0.0"},
		{"discovery", filepath.Join(b), []string{"director", "status"}, "v2.0.0"},
		{"discovery below the copy", f.copyDirSub(a, "notes"), []string{"version"}, "v1.0.0"},
		{"the store's own copy", f.root, []string{"director", "status", "--store", filepath.Join(a, "director.store")}, "v1.0.0"},
		{"an unrecorded task, on the pin", f.root, []string{"artisan", "resume", "--store", filepath.Join(a, "director.store"), "--repo", "app", "--task", "t-1"}, "v1.0.0"},
		{"release command", a, []string{"release", "status"}, "v1.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := f.launch(tc.dir, "", nil, tc.args...)
			if r.code != 0 || r.ran.Release != tc.want || !slices.Equal(r.ran.Args, tc.args) || r.stderr != "" {
				t.Fatalf("got exit %d, release %q, args %q, stderr %q; want %s", r.code, r.ran.Release, r.ran.Args, r.stderr, tc.want)
			}
			if !copydir.SamePath(filepath.Dir(r.ran.Exe), f.home.ReleaseDir(tc.want)) {
				t.Fatalf("ran %s, want the binary under %s", r.ran.Exe, f.home.ReleaseDir(tc.want))
			}
		})
	}
	r := f.launch(a, "to the release\n", []string{"FAKE_STDIN=1", "FAKE_EXIT=7"}, "task", "new")
	if r.code != 7 || r.ran.Stdin != "to the release\n" {
		t.Fatalf("got exit %d and stdin %q, want 7 and the launcher's stdin", r.code, r.ran.Stdin)
	}
	r = f.launch(a, "", []string{release.LauncherProbeEnv + "=0"}, "version")
	if r.ran.Probe != "" {
		t.Fatalf("the launcher passed %s=%q on", release.LauncherProbeEnv, r.ran.Probe)
	}
}

// copyDirSub makes a directory under dir.
func (f *fixture) copyDirSub(dir, name string) string {
	f.t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return sub
}

// TestLauncherRefusesAnUnusablePin: a pin whose release is missing or damaged, or a copy with no pin, refuses
// every command but `guildhall release ...`, naming the version, the path checked and the fix, and never falls back.
func TestLauncherRefusesAnUnusablePin(t *testing.T) {
	f := newFixture(t)
	f.install("v1.0.0")
	f.install("v2.0.0")
	missing := f.copyDir("missing", "v3.0.0")
	damaged := f.copyDir("damaged", "v1.0.0")
	absent := f.copyDir("absent", "")
	managed := f.copyDir("managed", "")
	if err := os.WriteFile(filepath.Join(managed, "managed.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.home.Binary("v1.0.0"), []byte("not the binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, dir string
		want      []string
	}{
		{"missing", missing, []string{"v3.0.0", f.home.Binary("v3.0.0"), "guildhall release install v3.0.0", "never falls back"}},
		{"damaged", damaged, []string{"v1.0.0", "damaged", f.home.ReleaseDir("v1.0.0"), "guildhall release install v1.0.0"}},
		{"absent", absent, []string{release.PinPath(absent), "guildhall release use"}},
		{"absent, managed", managed, []string{"guildhall config reload --copy " + managed}},
		{"no copy", f.root, []string{"no Guildhall copy selected", "--copy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, args := range [][]string{{"director", "start"}, {"task", "advance", "--repo", "app", "t-1"}} {
				r := f.launch(tc.dir, "", nil, args...)
				if r.code != 1 || r.stdout != "" {
					t.Fatalf("%v: got exit %d and output %q, want a refusal", args, r.code, r.stdout)
				}
				for _, w := range tc.want {
					if !strings.Contains(r.stderr, w) {
						t.Fatalf("%v: stderr %q does not name %q", args, r.stderr, w)
					}
				}
			}
		})
	}
}

// TestLauncherRecoveryDispatch: `guildhall release ...` with an unusable pin runs on the newest installed, verified
// release and says so; with none it refuses, never running an .incoming binary.
func TestLauncherRecoveryDispatch(t *testing.T) {
	f := newFixture(t)
	f.install("v1.0.0")
	f.install("v2.0.0")
	f.install("v3.0.0")
	// The newest install is damaged, so recovery takes the newest verified one.
	if err := os.WriteFile(f.home.Binary("v3.0.0"), []byte("damaged"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{f.copyDir("absent", ""), f.copyDir("missing", "v9.0.0"), f.copyDir("damaged", "v3.0.0"), f.root} {
		r := f.launch(dir, "", nil, "release", "status")
		if r.code != 0 || r.ran.Release != "v2.0.0" || !strings.Contains(r.stderr, "recovery dispatch") || !strings.Contains(r.stderr, "v2.0.0") {
			t.Fatalf("%s: got exit %d on %q, stderr %q; want recovery on v2.0.0", dir, r.code, r.ran.Release, r.stderr)
		}
	}
	empty := newFixture(t)
	if err := os.MkdirAll(filepath.Join(empty.home.IncomingDir(), "v1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(empty.home.IncomingDir(), "v1.0.0", release.BinaryName(runtime.GOOS)), []byte("unverified"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := empty.launch(empty.copyDir("absent", ""), "", nil, "release", "install", "v1.0.0")
	if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "no installed, verified release") {
		t.Fatalf("got exit %d, output %q, stderr %q; want a refusal", r.code, r.stdout, r.stderr)
	}
}

// TestLauncherRefusesConflictingSelection: two --copy or --store flags naming different directories are refused
// rather than picking one.
func TestLauncherRefusesConflictingSelection(t *testing.T) {
	f := newFixture(t)
	f.install("v1.0.0")
	a, b := f.copyDir("a", "v1.0.0"), f.copyDir("b", "v1.0.0")
	for _, args := range [][]string{
		{"director", "status", "--copy", a, "--copy", b},
		{"director", "status", "--store", filepath.Join(a, "director.store"), "--store", filepath.Join(b, "director.store")},
	} {
		r := f.launch(f.root, "", nil, args...)
		if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "conflicting") {
			t.Fatalf("%v: got exit %d, output %q, stderr %q", args, r.code, r.stdout, r.stderr)
		}
	}
}

// TestLauncherProbe: under the probe variable the launcher prints its report and dispatches nothing.
func TestLauncherProbe(t *testing.T) {
	f := newFixture(t)
	cmd := exec.Command(f.self, "version", "--json")
	cmd.Env = append(os.Environ(), roleEnv+"=launcher", release.LauncherProbeEnv+"=1", release.HomeEnv+"="+f.home.Dir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var r release.LauncherReport
	if err := json.Unmarshal(out, &r); err != nil || r != (release.LauncherReport{Schema: release.LauncherSchema, Version: release.LauncherVersion}) {
		t.Fatalf("probe printed %q: %v", out, err)
	}
}

// TestChildEnv: every inherited GUILDHALL_RELEASE and probe entry is dropped, and the selected release set once.
func TestChildEnv(t *testing.T) {
	got := ChildEnv([]string{"A=1", release.Env + "=v0", release.LauncherProbeEnv + "=1", "B=2", release.Env + "=v9"}, "v1.0.0")
	want := []string{"A=1", "B=2", release.Env + "=v1.0.0"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
