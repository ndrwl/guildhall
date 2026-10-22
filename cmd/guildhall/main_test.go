package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestBinaryVersionFromLinkerFlag builds the real binary the way the release
// workflow does and checks the injected version reaches the version command.
func TestBinaryVersionFromLinkerFlag(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "guildhall")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command(gobin, "build", "-trimpath",
		"-ldflags", "-X github.com/ndrwl/guildhall/internal/cli.version=v0.0.0-test",
		"-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("guildhall version: %v\n%s", err, out)
	}
	if want := "guildhall v0.0.0-test " + runtime.GOOS + "/" + runtime.GOARCH + " "; !strings.HasPrefix(string(out), want) {
		t.Errorf("output = %q, want prefix %q", out, want)
	}

	testReleaseRendering(t, bin)
	t.Run("release baseline", func(t *testing.T) { testReleaseBaseline(t, bin) })

	// An empty PATH keeps doctor from running whatever tools the host has.
	doctor := exec.Command(bin, "doctor")
	doctor.Env = append(os.Environ(), "PATH="+t.TempDir())
	out, err = doctor.CombinedOutput()
	if err != nil {
		t.Fatalf("guildhall doctor: %v\n%s", err, out)
	}
	if want := "os: " + runtime.GOOS + "\n"; !strings.HasPrefix(string(out), want) {
		t.Errorf("doctor output = %q, want prefix %q", out, want)
	}
	if want := "git: not found on PATH\n"; !strings.Contains(string(out), want) {
		t.Errorf("doctor output = %q, want %q", out, want)
	}

	if err := exec.Command(bin, "frobnicate").Run(); err == nil {
		t.Error("unknown command exited 0, want exit status 2")
	} else if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 2 {
		t.Errorf("unknown command error = %v, want exit status 2", err)
	}
}
