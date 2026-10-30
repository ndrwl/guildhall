package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// newCopy returns a Director copy directory (docs/running.md#copies): config.toml with its workflows, an empty
// director.store made by store init and state/. It has no copy marker until a command names it with --copy or
// markCopy gives it one.
func newCopy(t *testing.T) string {
	t.Helper()
	dir := bareCopy(t)
	app, _, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"store", "init", filepath.Join(dir, "director.store")}); code != 0 {
		t.Fatalf("store init: exit %d, stderr %s", code, stderr)
	}
	approveRepo(t, filepath.Join(dir, "director.store"), filepath.Join(dir, "config.toml"), "app")
	return dir
}

// bareCopy is a copy directory with its config and state but no store yet.
func bareCopy(t *testing.T) string {
	t.Helper()
	dir := filepath.Dir(writeDirector(t, directorTOML))
	if err := os.Mkdir(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// markCopy gives dir its copy marker, as a first use with --copy does, and returns dir.
func markCopy(t *testing.T, dir string) string {
	t.Helper()
	writeCopyMarker(nil, dir)
	if err := readCopyMarker(filepath.Join(dir, copyMarkerFile)); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runIn runs the CLI with wd as its working directory, a new empty directory when wd is empty, and nothing in the
// environment.
func runIn(t *testing.T, wd string, args ...string) (int, string, string) {
	t.Helper()
	if wd == "" {
		wd = t.TempDir()
	}
	app, stdout, stderr := newTestApp(nil, nil)
	app.Getwd = func() (string, error) { return wd, nil }
	code := app.Run(args)
	return code, stdout.String(), stderr.String()
}

// noCopyLine is the one refusal for a command that found no copy from wd.
func noCopyLine(wd string) string {
	return "no Guildhall copy found from " + wd + "; pass --copy <dir> or cd into a copy"
}

// The copy form reads the same store as naming the copy's store directly, and task new takes the copy's config.
func TestCopySelectsStoreAndConfig(t *testing.T) {
	dir := newCopy(t)
	code, _, stderr := runIn(t, "", "task", "new", "--copy", dir, "--repo", "app", "--title", "From the copy", "t-1")
	if code != 0 {
		t.Fatalf("task new --copy: exit %d, stderr %s", code, stderr)
	}
	if got, want := readTask(t, filepath.Join(dir, "director.store"), "app", "t-1").ConfigDigest, configDigest(t, filepath.Join(dir, "config.toml")); got != want {
		t.Errorf("config digest %s, want the copy's %s", got, want)
	}
	_, want, _ := runTask(t, "list", "--store", filepath.Join(dir, "director.store"), "--json")
	code, got, stderr := runIn(t, "", "task", "list", "--copy", dir, "--json")
	if code != 0 || got != want {
		t.Errorf("task list --copy: exit %d, stdout %q, stderr %s; want %q", code, got, stderr, want)
	}
	// An explicit flag naming the copy's own file agrees with it.
	code, got, stderr = runIn(t, "", "task", "list", "--copy", dir, "--store", filepath.Join(dir, ".", "director.store"), "--json")
	if code != 0 || got != want {
		t.Errorf("task list --copy --store <copy's>: exit %d, stdout %q, stderr %s; want %q", code, got, stderr, want)
	}
}

// With a copy marker at the copy root, a command run from the root, a subdirectory or a session directory finds
// the copy with no --copy, and --copy overrides it.
func TestCopyDiscovery(t *testing.T) {
	dir := markCopy(t, newCopy(t))
	other := newCopy(t)
	if code, _, stderr := runIn(t, "", "task", "new", "--copy", dir, "--repo", "app", "--title", "Only in dir", "t-1"); code != 0 {
		t.Fatalf("task new: exit %d, stderr %s", code, stderr)
	}
	_, inDir, _ := runIn(t, "", "task", "list", "--copy", dir, "--json")
	_, inOther, _ := runIn(t, "", "task", "list", "--copy", other, "--json")
	if inDir == inOther {
		t.Fatalf("copies list the same tasks: %q", inDir)
	}
	session := filepath.Join(dir, "director", "session-3")
	if err := os.MkdirAll(session, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, wd := range []string{dir, filepath.Join(dir, "workflows"), session, session + string(filepath.Separator) + ".."} {
		if code, got, stderr := runIn(t, wd, "task", "list", "--json"); code != 0 || got != inDir {
			t.Errorf("from %s: exit %d, stdout %q, stderr %s; want %q", wd, code, got, stderr, inDir)
		}
	}
	if code, got, stderr := runIn(t, session, "task", "list", "--copy", other, "--json"); code != 0 || got != inOther {
		t.Errorf("--copy over discovery: exit %d, stdout %q, stderr %s; want %q", code, got, stderr, inOther)
	}
}

// The nearest marker wins: a copy inside another copy's directory is found from inside it.
func TestCopyDiscoveryNearestMarker(t *testing.T) {
	outer := markCopy(t, newCopy(t))
	inner := filepath.Join(outer, "inner")
	if err := os.Rename(markCopy(t, newCopy(t)), inner); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runIn(t, "", "task", "new", "--copy", inner, "--repo", "app", "--title", "Inner", "t-1"); code != 0 {
		t.Fatalf("task new: exit %d, stderr %s", code, stderr)
	}
	_, want, _ := runIn(t, "", "task", "list", "--copy", inner, "--json")
	_, outerList, _ := runIn(t, "", "task", "list", "--copy", outer, "--json")
	if want == outerList {
		t.Fatalf("copies list the same tasks: %q", want)
	}
	if code, got, stderr := runIn(t, filepath.Join(inner, "workflows"), "task", "list", "--json"); code != 0 || got != want {
		t.Errorf("from inside the inner copy: exit %d, stdout %q, stderr %s; want the inner copy's %q", code, got, stderr, want)
	}
}

// A start at or under the copy's state/, where every Artisan works, finds no copy and never opens its store, and
// the search does not go on past that copy.
func TestCopyDiscoveryNotUnderState(t *testing.T) {
	outer := markCopy(t, bareCopy(t))
	dir := filepath.Join(outer, "nested")
	if err := os.Rename(markCopy(t, bareCopy(t)), dir); err != nil {
		t.Fatal(err)
	}
	for _, wd := range []string{filepath.Join(dir, "state"), filepath.Join(dir, "state", "x", "work")} {
		if err := os.MkdirAll(wd, 0o700); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := runIn(t, wd, "task", "list", "--json")
		if code != 2 || stdout != "" || !strings.Contains(stderr, noCopyLine(wd)) || strings.Contains(stderr, "store init") {
			t.Errorf("from %s: exit %d, stdout %q, stderr %q; want the no-copy usage error", wd, code, stdout, stderr)
		}
		if code, _, stderr := runIn(t, wd, "director", "status", "--json"); code != 2 || !strings.Contains(stderr, noCopyLine(wd)) {
			t.Errorf("director status from %s: exit %d, stderr %q; want the no-copy usage error", wd, code, stderr)
		}
	}
	for _, d := range []string{dir, outer} {
		if _, err := os.Stat(filepath.Join(d, "director.store")); !os.IsNotExist(err) {
			t.Errorf("a command reached the store of %s: %v", d, err)
		}
	}
	// A directory merely named state elsewhere in the copy is not the copy's state/.
	other := filepath.Join(dir, "workflows", "state")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runIn(t, other, "task", "list", "--json"); code != 1 || !strings.Contains(stderr, "store init "+filepath.Join(dir, "director.store")) {
		t.Errorf("from %s: exit %d, stderr %q; want the copy found and its missing store named", other, code, stderr)
	}
}

// With no marker up to the filesystem root, a command prints the one no-copy line naming where it searched from.
func TestCopyDiscoveryNoCopy(t *testing.T) {
	wd := t.TempDir()
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"director", "start"}, 2},
		{[]string{"director", "status", "--json"}, 2},
		{[]string{"director", "turn-ended"}, 1},
		{[]string{"director", "context-report"}, 1},
		{[]string{"director", "wait-wake"}, 1},
		{[]string{"quality", "judge", "--repo", "app", "--review", "1", "t-1"}, 2},
		{[]string{"artisan", "start", "--adapter", "fake", "--prompt", "p", "--workdir", wd, "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1", "--advance"}, 2},
		{[]string{"task", "list", "--json"}, 2},
		{[]string{"store", "doctor"}, 2},
	} {
		code, stdout, stderr := runIn(t, wd, tc.args...)
		if code != tc.code || stdout != "" || !strings.Contains(stderr, noCopyLine(wd)) {
			t.Errorf("%q: exit %d, stdout %q, stderr %q; want exit %d with %q", tc.args, code, stdout, stderr, tc.code, noCopyLine(wd))
		}
	}
}

// An explicit --store, --config or --state outside the copy given with --copy is a usage error.
func TestCopyDisagreementRefused(t *testing.T) {
	dir := newCopy(t)
	elsewhere := t.TempDir()
	outside := filepath.Join(elsewhere, "director.store")
	for _, tc := range []struct {
		name, flag string
		args       []string
	}{
		{"store", "--store", []string{"task", "list", "--copy", dir, "--store", outside, "--json"}},
		{"store doctor path", "store", []string{"store", "doctor", "--copy", dir, outside}},
		{"config", "--config", []string{"task", "new", "--copy", dir, "--config", filepath.Join(elsewhere, "config.toml"), "--repo", "app", "--title", "T", "t-1"}},
		{"state", "--state", []string{"artisan", "status", "--copy", dir, "--state", elsewhere, "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1"}},
		{"advance state", "--state", []string{"task", "start", "--copy", dir, "--state", filepath.Join(dir, "state", "x"), "--repo", "app", "t-1"}},
	} {
		code, stdout, stderr := runIn(t, "", tc.args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, tc.flag) || !strings.Contains(stderr, "copy") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want a usage error naming %s and the copy", tc.name, code, stdout, stderr, tc.flag)
		}
	}
}

