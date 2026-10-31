package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAdoptThroughTheLauncher adopts a fresh copy with real binaries: a version-stamped guildhall on PATH with the
// guildhall-launcher `go install` puts beside it. Adopt installs the guildhall as the copy's first release and puts
// the launcher on PATH in its place, keeping the old binary; the plain guildhall then runs the pinned release; a
// second adopt, through the launcher, changes nothing; and the release refuses a GUILDHALL_RELEASE that is not its own.
func TestAdoptThroughTheLauncher(t *testing.T) {
	pathDir := t.TempDir()
	const version = "v0.0.0-adopt-test"
	bin, _ := buildRelease(t, pathDir, version)
	launcherBin := filepath.Join(pathDir, "guildhall-launcher")
	if runtime.GOOS == "windows" {
		launcherBin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-trimpath", "-o", launcherBin, "../guildhall-launcher").CombinedOutput(); err != nil {
		t.Fatalf("go build launcher: %v\n%s", err, out)
	}
	copyDir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	env := append(os.Environ(), "GUILDHALL_HOME="+home, "PATH="+pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	run := func(exe string, extra []string, args ...string) (string, error) {
		t.Helper()
		cmd := binCommand(t, exe, args...)
		cmd.Env = append(append([]string{}, env...), extra...)
		out, err := cmd.CombinedOutput()
		t.Logf("%s %s: %v\n%s", filepath.Base(exe), strings.Join(args, " "), err, out)
		return string(out), err
	}
	if _, err := run(bin, nil, "store", "init", filepath.Join(copyDir, "director.store")); err != nil {
		t.Fatal(err)
	}
	if out, err := run(bin, nil, "release", "adopt", "--copy", copyDir); err != nil || !strings.Contains(out, "replaced "+bin+" with the launcher") {
		t.Fatalf("release adopt: %v\n%s", err, out)
	}
	backup := filepath.Join(pathDir, "guildhall.pre-adopt")
	if runtime.GOOS == "windows" {
		backup += ".exe"
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("no backup of the old binary: %v", err)
	}
	out, err := run(bin, nil, "version", "--json", "--copy", copyDir)
	var v struct {
		Version string `json:"version"`
	}
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || v.Version != version {
		t.Fatalf("the launcher on PATH ran %q: %v", out, err)
	}
	if out, err := run(bin, nil, "release", "adopt", "--copy", copyDir); err != nil || !strings.Contains(out, "already the launcher") || !strings.Contains(out, "already pins "+version) {
		t.Fatalf("second release adopt: %v\n%s", err, out)
	}
	if out, err := run(bin, nil, "release", "status", "--copy", copyDir); err != nil || !strings.Contains(out, "copy: "+copyDir+" pins "+version) {
		t.Fatalf("release status: %v\n%s", err, out)
	}
	installed := filepath.Join(home, "releases", version, filepath.Base(bin))
	if out, err := run(installed, []string{"GUILDHALL_RELEASE=v9.9.9"}, "version"); err == nil || !strings.Contains(out, "GUILDHALL_RELEASE=v9.9.9") {
		t.Fatalf("the release ran under another GUILDHALL_RELEASE: %v\n%s", err, out)
	}
}
