package config

import (
	"strings"
	"testing"
)

// TestManagedRelease: release and release_sha256 are managed keys that go together, never local and never
// overridable, with a release version and a sha256 each; ManagedRelease reads them, the sha256 in lowercase.
func TestManagedRelease(t *testing.T) {
	sum := strings.Repeat("Ab", 32)
	cfg := overrideConfig(t, "[managed.release]\nvalue = \"v1.4.0\"\n[managed.release_sha256]\nvalue = \""+sum+"\"\n", "")
	if v, s, ok := cfg.ManagedRelease(); !ok || v != "v1.4.0" || s != strings.ToLower(sum) {
		t.Fatalf("ManagedRelease = %q, %q, %v", v, s, ok)
	}
	none := overrideConfig(t, "", "")
	if _, _, ok := none.ManagedRelease(); ok {
		t.Fatal("a config with no release keys pins a release")
	}
	for _, c := range []struct {
		name, managed, local, want string
	}{
		{"release alone", "[managed.release]\nvalue = \"v1.4.0\"\n", "", "go together"},
		{"sha256 alone", "[managed.release_sha256]\nvalue = \"" + sum + "\"\n", "", "go together"},
		{"dev", "[managed.release]\nvalue = \"dev\"\n[managed.release_sha256]\nvalue = \"" + sum + "\"\n", "", `version "dev"`},
		{"bad sha256", "[managed.release]\nvalue = \"v1.4.0\"\n[managed.release_sha256]\nvalue = \"abc\"\n", "", "not 64 hex digits"},
		{"overridable", "[managed.release]\nvalue = \"v1.4.0\"\noverridable = true\n[managed.release_sha256]\nvalue = \"" + sum + "\"\n", "", "cannot be overridable"},
		{"local", "", "release = \"v1.4.0\"\n", "not a local setting"},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := strings.Replace(director(t), "[local]", c.managed+"[local]\n"+c.local, 1)
			if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Parse = %v, want an error naming %q", err, c.want)
			}
		})
	}
	if err := cfg.CheckTaskOverride(ReleaseKey, "v1.5.0"); err == nil || !strings.Contains(err.Error(), "machine-wide") {
		t.Fatalf("a task override of release = %v, want the machine-wide refusal", err)
	}
}
