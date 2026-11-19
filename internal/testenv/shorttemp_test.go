package testenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempRecorder is a TempT whose TempDir is a fixed directory and whose
// cleanups are recorded, so a test can run them and look at what is left.
type tempRecorder struct {
	dir      string
	cleanups []func()
}

func (r *tempRecorder) Helper()          {}
func (r *tempRecorder) TempDir() string  { return r.dir }
func (r *tempRecorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

func (r *tempRecorder) runCleanups() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
	r.cleanups = nil
}

// noSpaceOrTilde stands in for the Steward roster's Windows copy_dir rule.
func noSpaceOrTilde(slashDir string) error {
	if strings.ContainsAny(slashDir, " ~") {
		return errors.New("has a space or a ~")
	}
	return nil
}

// unsetRunnerTemp unsets RUNNER_TEMP for the test, restoring it afterwards.
func unsetRunnerTemp(t *testing.T) {
	t.Setenv("RUNNER_TEMP", "")
	os.Unsetenv("RUNNER_TEMP")
}

// badDir makes a directory whose path has a space and a ~, as a Windows
// profile's temp directory such as C:/Users/RUNNER~1/... has.
func badDir(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "First Last", "RUNNER~1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestShortTempDirUsesRunnerTemp(t *testing.T) {
	runner := t.TempDir()
	t.Setenv("RUNNER_TEMP", runner)
	// The recorder's TempDir stands in for a TMPDIR holding a space and a ~;
	// ShortTempDir reads t.TempDir, not TMPDIR.
	rec := &tempRecorder{dir: badDir(t)}
	dir, err := ShortTempDir(rec, noSpaceOrTilde)
	if err != nil {
		t.Fatalf("ShortTempDir: %v", err)
	}
	if filepath.Dir(dir) != runner {
		t.Fatalf("got %s, want a new directory under RUNNER_TEMP %s", dir, runner)
	}
	if err := noSpaceOrTilde(filepath.ToSlash(dir)); err != nil {
		t.Fatalf("got %s, which fits refuses: %v", dir, err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("%s is not a directory: %v", dir, err)
	}
	again, err := ShortTempDir(rec, noSpaceOrTilde)
	if err != nil || again == dir {
		t.Fatalf("a second call gave %s, %v; want a different new directory than %s", again, err, dir)
	}
	rec.runCleanups()
	for _, d := range []string{dir, again} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("%s survived cleanup: %v", d, err)
		}
	}
}

func TestShortTempDirSkipsARunnerTempThatDoesNotFit(t *testing.T) {
	t.Setenv("RUNNER_TEMP", badDir(t))
	clean := t.TempDir()
	rec := &tempRecorder{dir: clean}
	dir, err := ShortTempDir(rec, noSpaceOrTilde)
	if err != nil {
		t.Fatalf("ShortTempDir: %v", err)
	}
	if dir != clean {
		t.Fatalf("got %s, want the test's temp dir %s", dir, clean)
	}
	rec.runCleanups()
}

func TestShortTempDirUsesTempDirWithoutRunnerTemp(t *testing.T) {
	unsetRunnerTemp(t)
	clean := t.TempDir()
	rec := &tempRecorder{dir: clean}
	dir, err := ShortTempDir(rec, noSpaceOrTilde)
	if err != nil {
		t.Fatalf("ShortTempDir: %v", err)
	}
	if dir != clean {
		t.Fatalf("got %s, want the test's temp dir %s", dir, clean)
	}
}

func TestShortTempDirResolvesALinkedTempDir(t *testing.T) {
	unsetRunnerTemp(t)
	target := t.TempDir()
	link := filepath.Join(badDir(t), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic link: %v", err)
	}
	want, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	rec := &tempRecorder{dir: link}
	dir, err := ShortTempDir(rec, noSpaceOrTilde)
	if err != nil {
		t.Fatalf("ShortTempDir: %v", err)
	}
	if dir != want {
		t.Fatalf("got %s, want the link's target %s", dir, want)
	}
}

func TestShortTempDirNamesEveryCandidate(t *testing.T) {
	runner := badDir(t)
	t.Setenv("RUNNER_TEMP", runner)
	bad := badDir(t)
	rec := &tempRecorder{dir: bad}
	dir, err := ShortTempDir(rec, noSpaceOrTilde)
	if err == nil {
		t.Fatalf("got %s, want an error", dir)
	}
	msg := err.Error()
	for _, want := range []string{filepath.ToSlash(runner), filepath.ToSlash(bad), "has a space or a ~"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}
	if n := strings.Count(msg, "has a space or a ~"); n != 3 {
		t.Errorf("error %q gives fits' reason %d times, want 3 (RUNNER_TEMP, TempDir, its resolved form)", msg, n)
	}
	rec.runCleanups()
	entries, err := os.ReadDir(runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused RUNNER_TEMP candidate survived cleanup: %v", entries)
	}
}
