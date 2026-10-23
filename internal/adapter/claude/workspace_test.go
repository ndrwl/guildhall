package claude_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestSessionTrustReadOnlyAndConservative(t *testing.T) {
	copyDir := t.TempDir()
	dir := claude.WorkspaceDir(copyDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	getenv := func(key string) string {
		if key == "CLAUDE_CONFIG_DIR" {
			return configDir
		}
		return ""
	}
	cfg := filepath.Join(configDir, ".claude.json")
	for _, tc := range []struct{ name, data, want string }{
		{"missing", "", "not confirmed"},
		{"invalid", "{", "not confirmed"},
		{"false", `{"projects":{}}`, "not confirmed"},
		{"ancestor", trustJSON(t, filepath.Dir(canonical)), "not confirmed"},
		{"exact", trustJSON(t, canonical), "approval recorded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.data != "" {
				if err := os.WriteFile(cfg, []byte(tc.data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := (claude.Adapter{}).SessionTrust(copyDir, getenv)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("notice: %s", got)
			}
			data, err := os.ReadFile(cfg)
			if tc.data == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("read created config: %v", err)
				}
			} else if err != nil || string(data) != tc.data {
				t.Fatalf("trust state modified: %v", err)
			}
		})
	}
}

func trustJSON(t *testing.T, dir string) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"projects": map[string]any{filepath.ToSlash(dir): map[string]bool{"hasTrustDialogAccepted": true}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
