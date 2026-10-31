package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/copydir"
	"github.com/ndrwl/guildhall/internal/launcher"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// pinFixture is a standalone copy with a store, a Guildhall home and a PATH directory holding a guildhall release
// and the guildhall-launcher `go install` builds beside it.
type pinFixture struct {
	t                      *testing.T
	copy, home, pathDir    string
	guildhall, launcherBin string
	report                 string
	ran                    []string
	app                    *App
	stdout, stderr         *strings.Builder
}

func newPinFixture(t *testing.T) *pinFixture {
	t.Helper()
	f := &pinFixture{t: t, copy: bareCopy(t), home: filepath.Join(t.TempDir(), "home"), pathDir: t.TempDir()}
	init, _, stderr := newTestApp(nil, nil)
	if code := init.Run([]string{"store", "init", filepath.Join(f.copy, "director.store")}); code != 0 {
		t.Fatalf("store init: exit %d, stderr %s", code, stderr)
	}
	f.guildhall = filepath.Join(f.pathDir, release.BinaryName(runtime.GOOS))
	f.launcherBin = filepath.Join(f.pathDir, release.LauncherName(runtime.GOOS))
	writeFile(t, f.guildhall, "release v1.0.0")
	writeFile(t, f.launcherBin, "the launcher")
	f.report = `{"version":"v1.0.0","modified":false,"store_schema":1,"release_contract":1}`
	f.app, f.stdout, f.stderr = releaseTestApp(f.home, "", &f.ran)
	f.app.Output = func(_ context.Context, path string, args ...string) ([]byte, error) {
		f.ran = append(f.ran, path+" "+strings.Join(args, " "))
		return []byte(f.report), nil
	}
	// Like exec.LookPath, the fake finds a name without its extension on Windows, where guildhall is guildhall.exe.
	f.app.LookPath = func(name string) (string, error) {
		names := []string{name}
		if runtime.GOOS == "windows" {
			names = append(names, name+".exe")
		}
		for _, n := range names {
			if p := filepath.Join(f.pathDir, n); fileExists(p) {
				return p, nil
			}
		}
		return "", errors.New("not found")
	}
	f.app.ProbeLauncher = func(_ context.Context, path string) error {
		if b, err := os.ReadFile(path); err != nil || string(b) != "the launcher" {
			return errors.New("not a launcher")
		}
		return nil
	}
	return f
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// run runs the CLI, returning its exit code; stdout and stderr are reset first.
func (f *pinFixture) run(args ...string) int {
	f.t.Helper()
	f.stdout.Reset()
	f.stderr.Reset()
	code := f.app.Run(args)
	f.t.Logf("guildhall %s: exit %d\nstdout: %sstderr: %s", strings.Join(args, " "), code, f.stdout, f.stderr)
	return code
}

func (f *pinFixture) pins() []store.ReleasePin {
	f.t.Helper()
	pins, err := withStore(filepath.Join(f.copy, "director.store"), true, func(s *store.Store) ([]store.ReleasePin, error) { return s.ReleasePins() })
	if err != nil {
		f.t.Fatal(err)
	}
	return pins
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestReleaseAdopt: adopt installs the guildhall on PATH as the copy's first release, records its pin, installs
// the launcher in the home and puts it on PATH in place of the old binary, which it keeps; a second adopt changes
// nothing and says what is in place.
func TestReleaseAdopt(t *testing.T) {
	f := newPinFixture(t)
	h := release.Home{Dir: f.home, GOOS: runtime.GOOS}
	if code := f.run("release", "adopt", "--copy", f.copy); code != 0 {
		t.Fatalf("release adopt = %d", code)
	}
	if _, err := h.Runnable("v1.0.0"); err != nil || readText(t, h.Binary("v1.0.0")) != "release v1.0.0" {
		t.Fatalf("v1.0.0 not installed from PATH: %v", err)
	}
	pins := f.pins()
	if len(pins) != 1 || pins[0].Version != "v1.0.0" || pins[0].Source != store.PinAdopt || pins[0].PreviousVersion != "" {
		t.Fatalf("pins %+v, want one adopt pin of v1.0.0", pins)
	}
	if pin, err := release.ReadPin(f.copy); err != nil || pin != "v1.0.0" {
		t.Fatalf("release.pin = %q, %v", pin, err)
	}
	if copies, err := h.Copies(); err != nil || !slices.Equal(copies, []string{f.copy}) {
		t.Fatalf("inventory %q, %v", copies, err)
	}
	if readText(t, h.Launcher()) != "the launcher" || readText(t, f.guildhall) != "the launcher" {
		t.Fatal("the launcher is not installed in the home and on PATH")
	}
	if readText(t, backupName(f.guildhall)) != "release v1.0.0" {
		t.Fatal("the old binary was not kept beside the launcher")
	}
	ran := len(f.ran)
	if code := f.run("release", "adopt", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "already the launcher") || !strings.Contains(f.stdout.String(), "already pins v1.0.0") {
		t.Fatalf("second release adopt = %d, %q", code, f.stdout)
	}
	if len(f.pins()) != 1 || len(f.ran) != ran {
		t.Fatal("a second adopt recorded a pin or ran a binary")
	}
}

// TestReleaseAdoptRefusals: a dev or modified guildhall on PATH, or no launcher to install, refuses, leaving the
// PATH binary and the copy unchanged.
func TestReleaseAdoptRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, report, want string
		noLauncher         bool
	}{
		{"dev", `{"version":"dev","modified":false,"release_contract":1}`, "dev", false},
		{"modified", `{"version":"v1.0.0","modified":true,"release_contract":1}`, "modified", false},
		{"no launcher", "", "guildhall-launcher", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPinFixture(t)
			if tc.report != "" {
				f.report = tc.report
			}
			if tc.noLauncher {
				if err := os.Remove(f.launcherBin); err != nil {
					t.Fatal(err)
				}
			}
			if code := f.run("release", "adopt", "--copy", f.copy); code != 1 || !strings.Contains(f.stderr.String(), tc.want) {
				t.Fatalf("release adopt = %d, stderr %q; want a refusal naming %q", code, f.stderr, tc.want)
			}
			if readText(t, f.guildhall) != "release v1.0.0" || fileExists(backupName(f.guildhall)) {
				t.Fatal("a refused adopt changed the guildhall on PATH")
			}
			if len(f.pins()) != 0 || release.HasPin(f.copy) {
				t.Fatal("a refused adopt pinned the copy")
			}
		})
	}
}

