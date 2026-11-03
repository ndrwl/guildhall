package config

import (
	"path/filepath"
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

// TestReleaseSource: release_source may be local or managed, a local value replacing an overridable managed one; it
// must be a source git can take, a local path absolute and a URL with no credential, and no task may override it.
func TestReleaseSource(t *testing.T) {
	fork := "https://github.com/example/guildhall"
	upstream := "https://github.com/ndrwl/guildhall"
	abs := filepath.Join(t.TempDir(), "guildhall")
	for _, c := range []struct {
		name, managed, local, want string
		set                        bool
	}{
		{"unset", "", "", "", false},
		{"local", "", "release_source = \"" + fork + "\"\n", fork, true},
		{"an absolute local path", "", "release_source = '" + abs + "'\n", abs, true},
		{"an scp-style remote", "", "release_source = \"git@github.com:example/guildhall.git\"\n", "git@github.com:example/guildhall.git", true},
		{"managed", "[managed.release_source]\nvalue = \"" + upstream + "\"\n", "", upstream, true},
		{"local over overridable managed", "[managed.release_source]\nvalue = \"" + upstream + "\"\noverridable = true\n", "release_source = \"" + fork + "\"\n", fork, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := overrideConfig(t, c.managed, c.local).ReleaseSource()
			if got != c.want || ok != c.set {
				t.Errorf("ReleaseSource = %q, %v; want %q, %v", got, ok, c.want, c.set)
			}
		})
	}
	for _, c := range []struct {
		name, managed, local, want string
	}{
		{"empty", "", "release_source = \"\"\n", "release_source"},
		{"an option", "", "release_source = \"--upload-pack=x\"\n", "release_source"},
		{"a relative path", "", "release_source = \"../guildhall\"\n", "must be absolute"},
		{"a credential", "", "release_source = \"https://user:secret@example.com/guildhall\"\n", "credential"},
		{"a bad managed value", "[managed.release_source]\nvalue = \"-x\"\n", "", "managed release_source"},
		{"local over a pinned managed value", "[managed.release_source]\nvalue = \"" + upstream + "\"\n", "release_source = \"" + fork + "\"\n", "not overridable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := strings.Replace(director(t), "[local]", c.managed+"[local]\n"+c.local, 1)
			if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Parse = %v, want an error naming %q", err, c.want)
			}
		})
	}
	cfg := overrideConfig(t, "", "release_source = \""+fork+"\"\n")
	if err := cfg.CheckTaskOverride(ReleaseSourceKey, upstream); err == nil || !strings.Contains(err.Error(), "machine-wide") {
		t.Fatalf("a task override of release_source = %v, want the machine-wide refusal", err)
	}
}
