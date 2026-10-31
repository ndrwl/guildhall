package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/ndrwl/guildhall/internal/digest"
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
	return fmt.Appendf(nil, "# %s\n%s", digest.Of(body.Bytes()), body.Bytes()), nil
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
	if cfg.Managed, err = DecodeManaged(managed); err != nil {
		return nil, err
	}
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, errors.New("invalid combined config: " + strings.Join(problems, "; "))
	}
	return cfg, nil
}

// DecodeManaged checks managed.toml's checksum line and schema and returns its settings, without validating them
// against a config: ParseCombined does that.
func DecodeManaged(managed []byte) (map[string]ManagedSetting, error) {
	header, body, ok := bytes.Cut(managed, []byte("\n"))
	if !ok || string(header) != "# "+string(digest.Of(body)) {
		return nil, errors.New("managed.toml: incomplete or corrupt transfer (checksum mismatch)")
	}
	var m managedFile
	md, err := toml.Decode(string(body), &m)
	if err != nil {
		return nil, fmt.Errorf("managed.toml: %w", err)
	}
	if m.SchemaVersion != SchemaVersion || len(md.Undecoded()) != 0 {
		return nil, errors.New("managed.toml: unsupported schema or unknown keys")
	}
	if typ := md.Type("managed"); typ != "" && typ != "Hash" {
		return nil, errors.New("managed.toml: managed must contain tables")
	}
	return m.Managed, nil
}

// InlineManaged reports whether the config.toml text local defines a [managed] table, even an empty one: a Director
// copy refuses it in every mode (docs/director-modes.md#mode). Text that is not TOML is an error.
func InlineManaged(local []byte) (bool, error) {
	var raw map[string]any
	md, err := toml.Decode(string(local), &raw)
	if err != nil {
		return false, fmt.Errorf("config: %w", err)
	}
	return md.IsDefined("managed"), nil
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
