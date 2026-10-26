package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/git"
)

func TestFinalHead(t *testing.T) {
	global := isolate(t)
	ctx := context.Background()
	r := repo{t, t.TempDir(), global}
	r.run("init", "-q")
	if _, err := (git.Program{}).FinalHead(ctx, r.dir); err == nil {
		t.Fatal("unborn HEAD accepted")
	}
	head := r.commit("base", map[string]string{"code with spaces.txt": "base\n"})
	check := func() {
		t.Helper()
		got, err := (git.Program{}).FinalHead(ctx, r.dir)
		if err != nil || got != head {
			t.Fatalf("FinalHead = %q, %v; want %s", got, err, head)
		}
	}
	check()
	if err := os.WriteFile(filepath.Join(r.dir, "notes.md"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	check()
	r.run("checkout", "--detach", head)
	check()
	for _, change := range []string{"modified", "staged", "deleted", "renamed", "added"} {
		t.Run(change, func(t *testing.T) {
			name := "code with spaces.txt"
			switch change {
			case "modified", "staged":
				if err := os.WriteFile(filepath.Join(r.dir, name), []byte("changed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if change == "staged" {
					r.run("add", name)
				}
			case "deleted":
				r.run("rm", name)
			case "renamed":
				r.run("mv", name, "renamed.txt")
			case "added":
				name = "notes.md"
				r.run("add", name)
			}
			got, err := (git.Program{}).FinalHead(ctx, r.dir)
			if got != "" || err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("dirty FinalHead = %q, %v, want path %s", got, err, name)
			}
			r.run("restore", "--staged", "--worktree", ".")
		})
	}
	check()
	t.Run("submodule tracked changes but not untracked notes", func(t *testing.T) {
		parent := repo{t, t.TempDir(), global}
		parent.run("init", "-q")
		parent.run("-c", "protocol.file.allow=always", "submodule", "add", r.dir, "module")
		parentHead := parent.commit("module", map[string]string{"parent.txt": "parent\n"})
		module := repo{t, filepath.Join(parent.dir, "module"), global}
		// Per-module ignore settings must not conceal tracked work.
		parent.run("config", "submodule.module.ignore", "all")
		if err := os.WriteFile(filepath.Join(module.dir, "untracked.txt"), []byte("notes"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := (git.Program{}).FinalHead(ctx, parent.dir)
		if err != nil || got != parentHead {
			t.Fatalf("untracked submodule notes: %q, %v", got, err)
		}
		if err := os.WriteFile(filepath.Join(module.dir, "code with spaces.txt"), []byte("changed"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := (git.Program{}).FinalHead(ctx, parent.dir); err == nil || !strings.Contains(err.Error(), "module") {
			t.Fatalf("dirty submodule: %v", err)
		}
	})
	t.Run("plain directory", func(t *testing.T) {
		if _, err := (git.Program{}).FinalHead(ctx, t.TempDir()); err == nil {
			t.Fatal("non-repository accepted")
		}
	})
	t.Run("missing executable", func(t *testing.T) {
		if _, err := (git.Program{Path: filepath.Join(t.TempDir(), "missing-git")}).FinalHead(ctx, r.dir); err == nil {
			t.Fatal("missing git accepted")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ended, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := (git.Program{}).FinalHead(ended, r.dir); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled = %v", err)
		}
	})
	t.Run("bounded", func(t *testing.T) {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(hangingGitEnv, "1")
		// timing:allow intentional timeout of a child that never answers; no readiness assertion.
		_, err = (git.Program{Path: exe, Timeout: 200 * time.Millisecond}).FinalHead(ctx, r.dir)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout = %v", err)
		}
	})
}
