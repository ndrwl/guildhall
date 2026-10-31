package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// adoptedCopy is a version copy pinned to release v1.0.0, installed in a Guildhall home with the content
// "release v1.0.0", and an App whose GUILDHALL_HOME is that home and whose PATH guildhall is a launcher stand-in.
func adoptedCopy(t *testing.T) (dir string, h release.Home, pinnedSHA string, app *App) {
	t.Helper()
	dir = versionCopy(t, "git@github.com:example/app.git")
	h = release.Home{Dir: filepath.Join(t.TempDir(), "home"), GOOS: runtime.GOOS}
	from, sum := releaseFile(t, "release v1.0.0")
	probe := func(context.Context, string) (release.Report, error) {
		return release.Report{Version: "v1.0.0", ReleaseContract: 1}, nil
	}
	if _, err := h.Install(context.Background(), "v1.0.0", from, sum, probe, time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := release.WritePin(dir, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	launcherPath := filepath.Join(t.TempDir(), "guildhall")
	writeFile(t, launcherPath, "the launcher")
	app, _, _ = newTestApp(nil, nil)
	app.Getenv = func(k string) string {
		if k == release.HomeEnv {
			return h.Dir
		}
		return ""
	}
	app.LookPath = func(string) (string, error) { return launcherPath, nil }
	return dir, h, sum, app
}

// TestDirectorStatusInstallOnAnAdoptedCopy: on a copy with a release pin, the install item compares the pinned
// release, by its pseudo-version's commit, with main, and names the release commands that move it.
func TestDirectorStatusInstallOnAnAdoptedCopy(t *testing.T) {
	setVersion(t, "v1.2.3")
	const pin = "v0.0.0-20261020100000-0123456789ab"
	dir := versionCopy(t, "git@github.com:ndrwl/guildhall.git")
	withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
		_, r, err := s.RecordReleasePin(store.ReleasePin{Version: pin, SHA256: strings.Repeat("a", 64), Source: store.PinAdopt})
		return r, err
	})
	if err := release.WritePin(dir, pin); err != nil {
		t.Fatal(err)
	}
	g := &fakeVersionGit{ref: true, behind: []string{"y (#160)", "x (#159)"}}
	app, stdout, stderr := newTestApp(nil, nil)
	app.BuildInfo, app.VersionGit = testBuildInfo(nil), g
	if code := app.Run([]string{"director", "status", "--json", "--copy", dir}); code != 0 {
		t.Fatalf("director status = %d, %s", code, stderr)
	}
	var out struct {
		NeedsMe []needsMeItem `json:"needs_me"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := needsMeItem{Kind: installKind, Repository: "guildhall", Count: 2,
		Reason: "this copy's pinned release " + pin + " is 2 merges behind main (#160, #159); on the Guildmaster's word, install the new build with " +
			"guildhall release install <version> --from <file> --sha256 <hex>, pin it with guildhall release use <version>, then run guildhall director reload",
		Command: "guildhall release install"}
	if len(out.NeedsMe) != 1 || out.NeedsMe[0] != want {
		t.Errorf("needs_me = %+v, want only %+v", out.NeedsMe, want)
	}
	if !slices.Contains(g.calls, "behind 0123456789ab "+versionMainRef) {
		t.Errorf("git calls %q, want the pin's commit compared with main", g.calls)
	}
}

// TestDirectorStartTypesThePlainCommandOnAnAdoptedCopy: on a copy with a release pin, the Director's tab is typed
// plain guildhall with the explicit --copy, so the session starts, and restarts in that tab, through the PATH
// launcher on the copy's pin rather than on the binary that opened the tab.
func TestDirectorStartTypesThePlainCommandOnAnAdoptedCopy(t *testing.T) {
	dir := tabCopy(t, "/exit")
	withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
		_, r, err := s.RecordReleasePin(store.ReleasePin{Version: "v1.0.0", SHA256: strings.Repeat("a", 64), Source: store.PinAdopt})
		return r, err
	})
	if err := release.WritePin(dir, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	host := newDirectorTabs()
	typed := 0
	host.onRun = func(tab adapter.TabRef, line string) {
		argv := supervisorArgv(t, dir, tab)
		argv[0] = "guildhall"
		if want, err := herdr.ShellCommand("bash", argv); err != nil || line != want {
			t.Errorf("typed %q, want %q (%v)", line, want, err)
			return
		}
		typed++
		if code, out, stderr := runDirectorCmd(t, argv[2:]...); code != 0 {
			t.Errorf("supervisor in the tab: exit %d\n%s\n%s", code, out, stderr)
		}
	}
	code, out, stderr := runWrapper(t, host, "--tab", "--copy", dir)
	host.runs.Wait()
	if code != 0 || typed != 1 {
		t.Fatalf("start: exit %d, typed %d\nstdout %s\nstderr %s", code, typed, out, stderr)
	}
}

// TestAdoptedCopyComparesThePin: on a copy with a release pin, director status's supervisor_binary and the reload's
// binary decision compare with the pinned release, not the guildhall on PATH, which is the launcher; the reload
// pre-flights and moves onto the pinned binary, and refuses one that is missing or reports another version.
func TestAdoptedCopyComparesThePin(t *testing.T) {
	dir, h, pinned, app := adoptedCopy(t)
	for _, tt := range []struct {
		name, sha256 string
		differs      bool
		summary      string
	}{
		{"same", pinned, false, "session 1's supervisor runs the pinned release v1.0.0"},
		{"another", strings.Repeat("e", 64), true, "not the pinned release v1.0.0 " + h.Binary("v1.0.0")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := app.supervisorBinaryStatus(statusSession{Number: 1, BinarySHA256: tt.sha256}, dir)
			if b == nil || b.Differs != tt.differs || b.PathSHA256 != pinned || b.Path != h.Binary("v1.0.0") || !strings.Contains(b.Summary, tt.summary) {
				t.Errorf("supervisor_binary = %+v; want differs %v and %q", b, tt.differs, tt.summary)
			}
		})
	}
	var ran []string
	report := `{"version":"v1.0.0"}`
	app.Output = func(_ context.Context, path string, args ...string) ([]byte, error) {
		ran = append(ran, path+" "+strings.Join(args, " "))
		return []byte(report), nil
	}
	other := strings.Repeat("e", 64)
	if d := app.decideReexec("director", other, dir, ""); !d.Move || d.Pinned != "v1.0.0" || d.Binary.Path != h.Binary("v1.0.0") {
		t.Fatalf("decision %+v, want a move onto the pinned release", d)
	}
	if len(ran) != 2 || ran[0] != h.Binary("v1.0.0")+" version --json" || ran[1] != h.Binary("v1.0.0")+" director start --check --copy "+dir {
		t.Fatalf("pre-flight ran %q", ran)
	}
	if d := app.decideReexec("director", pinned, dir, ""); d.Move || d.Refused != (store.ReloadRefusal{}) {
		t.Fatalf("decision %+v, want none for the pinned binary itself", d)
	}
	report = `{"version":"v0.9.0"}`
	if d := app.decideReexec("director", other, dir, ""); d.Move || !strings.Contains(d.Refused.Reason, "reports version v0.9.0") {
		t.Fatalf("decision %+v, want a refusal of a mislabelled release", d)
	}
	if err := release.WritePin(dir, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	if d := app.decideReexec("director", other, dir, ""); d.Move || !strings.Contains(d.Refused.Reason, "pinned release v2.0.0") {
		t.Fatalf("decision %+v, want a refusal of a pin that is not installed", d)
	}
}