// TestReleaseUse: use moves a standalone copy's pin to an installed release, recording the previous one; the same
// pin again changes nothing; a release not installed and a managed copy are refused.
func TestReleaseUse(t *testing.T) {
	f := newPinFixture(t)
	if code := f.run("release", "adopt", "--copy", f.copy); code != 0 {
		t.Fatalf("release adopt = %d", code)
	}
	from, sum := releaseFile(t, "release v1.1.0")
	f.report = `{"version":"v1.1.0","modified":false,"store_schema":1,"release_contract":1}`
	if code := f.run("release", "install", "v1.1.0", "--from", from, "--sha256", sum); code != 0 {
		t.Fatalf("release install = %d", code)
	}
	if code := f.run("release", "use", "v1.1.0", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "was v1.0.0") {
		t.Fatalf("release use = %d, %q", code, f.stdout)
	}
	pins := f.pins()
	if len(pins) != 2 || pins[1].Version != "v1.1.0" || pins[1].SHA256 != sum || pins[1].Source != store.PinUse || pins[1].PreviousVersion != "v1.0.0" || pins[1].PreviousSHA256 != pins[0].SHA256 {
		t.Fatalf("pins %+v", pins)
	}
	if pin, _ := release.ReadPin(f.copy); pin != "v1.1.0" {
		t.Fatalf("release.pin = %q", pin)
	}
	if code := f.run("release", "use", "v1.1.0", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "nothing changed") || len(f.pins()) != 2 {
		t.Fatalf("second release use = %d, %q", code, f.stdout)
	}
	if code := f.run("release", "use", "v9.0.0", "--copy", f.copy); code != 1 || !strings.Contains(f.stderr.String(), "guildhall release install v9.0.0") {
		t.Fatalf("release use of a missing release = %d, %q", code, f.stderr)
	}
	writeFile(t, filepath.Join(f.copy, "managed.toml"), "")
	if code := f.run("release", "use", "v1.0.0", "--copy", f.copy); code != 1 || !strings.Contains(f.stderr.String(), "rollout") {
		t.Fatalf("release use on a managed copy = %d, %q", code, f.stderr)
	}
	if len(f.pins()) != 2 {
		t.Fatal("a refused release use recorded a pin")
	}
}

