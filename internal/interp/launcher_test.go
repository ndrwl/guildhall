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
	t.Setenv(CodexStubEnv, "")
	for _, name := range []string{MockAdapter, FakeAdapter} {
		if err := CheckAdapter(name); err != nil {
			t.Errorf("CheckAdapter(%q) = %v, want allowed", name, err)
		}
	}
	for _, name := range []string{"claude", "codex", "gemini", "", "Mock"} {
		if err := CheckAdapter(name); !errors.Is(err, ErrAdapterRefused) {
			t.Errorf("CheckAdapter(%q) = %v, want ErrAdapterRefused", name, err)
		}
	}
}

// TestCheckAdapterAllowsOnlyAStubbedClaude: under test, claude is allowed only while the claude on
// PATH is the stub ClaudeStubEnv names, so a real Claude Code is never run.
func TestCheckAdapterAllowsOnlyAStubbedClaude(t *testing.T) {
	testCheckAdapterAllowsOnlyAStub(t, ClaudeAdapter, ClaudeStubEnv)
}

// TestCheckAdapterAllowsOnlyAStubbedCodex: under test, codex is allowed only while the codex on PATH is the stub
// CodexStubEnv names, so a real Codex CLI is never run; a stubbed claude does not admit it.
func TestCheckAdapterAllowsOnlyAStubbedCodex(t *testing.T) {
	testCheckAdapterAllowsOnlyAStub(t, CodexAdapter, CodexStubEnv)
}

func testCheckAdapterAllowsOnlyAStub(t *testing.T, program, env string) {
	other := map[string]string{ClaudeStubEnv: CodexStubEnv, CodexStubEnv: ClaudeStubEnv}[env]
	name := program
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
		{"real program ahead of the stub", realDir + string(os.PathListSeparator) + stubDir, stub, false},
		{"relative stub", stubDir, name, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", tc.path)
			t.Setenv(env, tc.env)
			t.Setenv(other, "")
			if err := CheckAdapter(program); (err == nil) != tc.allowed || err != nil && !errors.Is(err, ErrAdapterRefused) {
				t.Errorf("CheckAdapter(%s) = %v, want allowed %v", program, err, tc.allowed)
			}
		})
	}
	t.Run("the other runtime's stub", func(t *testing.T) {
		t.Setenv("PATH", stubDir)
		t.Setenv(env, "")
		t.Setenv(other, stub)
		if err := CheckAdapter(program); !errors.Is(err, ErrAdapterRefused) {
			t.Errorf("CheckAdapter(%s) with only %s set = %v, want ErrAdapterRefused", program, other, err)
		}
	})
}

func TestCheckAdapterOutsideTests(t *testing.T) {
	for _, name := range []string{"claude", "codex", MockAdapter, FakeAdapter} {
		if err := checkAdapter(name, false); err != nil {
			t.Errorf("checkAdapter(%q, outside tests) = %v, want allowed", name, err)
		}
	}
}
