package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/release"
)

// releaseTestApp is an App whose Guildhall home is home, at a fixed clock, whose runs of any binary answer
// `version --json` with report, recording each path run.
func releaseTestApp(home string, report string, ran *[]string) (*App, *strings.Builder, *strings.Builder) {
	app, _, _ := newTestApp(nil, nil)
	var stdout, stderr strings.Builder
	app.Stdout, app.Stderr = &stdout, &stderr
	app.Getenv = func(k string) string {
		if k == release.HomeEnv {
			return home
		}
		return ""
	}
	app.Now = func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) }
	app.Output = func(_ context.Context, path string, args ...string) ([]byte, error) {
		*ran = append(*ran, path+" "+strings.Join(args, " "))
		if report == "" {
			return []byte("exec format error\n"), errors.New("exit status 1")
		}
		return []byte(report), nil
	}
	return app, &stdout, &stderr
}

// releaseFile writes a stand-in binary and returns its path and sha256.
func releaseFile(t *testing.T, content string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guildhall_linux_amd64")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return path, hex.EncodeToString(sum[:])
}

func TestReleaseInstallAndStatus(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	from, sum := releaseFile(t, "release v1.4.0")
	var ran []string
	report := `{"version":"v1.4.0","modified":false,"store_schema":1,"release_contract":1}`
	app, stdout, stderr := releaseTestApp(home, report, &ran)

	dir := filepath.Join(home, "releases", "v1.4.0")
	if code := app.Run([]string{"release", "install", "v1.4.0", "--from", from, "--sha256", sum}); code != 0 {
		t.Fatalf("release install = %d; stderr %s", code, stderr)
	}
	if want := "installed v1.4.0 (sha256 " + sum + ") at " + dir + "\n"; stdout.String() != want {
		t.Errorf("release install printed %q, want %q", stdout, want)
	}
	if len(ran) != 1 || !strings.HasSuffix(ran[0], " version --json") || !strings.HasPrefix(ran[0], filepath.Join(home, "releases", ".incoming")) {
		t.Errorf("ran %q, want one version --json of the copy under .incoming", ran)
	}

	stdout.Reset()
	if code := app.Run([]string{"release", "install", "--sha256", sum, "v1.4.0", "--from", from}); code != 0 {
		t.Fatalf("second release install = %d; stderr %s", code, stderr)
	}
	if want := "v1.4.0 is already installed (sha256 " + sum + ") at " + dir + "; nothing changed\n"; stdout.String() != want {
		t.Errorf("second release install printed %q, want %q", stdout, want)
	}

	if err := (release.Home{Dir: home}).Register(filepath.Join(home, "..", "copy"), time.Second); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := app.Run([]string{"release", "status"}); code != 0 {
		t.Fatalf("release status = %d; stderr %s", code, stderr)
	}
	want := "home: " + home + "\nreleases:\n  v1.4.0  sha256:" + sum[:12] + "  installed 2026-10-31T12:00:00Z  store schema 1  release contract 1\n" +
		"copies:\n  " + filepath.Join(filepath.Dir(home), "copy") + "\n"
	if stdout.String() != want {
		t.Errorf("release status =\n%s\nwant\n%s", stdout, want)
	}

	// Damage is reported, and status still exits 0.
	bin := filepath.Join(dir, release.BinaryName(runtime.GOOS))
	if err := os.WriteFile(bin, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := app.Run([]string{"release", "status", "--json"}); code != 0 {
		t.Fatalf("release status --json = %d; stderr %s", code, stderr)
	}
	var got releaseStatusReport
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatalf("release status --json printed %q: %v", stdout, err)
	}
	if got.Home != home || len(got.Releases) != 1 || !strings.Contains(got.Releases[0].Damage, "binary hashes to") || len(got.Copies) != 1 {
		t.Errorf("release status --json = %+v, want v1.4.0 damaged and one copy", got)
	}
	stdout.Reset()
	if code := app.Run([]string{"release", "status"}); code != 0 || !strings.Contains(stdout.String(), "  v1.4.0  damaged, never run: binary hashes to") {
		t.Errorf("release status = %d, %q; want v1.4.0 damaged", code, stdout)
	}
}

func TestReleaseStatusOfAnEmptyHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	var ran []string
	app, stdout, stderr := releaseTestApp(home, "", &ran)
	if code := app.Run([]string{"release", "status"}); code != 0 || stdout.String() != "home: "+home+"\nreleases: none\ncopies: none\n" {
		t.Errorf("release status = %d, %q, %q", code, stdout, stderr)
	}
	stdout.Reset()
	if code := app.Run([]string{"release", "status", "--json"}); code != 0 {
		t.Fatalf("release status --json = %d, %q", code, stderr)
	}
	homeJSON, _ := json.Marshal(home)
	if want := `{"home":` + string(homeJSON) + `,"releases":[],"copies":[]}` + "\n"; stdout.String() != want {
		t.Errorf("release status --json = %q, want %q", stdout, want)
	}
	// Status reads only: it never creates the home.
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("release status left %s: %v", home, err)
	}
}

func TestReleaseInstallRefusals(t *testing.T) {
	from, sum := releaseFile(t, "a build")
	cases := []struct {
		name, version, report, want string
	}{
		{"dev", "dev", `{"version":"dev"}`, `version "dev"`},
		{"reports dev", "v1.4.0", `{"version":"dev","release_contract":1}`, "reports version dev"},
		{"modified", "v1.4.0", `{"version":"v1.4.0","modified":true,"release_contract":1}`, "modified"},
		{"predates release installs", "v1.4.0", `{"version":"v1.4.0","modified":false}`, "predates release installs"},
		{"cannot run", "v1.4.0", "", "exec format error"},
		{"reports nothing", "v1.4.0", "not json", "no version reported"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			var ran []string
			app, stdout, stderr := releaseTestApp(home, c.report, &ran)
			code := app.Run([]string{"release", "install", c.version, "--from", from, "--sha256", sum})
			if code != 1 || stdout.String() != "" || !strings.HasPrefix(stderr.String(), "guildhall: release install: ") || !strings.Contains(stderr.String(), c.want) {
				t.Errorf("release install = %d, %q, %q; want exit 1 naming %q", code, stdout, stderr, c.want)
			}
			if entries, _ := os.ReadDir(filepath.Join(home, "releases")); len(entries) > 1 {
				t.Errorf("releases/ holds %v after a refusal", entries)
			}
		})
	}
	t.Run("wrong sha256", func(t *testing.T) {
		var ran []string
		app, _, stderr := releaseTestApp(filepath.Join(t.TempDir(), "home"), `{"version":"v1.4.0","release_contract":1}`, &ran)
		if code := app.Run([]string{"release", "install", "v1.4.0", "--from", from, "--sha256", strings.Repeat("0", 64)}); code != 1 || !strings.Contains(stderr.String(), "not the expected") || len(ran) != 0 {
			t.Errorf("release install with the wrong sha256 = %d, %q, ran %q; want exit 1 and nothing run", code, stderr, ran)
		}
	})
}

func TestReleaseUsage(t *testing.T) {
	for _, args := range [][]string{
		{"release"},
		{"release", "frobnicate"},
		{"release", "install"},
		{"release", "install", "v1.4.0"},
		{"release", "install", "v1.4.0", "--from", "x"},
		{"release", "install", "v1.4.0", "--sha256", "x"},
		{"release", "install", "v1.4.0", "v1.5.0", "--from", "x", "--sha256", "y"},
		{"release", "install", "--bogus"},
		{"release", "status", "extra"},
		{"release", "status", "--bogus"},
	} {
		var ran []string
		app, stdout, stderr := releaseTestApp(t.TempDir(), "", &ran)
		if code := app.Run(args); code != 2 || stdout.String() != "" || !strings.Contains(stderr.String(), "Usage: guildhall <command>") {
			t.Errorf("%q = %d, %q, %q; want a usage error", args, code, stdout, stderr)
		}
	}
	var ran []string
	app, _, _ := releaseTestApp(t.TempDir(), "", &ran)
	app.Stdout = failingWriter{}
	if code := app.Run([]string{"release", "status"}); code != 1 {
		t.Errorf("release status to an unwritable stdout = %d, want 1", code)
	}
}