// TestReleaseStatusShowsThePin: status names the copies pinning each release and the selected copy's pin, and a
// release.pin that disagrees with the store's newest pin event is a problem there and refused by the
// configuration-admission boundary until release use repairs it.
func TestReleaseStatusShowsThePin(t *testing.T) {
	f := newPinFixture(t)
	if code := f.run("release", "adopt", "--copy", f.copy); code != 0 {
		t.Fatalf("release adopt = %d", code)
	}
	if code := f.run("release", "status", "--json", "--copy", f.copy); code != 0 {
		t.Fatalf("release status = %d", code)
	}
	var rep releaseStatusReport
	if err := json.Unmarshal([]byte(f.stdout.String()), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Releases) != 1 || !slices.Equal(rep.Releases[0].PinnedBy, []string{f.copy}) || rep.Copy == nil || rep.Copy.Pin != "v1.0.0" || rep.Copy.EventVersion != "v1.0.0" || rep.Copy.Problem != "" {
		t.Fatalf("status %+v", rep)
	}
	if _, err := admitDirector(filepath.Join(f.copy, "config.toml")); err != nil {
		t.Fatalf("an agreeing pin is refused: %v", err)
	}
	if err := release.WritePin(f.copy, "v0.9.0"); err != nil {
		t.Fatal(err)
	}
	if code := f.run("release", "status", "--copy", f.copy); code != 0 || !strings.Contains(f.stdout.String(), "problem: ") || !strings.Contains(f.stdout.String(), "guildhall release use v1.0.0") {
		t.Fatalf("release status = %d, %q", code, f.stdout)
	}
	if _, err := admitDirector(filepath.Join(f.copy, "config.toml")); err == nil || !strings.Contains(err.Error(), release.PinFile) {
		t.Fatalf("a disagreeing pin is admitted: %v", err)
	}
	if code := f.run("release", "use", "v1.0.0", "--copy", f.copy); code != 0 {
		t.Fatalf("release use = %d", code)
	}
	if _, err := admitDirector(filepath.Join(f.copy, "config.toml")); err != nil {
		t.Fatalf("release use did not repair the pin: %v", err)
	}
}

// TestCheckReleaseEnv: a release refuses a GUILDHALL_RELEASE naming another version and admits its own or none.
func TestCheckReleaseEnv(t *testing.T) {
	setVersion(t, "v1.2.3")
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == release.Env {
				return v
			}
			return ""
		}
	}
	for v, ok := range map[string]bool{"": true, "v1.2.3": true, "v1.2.4": false} {
		if err := CheckReleaseEnv(env(v)); (err == nil) != ok {
			t.Errorf("%s=%q: %v", release.Env, v, err)
		}
	}
}

// TestCopySelectionIsShared: the CLI's copy selection and the launcher's agree on every case, one table for both, so
// the launcher always runs the release of the copy the command then acts on.
func TestCopySelectionIsShared(t *testing.T) {
	root := t.TempDir()
	a := markCopy(t, mkdir(t, filepath.Join(root, "a")))
	b := markCopy(t, mkdir(t, filepath.Join(root, "b")))
	nested := markCopy(t, mkdir(t, filepath.Join(a, "inner")))
	plain := mkdir(t, filepath.Join(root, "plain"))
	mkdir(t, filepath.Join(a, "state", "work"))
	mkdir(t, filepath.Join(a, "notes"))
	for _, tc := range []struct {
		name, wd string
		args     []string
		want     string
	}{
		{"--copy wins over discovery", a, []string{"--copy", b}, b},
		{"discovery at the copy", a, nil, a},
		{"discovery below it", filepath.Join(a, "notes"), nil, a},
		{"the nearest copy", nested, nil, nested},
		{"none under state/", filepath.Join(a, "state", "work"), nil, ""},
		{"none outside a copy", plain, nil, ""},
		{"no discovery with --config", a, []string{"--config", filepath.Join(plain, "config.toml")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, _, _ := newTestApp(nil, nil)
			app.Getwd = func() (string, error) { return tc.wd, nil }
			fl := flag.NewFlagSet("test", flag.ContinueOnError)
			var cp copyFlags
			cp.register(fl)
			config := fl.String("config", "", "")
			if err := fl.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			var storePath string
			if err := cp.apply(app, &storePath, config, nil); err != nil {
				t.Fatal(err)
			}
			got, ok, err := launcher.SelectCopy(tc.args, app.Getwd)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				got = ""
			}
			if !samePathOrEmpty(cp.dir, tc.want) || !samePathOrEmpty(got, tc.want) {
				t.Fatalf("CLI selected %q, launcher %q; want %q", cp.dir, got, tc.want)
			}
		})
	}
}

func samePathOrEmpty(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return copydir.SamePath(a, b)
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
