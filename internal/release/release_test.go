package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const lockWait = 5 * time.Second

var installedAt = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestLocate(t *testing.T) {
	// Locate resolves the binary's symbolic links, so the root is resolved too: macOS's temporary directory is under
	// /var, a link to /private/var.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(root, "home", "releases", "v1.4.0", BinaryName(runtime.GOOS))
	if err := os.MkdirAll(filepath.Dir(release), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(root, "bin", BinaryName(runtime.GOOS))
	abs := func(p string) string {
		a, err := filepath.Abs(p)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	cases := []struct {
		name    string
		vars    map[string]string
		exe     string
		version string
		goos    string
		want    string
	}{
		{"GUILDHALL_HOME wins", map[string]string{HomeEnv: filepath.Join(root, "set"), "HOME": root}, release, "v1.4.0", runtime.GOOS, filepath.Join(root, "set")},
		{"a relative GUILDHALL_HOME is made absolute", map[string]string{HomeEnv: "rel"}, "", "", runtime.GOOS, abs("rel")},
		{"an installed release finds its home", map[string]string{"HOME": root}, release, "v1.4.0", runtime.GOOS, filepath.Join(root, "home")},
		{"a release under another version's directory does not", map[string]string{"XDG_DATA_HOME": filepath.Join(root, "data")}, release, "v1.5.0", runtime.GOOS, filepath.Join(root, "data", "guildhall")},
		{"a binary elsewhere takes the default", map[string]string{"XDG_DATA_HOME": filepath.Join(root, "data")}, elsewhere, "v1.4.0", "linux", filepath.Join(root, "data", "guildhall")},
		{"XDG_DATA_HOME", map[string]string{"XDG_DATA_HOME": filepath.Join(root, "data"), "HOME": root}, "", "", "linux", filepath.Join(root, "data", "guildhall")},
		{"a relative XDG_DATA_HOME is ignored", map[string]string{"XDG_DATA_HOME": "data", "HOME": root}, "", "", "linux", filepath.Join(root, ".local", "share", "guildhall")},
		{"HOME", map[string]string{"HOME": root}, "", "", "darwin", filepath.Join(root, ".local", "share", "guildhall")},
		{"LOCALAPPDATA on Windows", map[string]string{"LOCALAPPDATA": root, "HOME": filepath.Join(root, "h")}, "", "", "windows", filepath.Join(root, "Guildhall")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := Locate(env(c.vars), c.exe, c.version, c.goos)
			if err != nil {
				t.Fatal(err)
			}
			if h.Dir != c.want || h.GOOS != c.goos {
				t.Errorf("Locate = %+v, want %s on %s", h, c.want, c.goos)
			}
		})
	}
	for _, goos := range []string{"linux", "windows"} {
		if _, err := Locate(env(nil), "", "", goos); !errors.Is(err, ErrNoHome) {
			t.Errorf("Locate with no variables on %s = %v, want ErrNoHome", goos, err)
		}
	}
}

func TestValidVersion(t *testing.T) {
	for _, v := range []string{"v1.4.0", "v0.0.0-20261031162214-d385b331abcd", "v1.4.0-rc.1+build.5"} {
		if err := ValidVersion(v); err != nil {
			t.Errorf("ValidVersion(%q) = %v", v, err)
		}
	}
	for _, v := range []string{"dev", "", "1.4.0", ".incoming", "v1/../x", `v1\x`, "v..", "v1..2", "v", "v 1"} {
		if err := ValidVersion(v); err == nil {
			t.Errorf("ValidVersion(%q) = nil, want a refusal", v)
		}
	}
}

// binary writes a stand-in release binary and returns its path and sha256.
func binary(t *testing.T, content string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guildhall-download")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return path, hex.EncodeToString(sum[:])
}

// reports is a Probe that answers report and records each path it ran.
func reports(report Report, ran *[]string) Probe {
	return func(_ context.Context, path string) (Report, error) {
		*ran = append(*ran, path)
		return report, nil
	}
}

func good(version string) Report {
	return Report{Version: version, StoreSchema: 1, ReleaseContract: 1}
}

func newHome(t *testing.T) Home {
	return Home{Dir: filepath.Join(t.TempDir(), "home"), GOOS: runtime.GOOS}
}

// incomingEmpty fails unless .incoming holds nothing.
func incomingEmpty(t *testing.T, h Home) {
	t.Helper()
	entries, err := os.ReadDir(h.IncomingDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) > 0 {
		t.Errorf(".incoming holds %v, want nothing", entries)
	}
}

func TestInstall(t *testing.T) {
	h := newHome(t)
	from, sum := binary(t, "release one")
	var ran []string
	res, err := h.Install(context.Background(), "v1.4.0", from, strings.ToUpper(sum), reports(good("v1.4.0"), &ran), installedAt, lockWait)
	if err != nil {
		t.Fatal(err)
	}
	want := Release{Version: "v1.4.0", SHA256: sum, StoreSchema: 1, ReleaseContract: 1, InstalledAt: installedAt}
	if res.Already || res.Release != want {
		t.Errorf("Install = %+v, want %+v newly installed", res, want)
	}
	// The probe ran the verified copy under .incoming, never the file it was given or the installed one.
	if len(ran) != 1 || !strings.HasPrefix(ran[0], h.IncomingDir()+string(filepath.Separator)) {
		t.Errorf("probe ran %v, want one run under %s", ran, h.IncomingDir())
	}
	if b, err := os.ReadFile(h.Binary("v1.4.0")); err != nil || string(b) != "release one" {
		t.Errorf("installed binary = %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(h.ReleaseDir("v1.4.0"), "SHA256")); err != nil || string(b) != sum+"\n" {
		t.Errorf("SHA256 = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(h.Binary("v1.4.0")); err != nil || info.Mode().Perm()&0o111 == 0 {
			t.Errorf("installed binary mode = %v, %v, want executable", info.Mode(), err)
		}
	}
	incomingEmpty(t, h)

	// Installing it again changes nothing and runs nothing.
	ran = nil
	res, err = h.Install(context.Background(), "v1.4.0", from, sum, reports(good("v1.4.0"), &ran), installedAt.Add(time.Hour), lockWait)
	if err != nil || !res.Already || res.Release != want || len(ran) != 0 {
		t.Errorf("second Install = %+v, %v, probe ran %v; want Already with the first install time and no run", res, err, ran)
	}

	// Another binary under the same version is refused, as a release directory is never changed.
	other, otherSum := binary(t, "release one, rebuilt")
	if _, err := h.Install(context.Background(), "v1.4.0", other, otherSum, reports(good("v1.4.0"), &ran), installedAt, lockWait); err == nil || !strings.Contains(err.Error(), "never changed") {
		t.Errorf("Install of another binary as v1.4.0 = %v, want a refusal", err)
	}
	if b, _ := os.ReadFile(h.Binary("v1.4.0")); string(b) != "release one" {
		t.Errorf("installed binary changed to %q", b)
	}
	incomingEmpty(t, h)
}

func TestInstallRefusals(t *testing.T) {
	from, sum := binary(t, "a build")
	cases := []struct {
		name, version, sum string
		report             Report
		probeErr           error
		want               string
		probed             bool
	}{
		{"dev version", "dev", sum, good("dev"), nil, `"dev"`, false},
		{"bad version", "../v1", sum, good("v1"), nil, "not a release version", false},
		{"bad sha256", "v1.4.0", "abc", good("v1.4.0"), nil, "not 64 hex digits", false},
		{"sha256 mismatch", "v1.4.0", strings.Repeat("0", 64), good("v1.4.0"), nil, "not the expected", false},
		{"reports dev", "v1.4.0", sum, good("dev"), nil, "reports version dev", true},
		{"reports another version", "v1.4.0", sum, good("v1.5.0"), nil, `reports version "v1.5.0"`, true},
		{"modified", "v1.4.0", sum, Report{Version: "v1.4.0", Modified: true, ReleaseContract: 1}, nil, "modified", true},
		{"no release contract", "v1.4.0", sum, Report{Version: "v1.4.0", StoreSchema: 1}, nil, "predates release installs", true},
		{"probe fails", "v1.4.0", sum, Report{}, errors.New("exec format error"), "exec format error", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHome(t)
			probed := false
			probe := func(context.Context, string) (Report, error) {
				probed = true
				return c.report, c.probeErr
			}
			_, err := h.Install(context.Background(), c.version, from, c.sum, probe, installedAt, lockWait)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Install = %v, want a refusal containing %q", err, c.want)
			}
			if probed != c.probed {
				t.Errorf("probe ran = %v, want %v", probed, c.probed)
			}
			if list, err := h.List(); err != nil || len(list) != 0 {
				t.Errorf("releases after a refusal = %+v, %v; want none", list, err)
			}
			incomingEmpty(t, h)
		})
	}
	t.Run("missing file", func(t *testing.T) {
		h := newHome(t)
		var ran []string
		if _, err := h.Install(context.Background(), "v1.4.0", filepath.Join(t.TempDir(), "gone"), sum, reports(good("v1.4.0"), &ran), installedAt, lockWait); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Install of a missing file = %v, want fs.ErrNotExist", err)
		}
		incomingEmpty(t, h)
	})
}

