package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildRelease builds the binary into dir, stamped with version by the linker flag when it is not "", without vcs
// information so a checkout with local changes still builds an unmodified binary, and returns its path and sha256.
func buildRelease(t *testing.T, dir, version string) (string, string) {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not on PATH")
	}
	bin := filepath.Join(dir, "guildhall")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	args := []string{"build", "-trimpath", "-buildvcs=false"}
	if version != "" {
		args = append(args, "-ldflags", "-X github.com/ndrwl/guildhall/internal/cli.version="+version)
	}
	if out, err := exec.Command(gobin, append(args, "-o", bin, ".")...).CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return bin, hex.EncodeToString(sum[:])
}

// TestReleaseInstallOfABuiltBinary installs a version-stamped build of the real binary into a temporary home with
// the binary itself, runs the installed release, and sees a plain go build, which reports dev, refused.
func TestReleaseInstallOfABuiltBinary(t *testing.T) {
	bin, sum := buildRelease(t, t.TempDir(), "v0.0.0-release-test")
	// The installed release finds its home from its symlink-resolved path, so the home is resolved too: macOS's
	// temporary directory is under /var, a link to /private/var.
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	run := func(bin string, args ...string) (string, error) {
		cmd := binCommand(t, bin, args...)
		cmd.Env = append(os.Environ(), "GUILDHALL_HOME="+home)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run(bin, "version", "--json")
	var report struct {
		Version         string `json:"version"`
		Modified        bool   `json:"modified"`
		StoreSchema     int    `json:"store_schema"`
		ReleaseContract int    `json:"release_contract"`
	}
	if err != nil || json.Unmarshal([]byte(out), &report) != nil || report.Version != "v0.0.0-release-test" || report.Modified ||
		report.StoreSchema < 1 || report.ReleaseContract != 1 {
		t.Fatalf("version --json = %q, %v", out, err)
	}

	if out, err := run(bin, "release", "install", "v0.0.0-release-test", "--from", bin, "--sha256", sum); err != nil || !strings.HasPrefix(out, "installed v0.0.0-release-test ") {
		t.Fatalf("release install = %q, %v", out, err)
	}
	installed := filepath.Join(home, "releases", "v0.0.0-release-test", filepath.Base(bin))
	if out, err := run(installed, "version"); err != nil || !strings.HasPrefix(out, "guildhall v0.0.0-release-test ") {
		t.Errorf("installed release's version = %q, %v", out, err)
	}
	// The installed release finds its home from its own path when GUILDHALL_HOME is unset.
	status := binCommand(t, installed, "release", "status")
	status.Env = append(os.Environ(), "GUILDHALL_HOME=")
	if out, err := status.CombinedOutput(); err != nil || !strings.HasPrefix(string(out), "home: "+home+"\n") ||
		!strings.Contains(string(out), "  v0.0.0-release-test  sha256:"+sum[:12]+"  installed ") {
		t.Errorf("installed release's release status = %q, %v", out, err)
	}

	dev, devSum := buildRelease(t, t.TempDir(), "")
	if out, err := run(dev, "release", "install", "v0.0.0-dev-build", "--from", dev, "--sha256", devSum); err == nil || !strings.Contains(out, "reports version dev") {
		t.Errorf("release install of a plain go build = %q, %v; want a refusal", out, err)
	}
}