// An explicit path flag without --copy bypasses copy selection, so a launched child given another copy's paths
// keeps them even when it runs inside a copy.
func TestCopyDiscoveryOverriddenByPathFlag(t *testing.T) {
	here := markCopy(t, newCopy(t))
	given := newCopy(t)
	if code, _, stderr := runIn(t, "", "task", "new", "--copy", given, "--repo", "app", "--title", "Only in given", "t-1"); code != 0 {
		t.Fatalf("task new: exit %d, stderr %s", code, stderr)
	}
	_, want, _ := runIn(t, "", "task", "list", "--copy", given, "--json")
	if code, got, stderr := runIn(t, here, "task", "list", "--store", filepath.Join(given, "director.store"), "--json"); code != 0 || got != want {
		t.Errorf("--store inside another copy: exit %d, stdout %q, stderr %s; want %q", code, got, stderr, want)
	}
	code, _, stderr := runIn(t, here, "artisan", "status", "--store", filepath.Join(given, "director.store"), "--state", filepath.Join(given, "state"),
		"--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1")
	if code == 2 || strings.Contains(stderr, "copy") {
		t.Errorf("artisan status --store --state inside another copy: exit %d, stderr %q; want no usage error about the copy", code, stderr)
	}
	// A partial set of explicit paths stays an explicit-path command: its other path flags are still required.
	if code, _, stderr := runIn(t, here, "director", "status", "--json", "--store", filepath.Join(given, "director.store")); code != 2 || !strings.Contains(stderr, "--store and --state are required") {
		t.Errorf("director status --store inside a copy: exit %d, stderr %q; want --state still required", code, stderr)
	}
}

