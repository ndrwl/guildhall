package claude

import (
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
