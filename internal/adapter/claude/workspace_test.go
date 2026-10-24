package claude_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter/claude"
)

func TestWorkspaceRefreshPreservesRecordsAndPermissions(t *testing.T) {
	copyDir := t.TempDir()
	a := claude.Adapter{}
	for _, name := range []string{"old", "new"} {
		record := filepath.Join(copyDir, "director", name)
		skill := filepath.Join(record, ".claude", "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(skill, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		settings := filepath.Join(record, claude.SettingsFile)
		if err := os.WriteFile(settings, claude.Settings(copyDir), 0o600); err != nil {
			t.Fatal(err)
		}
		dir, err := a.PrepareSessionWorkspace(copyDir, record)
		if err != nil || dir != claude.WorkspaceDir(copyDir) {
			t.Fatalf("prepare: %s %v", dir, err)
		}
		for _, path := range []string{settings, filepath.Join(dir, claude.SettingsFile)} {
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(claude.Settings(copyDir)) {
				t.Fatalf("settings changed: %s %v", path, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(claude.WorkspaceDir(copyDir), ".claude", "skills", "old")); !os.IsNotExist(err) {
		t.Fatalf("stale skill retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(copyDir, "director", "old", ".claude", "skills", "old", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}
