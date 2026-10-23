package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WorkspaceDir is the stable, per-copy folder Claude asks the operator to trust once.
func WorkspaceDir(copyDir string) string { return filepath.Join(copyDir, "director", "claude") }

// PrepareSessionWorkspace installs the numbered record's .claude tree without changing the record.
// The supervisor owns serialization: it calls this only while no preceding runtime is running.
// A failed publication starts no runtime; a later start can publish a fresh tree again.
func (Adapter) PrepareSessionWorkspace(copyDir, recordDir string) (string, error) {
	dir := WorkspaceDir(copyDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(dir, ".settings-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := os.CopyFS(stage, os.DirFS(filepath.Join(recordDir, ".claude"))); err != nil {
		return "", fmt.Errorf("claude workspace: copy runtime files: %w", err)
	}
	target := filepath.Join(dir, ".claude")
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	if err := os.Rename(stage, target); err != nil {
		return "", err
	}
	return dir, nil
}

// SessionTrust reports an exact saved folder approval where readable, never writes it, and never
// claims to reproduce Claude's version-dependent ancestor, git-root and managed trust resolution.
func (Adapter) SessionTrust(copyDir string, getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	dir := WorkspaceDir(copyDir)
	unknown := fmt.Sprintf("%s: trust not confirmed; first start may require one folder-trust approval in Claude; effective ancestor/project trust is resolved by Claude (docs/director.md)", dir)
	if getenv("CLAUDE_CODE_IGNORE_PERSISTED_TRUST") != "" {
		return unknown + "; persisted trust disabled by environment"
	}
	configDir := getenv("CLAUDE_CONFIG_DIR")
	var configPath string
	if configDir != "" {
		configPath = filepath.Join(configDir, ".claude.json")
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return unknown
		}
		configPath = filepath.Join(home, ".claude.json")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return unknown
	}
	var cfg struct {
		Projects map[string]struct {
			Accepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return unknown
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return unknown
	}
	if cfg.Projects[filepath.ToSlash(canonical)].Accepted {
		return fmt.Sprintf("%s: exact folder-trust approval recorded; Claude resolves effective trust at startup", dir)
	}
	return unknown
}
