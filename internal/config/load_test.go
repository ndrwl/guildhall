package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const loadLocal = "schema_version = 1\nmode = \"steward\"\n[private_config]\nlocation = '/private'\n"

// loadDir writes config.toml and, unless managed is nil, managed.toml into a new directory and returns the config's path.
func loadDir(t *testing.T, local string, managed []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	if managed != nil {
		if err := os.WriteFile(filepath.Join(dir, "managed.toml"), managed, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "config.toml")
}

func encodeManaged(t *testing.T, settings map[string]ManagedSetting) []byte {
	t.Helper()
	data, err := EncodeManaged(settings)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLoadMissingConfig(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "config.toml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing config: %v", err)
	}
}

func TestLoadWithoutManagedFileAcceptsLegacyInlineSettings(t *testing.T) {
	cfg, err := Load(loadDir(t, loadLocal+"[managed.style]\nvalue = 'inline'\n", nil))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := cfg.Setting("style"); v != "inline" {
		t.Fatalf("style = %q", v)
	}
}

func TestLoadWithoutManagedFileValidatesConfig(t *testing.T) {
	if _, err := Load(loadDir(t, loadLocal+"unknown = 1\n", nil)); err == nil {
		t.Fatal("accepted unknown key")
	}
}

func TestLoadCombinesManagedFile(t *testing.T) {
	managed := encodeManaged(t, map[string]ManagedSetting{"style": {Value: "central"}, "tone": {Value: "plain", Overridable: true}})
	cfg, err := Load(loadDir(t, loadLocal+"[local]\ntone = 'local'\n", managed))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := cfg.Setting("style"); v != "central" {
		t.Fatalf("style = %q", v)
	}
	if v, _ := cfg.Setting("tone"); v != "local" {
		t.Fatalf("tone = %q", v)
	}
}

// A managed.toml that exists but cannot be used is refused, never replaced by the legacy inline layout,
// even when config.toml alone would load.
func TestLoadRefusesUnusableManagedFile(t *testing.T) {
	valid := encodeManaged(t, map[string]ManagedSetting{"style": {Value: "central"}})
	for _, c := range []struct {
		name, local string
		managed     []byte
		want        string
	}{
		{"corrupt", loadLocal, valid[:len(valid)-1], "checksum"},
		{"empty", loadLocal, []byte{}, "checksum"},
		{"unknown key", loadLocal, encodeRaw("schema_version = 1\nextra = 1\n"), "unknown keys"},
		{"newer schema", loadLocal, encodeRaw("schema_version = 2\n"), "unsupported schema"},
		{"inline conflict", loadLocal + "[managed.style]\nvalue = 'inline'\n", valid, "move managed settings"},
		{"forbidden override", loadLocal + "[local]\nstyle = 'local'\n", valid, "invalid combined config"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Load(loadDir(t, c.local, c.managed)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

// encodeRaw frames body with a valid checksum line, so only its content is wrong.
func encodeRaw(body string) []byte {
	return fmt.Appendf(nil, "# sha256:%x\n%s", sha256.Sum256([]byte(body)), body)
}

func TestLoadRefusesUnreadableManagedFile(t *testing.T) {
	path := loadDir(t, loadLocal, nil)
	if err := os.Mkdir(filepath.Join(filepath.Dir(path), "managed.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory managed.toml: %v", err)
	}
}
