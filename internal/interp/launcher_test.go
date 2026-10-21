package interp

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckAdapterUnderTest(t *testing.T) {
	t.Setenv(ClaudeStubEnv, "")
	for _, name := range []string{MockAdapter, FakeAdapter} {
		if err := CheckAdapter(name); err != nil {
			t.Errorf("CheckAdapter(%q) = %v, want allowed", name, err)
		}
	}
	for _, name := range []string{"claude", "codex", "", "Mock"} {
		if err := CheckAdapter(name); !errors.Is(err, ErrAdapterRefused) {
			t.Errorf("CheckAdapter(%q) = %v, want ErrAdapterRefused", name, err)
		}
	}
}

// TestCheckAdapterAllowsOnlyAStubbedClaude: under test, claude is allowed only while the claude on
// PATH is the stub ClaudeStubEnv names, so a real Claude Code is never run.
func TestCheckAdapterAllowsOnlyAStubbedClaude(t *testing.T) {
	name := ClaudeAdapter
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	stubDir, realDir := t.TempDir(), t.TempDir()
	stub, real := filepath.Join(stubDir, name), filepath.Join(realDir, name)
	for _, p := range []string{stub, real} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, path, env string
		allowed         bool
	}{
		{"stub on PATH", stubDir, stub, true},
		{"no stub named", stubDir, "", false},
		{"real claude ahead of the stub", realDir + string(os.PathListSeparator) + stubDir, stub, false},
		{"relative stub", stubDir, name, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", tc.path)
			t.Setenv(ClaudeStubEnv, tc.env)
			if err := CheckAdapter(ClaudeAdapter); (err == nil) != tc.allowed || err != nil && !errors.Is(err, ErrAdapterRefused) {
				t.Errorf("CheckAdapter(claude) = %v, want allowed %v", err, tc.allowed)
			}
		})
	}
}

func TestCheckAdapterOutsideTests(t *testing.T) {
	for _, name := range []string{"claude", MockAdapter, FakeAdapter} {
		if err := checkAdapter(name, false); err != nil {
			t.Errorf("checkAdapter(%q, outside tests) = %v, want allowed", name, err)
		}
	}
}