// installed installs content as version into h and returns its sha256.
func installed(t *testing.T, h Home, version, content string, at time.Time) string {
	t.Helper()
	from, sum := binary(t, content)
	var ran []string
	if _, err := h.Install(context.Background(), version, from, sum, reports(good(version), &ran), at, lockWait); err != nil {
		t.Fatal(err)
	}
	return sum
}

func TestCheckFindsDamage(t *testing.T) {
	cases := []struct {
		name   string
		damage func(t *testing.T, dir, bin string)
		want   string
	}{
		{"binary changed", func(t *testing.T, _, bin string) { write(t, bin, "tampered") }, "binary hashes to"},
		{"binary removed", func(t *testing.T, _, bin string) { remove(t, bin) }, "binary:"},
		{"SHA256 removed", func(t *testing.T, dir, _ string) { remove(t, filepath.Join(dir, "SHA256")) }, "SHA256:"},
		{"SHA256 garbled", func(t *testing.T, dir, _ string) { write(t, filepath.Join(dir, "SHA256"), "nope\n") }, "not 64 hex digits"},
		{"SHA256 names another binary", func(t *testing.T, dir, _ string) {
			write(t, filepath.Join(dir, "SHA256"), strings.Repeat("a", 64)+"\n")
		}, "release.json names sha256"},
		{"release.json removed", func(t *testing.T, dir, _ string) { remove(t, filepath.Join(dir, "release.json")) }, "release.json:"},
		{"release.json names another release", func(t *testing.T, dir, _ string) {
			p := filepath.Join(dir, "release.json")
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			write(t, p, strings.Replace(string(b), `"v1.4.0"`, `"v1.3.0"`, 1))
		}, `names release "v1.3.0"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHome(t)
			sum := installed(t, h, "v1.4.0", "release one", installedAt)
			c.damage(t, h.ReleaseDir("v1.4.0"), h.Binary("v1.4.0"))
			r, err := h.Check("v1.4.0")
			if err != nil || !strings.Contains(r.Damage, c.want) {
				t.Errorf("Check = %+v, %v; want damage containing %q", r, err, c.want)
			}
			from, _ := binary(t, "release one")
			var ran []string
			if _, err := h.Install(context.Background(), "v1.4.0", from, sum, reports(good("v1.4.0"), &ran), installedAt, lockWait); err == nil || !strings.Contains(err.Error(), "damaged") {
				t.Errorf("Install over a damaged release = %v, want a refusal naming the damage", err)
			}
		})
	}
	if _, err := newHome(t).Check("v9.9.9"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Check of a release never installed = %v, want fs.ErrNotExist", err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestList(t *testing.T) {
	h := newHome(t)
	if list, err := h.List(); err != nil || len(list) != 0 {
		t.Fatalf("List of a new home = %+v, %v", list, err)
	}
	installed(t, h, "v1.3.0", "three", installedAt)
	installed(t, h, "v1.4.0", "four", installedAt.Add(time.Hour))
	installed(t, h, "v1.2.0", "two", installedAt.Add(time.Hour))
	// A stray file and an unverified copy under .incoming are not releases; a stray directory is damaged.
	if err := os.MkdirAll(filepath.Join(h.IncomingDir(), "v9.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(h.ReleasesDir(), "stray"), 0o755); err != nil {
		t.Fatal(err)
	}
	list, err := h.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range list {
		got = append(got, fmt.Sprintf("%s %t", r.Version, r.Damage == ""))
	}
	want := []string{"v1.2.0 true", "v1.4.0 true", "v1.3.0 true", "stray false"}
	if !slices.Equal(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

func TestRegister(t *testing.T) {
	h := newHome(t)
	if copies, err := h.Copies(); err != nil || len(copies) != 0 {
		t.Fatalf("Copies of a new home = %v, %v", copies, err)
	}
	root := t.TempDir()
	b, a := filepath.Join(root, "b"), filepath.Join(root, "a")
	for _, dir := range []string{b, a, b, filepath.Join(a, "x", "..")} {
		if err := h.Register(dir, lockWait); err != nil {
			t.Fatal(err)
		}
	}
	if copies, err := h.Copies(); err != nil || !slices.Equal(copies, []string{a, b}) {
		t.Errorf("Copies = %v, %v; want %v", copies, err, []string{a, b})
	}
	if err := h.Register(filepath.Join(root, "bad\nname"), lockWait); err == nil {
		t.Error("Register of a directory with a line break succeeded")
	}
}

// TestRegisterIsSerialized registers many copies at once; the home lock keeps every one.
func TestRegisterIsSerialized(t *testing.T) {
	h := newHome(t)
	root := t.TempDir()
	var want []string
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		dir := filepath.Join(root, fmt.Sprintf("copy-%02d", i))
		want = append(want, dir)
		wg.Go(func() { errs <- h.Register(dir, 30*time.Second) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if copies, err := h.Copies(); err != nil || !slices.Equal(copies, want) {
		t.Errorf("Copies = %v, %v; want all %d", copies, err, len(want))
	}
}

func TestLock(t *testing.T) {
	h := newHome(t)
	unlock, err := h.Lock(lockWait)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Lock(50 * time.Millisecond); !errors.Is(err, ErrLocked) {
		t.Errorf("second Lock while held = %v, want ErrLocked", err)
	}
	if err := h.Register(filepath.Join(t.TempDir(), "c"), 50*time.Millisecond); !errors.Is(err, ErrLocked) {
		t.Errorf("Register while the lock is held = %v, want ErrLocked", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	unlock, err = h.Lock(lockWait)
	if err != nil {
		t.Fatalf("Lock after unlock = %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}