// The environment never selects a copy: with every variable naming a copy, a command outside any copy finds none.
func TestCopyIgnoresEnvironment(t *testing.T) {
	dir := markCopy(t, newCopy(t))
	wd := t.TempDir()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Getwd = func() (string, error) { return wd, nil }
	app.Getenv = func(string) string { return dir }
	if code := app.Run([]string{"task", "list", "--json"}); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), noCopyLine(wd)) {
		t.Errorf("exit %d, stdout %q, stderr %q; want the no-copy usage error", code, stdout, stderr)
	}
}

// A marker that is not JSON, or has an unknown schema or version, or cannot be read, is an error naming it, and
// discovery does not go past it to an outer copy.
func TestCopyMalformedMarker(t *testing.T) {
	outer := markCopy(t, newCopy(t))
	dir := filepath.Join(outer, "bad")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, copyMarkerFile)
	for _, body := range []string{"not json", `{"schema":"guildhall.copy","version":2,"name":"bad"}`, `{"schema":"other","version":1,"name":"bad"}`} {
		writeFile(t, marker, body)
		code, stdout, stderr := runIn(t, dir, "task", "list", "--json")
		if code != 2 || stdout != "" || !strings.Contains(stderr, marker) {
			t.Errorf("marker %q: exit %d, stdout %q, stderr %q; want a usage error naming %s", body, code, stdout, stderr, marker)
		}
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(marker, 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runIn(t, dir, "task", "list", "--json"); code != 2 || !strings.Contains(stderr, marker) {
		t.Errorf("unreadable marker: exit %d, stderr %q; want a usage error naming %s", code, stderr, marker)
	}
}

// A command given --copy writes the copy's marker, named for its directory, when it has none, and leaves an
// existing marker byte for byte; store init --copy gives a new copy its marker.
func TestCopyMarkerWritten(t *testing.T) {
	dir := newCopy(t)
	marker := filepath.Join(dir, copyMarkerFile)
	if code, _, stderr := runIn(t, "", "task", "list", "--copy", dir, "--json"); code != 0 || stderr != "" {
		t.Fatalf("task list --copy: exit %d, stderr %q", code, stderr)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var m copyMarker
	if err := json.Unmarshal(b, &m); err != nil || m != (copyMarker{Schema: "guildhall.copy", Version: 1, Name: filepath.Base(dir)}) {
		t.Errorf("marker %s = %+v, %v; want the copy named %q", b, m, err, filepath.Base(dir))
	}
	kept := `{"schema":"guildhall.copy","version":1,"name":"renamed by hand"}` + "  \n"
	writeFile(t, marker, kept)
	if code, _, stderr := runIn(t, "", "task", "list", "--copy", dir, "--json"); code != 0 {
		t.Fatalf("task list --copy: exit %d, stderr %q", code, stderr)
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != kept {
		t.Errorf("existing marker became %q, %v; want it unchanged", b, err)
	}

	fresh := bareCopy(t)
	if code, _, stderr := runIn(t, "", "store", "init", "--copy", fresh); code != 0 {
		t.Fatalf("store init --copy: exit %d, stderr %q", code, stderr)
	}
	if err := readCopyMarker(filepath.Join(fresh, copyMarkerFile)); err != nil {
		t.Errorf("store init --copy: %v", err)
	}
	if code, _, stderr := runIn(t, fresh, "store", "doctor"); code != 0 {
		t.Errorf("store doctor in the new copy: exit %d, stderr %q", code, stderr)
	}
	// A bare store init cannot tell it is making a copy, so it writes no marker.
	plain := bareCopy(t)
	if code, _, stderr := runIn(t, "", "store", "init", filepath.Join(plain, "director.store")); code != 0 {
		t.Fatalf("store init: exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(plain, copyMarkerFile)); !os.IsNotExist(err) {
		t.Errorf("bare store init wrote a marker: %v", err)
	}
}

// A copy whose marker cannot be written gets one warning line, and the command still succeeds.
func TestCopyMarkerUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test cannot write")
	}
	dir := newCopy(t)
	_, want, _ := runIn(t, "", "task", "list", "--store", filepath.Join(dir, "director.store"), "--json")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	code, got, stderr := runIn(t, "", "task", "list", "--copy", dir, "--json")
	if code != 0 || got != want || strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "copy marker") {
		t.Errorf("exit %d, stdout %q, stderr %q; want %q and one warning line", code, got, stderr, want)
	}
	if _, err := os.Stat(filepath.Join(dir, copyMarkerFile)); !os.IsNotExist(err) {
		t.Errorf("marker written: %v", err)
	}
}

// Every store-taking command accepts --copy and copy discovery: against a copy without a store each reaches the
// store, refuses and names store init.
func TestCopyEveryStoreCommandNamesStoreInit(t *testing.T) {
	dir := markCopy(t, bareCopy(t))
	result := filepath.Join(t.TempDir(), "result.json")
	writeFile(t, result, "{}")
	attempt := []string{"--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1"}
	for _, args := range [][]string{
		{"task", "new", "--repo", "app", "--title", "T", "t-1"},
		{"task", "show", "--repo", "app", "--json", "t-1"},
		{"task", "list", "--json"},
		{"task", "timeline", "--repo", "app", "--json", "t-1"},
		{"task", "files", "--repo", "app", "--json", "t-1"},
		{"task", "finding", "rule", "--repo", "app", "--outcome", "o", "--finding", "f", "--move", "defer", "--reason", "r", "t-1"},
		{"task", "review", "--full", "--repo", "app", "t-1"},
		{"task", "start", "--repo", "app", "t-1"},
		append([]string{"step", "retry"}, attempt...),
		{"decide", "list"},
		{"decide", "show", "--repo", "app", "--task", "t-1", "--decision", "d"},
		{"messages", "list", "director"},
		{"messages", "read", "m-1"},
		{"workflow", "approvals", "--json"},
		{"workflow", "approve", "--repo", "app", "--words-file", result},
		append([]string{"artisan", "status"}, attempt...),
		{"store", "doctor"},
	} {
		want := "guildhall store init " + filepath.Join(dir, "director.store")
		for _, form := range []struct {
			name, wd string
			args     []string
		}{
			{"--copy", "", append(append([]string{}, args...), "--copy", dir)},
			{"discovery", filepath.Join(dir, "workflows"), args},
		} {
			code, stdout, stderr := runIn(t, form.wd, form.args...)
			if code != 1 || stdout != "" || !strings.Contains(stderr, want) {
				t.Errorf("%q with %s: exit %d, stdout %q, stderr %q; want a refusal naming %q", args, form.name, code, stdout, stderr, want)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "director.store")); !os.IsNotExist(err) {
		t.Errorf("a command created the copy's store: %v", err)
	}
	// step done reads its result before the store, so it shows its state came from the copy by naming the
	// attempt's result file there.
	want := filepath.Join(artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}.Dir(filepath.Join(dir, "state")), artisan.ResultFile)
	code, _, stderr := runIn(t, dir, append([]string{"step", "done", "--result", result}, attempt...)...)
	if code != 1 || !strings.Contains(stderr, want) {
		t.Errorf("step done: exit %d, stderr %q; want a refusal naming %s", code, stderr, want)
	}
}

// Without a copy the path flags stay required.
func TestNoCopyKeepsFlagsRequired(t *testing.T) {
	for _, args := range [][]string{
		{"task", "list", "--json"},
		{"task", "start", "--repo", "app", "t-1"},
		{"decide", "list"},
		{"messages", "list", "director"},
		{"workflow", "approvals"},
		{"artisan", "status", "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1"},
		{"store", "doctor"},
	} {
		if code, _, stderr := runIn(t, "", args...); code != 2 || !strings.Contains(strings.ToLower(stderr), "usage:") || !strings.Contains(stderr, "no Guildhall copy found") {
			t.Errorf("%q: exit %d, stderr %q; want a usage error with the no-copy line", args, code, stderr)
		}
	}
}
