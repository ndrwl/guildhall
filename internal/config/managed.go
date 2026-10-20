package config

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type managedFile struct {
	SchemaVersion int                       `toml:"schema_version"`
	Managed       map[string]ManagedSetting `toml:"managed"`
}

func EncodeManaged(settings map[string]ManagedSetting) ([]byte, error) {
	var body bytes.Buffer
	encoder := toml.NewEncoder(&body)
	encoder.Indent = ""
	if err := encoder.Encode(managedFile{SchemaVersion: SchemaVersion, Managed: settings}); err != nil {
		return nil, err
	}
	return fmt.Appendf(nil, "# sha256:%x\n%s", sha256.Sum256(body.Bytes()), body.Bytes()), nil
}

func ParseCombined(local, managed []byte) (*Config, error) {
	cfg, err := Parse(local)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	md, err := toml.Decode(string(local), &raw)
	if err != nil {
		return nil, err
	}
	if md.IsDefined("managed") {
		return nil, errors.New("inline managed settings conflict with managed.toml; move managed settings out of config.toml during enrolment")
	}
	header, body, ok := bytes.Cut(managed, []byte("\n"))
	if !ok || string(header) != fmt.Sprintf("# sha256:%x", sha256.Sum256(body)) {
		return nil, errors.New("managed.toml: incomplete or corrupt transfer (checksum mismatch)")
	}
	var m managedFile
	md, err = toml.Decode(string(body), &m)
	if err != nil {
		return nil, fmt.Errorf("managed.toml: %w", err)
	}
	if m.SchemaVersion != SchemaVersion || len(md.Undecoded()) != 0 {
		return nil, errors.New("managed.toml: unsupported schema or unknown keys")
	}
	if typ := md.Type("managed"); typ != "" && typ != "Hash" {
		return nil, errors.New("managed.toml: managed must contain tables")
	}
	cfg.Managed = m.Managed
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, errors.New("invalid combined config: " + strings.Join(problems, "; "))
	}
	return cfg, nil
}

// Load reads the config at filename and the managed.toml in the same directory.
// An unreadable config file is returned as its read error.
// Only a managed.toml that does not exist falls back to Parse, which accepts legacy inline managed settings;
// any other read error, or a managed.toml that ParseCombined refuses, is returned and never falls back.
func Load(filename string) (*Config, error) {
	local, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	managed, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "managed.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return Parse(local)
	}
	if err != nil {
		return nil, err
	}
	return ParseCombined(local, managed)
}
