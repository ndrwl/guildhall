package setup_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/setup"
)

// shellStub writes an executable sh script named name into dir.
func shellStub(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// gapIDs runs the doctor of built-in name with vars and returns its gap ids.
func gapIDs(t *testing.T, sb sandbox, name string, vars map[string]string) []string {
	t.Helper()
	s, err := setup.LoadBuiltin(name, "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := setup.Doctor(context.Background(), s, setup.Facts{Home: sb.home, SetupDir: sb.setupDir, Vars: vars})
	if err != nil {
		t.Fatalf("doctor %s: %v", name, err)
	}
	ids := []string{}
	for _, g := range r.Gaps {
		ids = append(ids, g.ID)
	}
	return ids
}

// TestBuiltinNewCopy: copy-free is a gap while the copy directory holds a config or a store, and clone-paths-free
// while a clone path is a file or a directory that is not empty; with no params both pass.
func TestBuiltinNewCopy(t *testing.T) {
	requireUnix(t)
	sb := newSandbox(t)
	copyDir, empty, full, file := t.TempDir(), t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "file")
	for path, data := range map[string]string{filepath.Join(full, "README"): "x", file: "x"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	absent := filepath.Join(t.TempDir(), "absent")
	for _, c := range []struct {
		name string
		vars map[string]string
		want []string
	}{
		{"no params", nil, []string{}},
		{"free", map[string]string{"copy_dir": copyDir, "clone_paths": empty + ":" + absent}, []string{}},
		{"clone path not empty", map[string]string{"copy_dir": copyDir, "clone_paths": absent + ":" + full}, []string{"clone-paths-free"}},
		{"clone path a file", map[string]string{"clone_paths": file}, []string{"clone-paths-free"}},
	} {
		if got := gapIDs(t, sb, "new-copy", c.vars); !slices.Equal(got, c.want) {
			t.Errorf("%s: gaps %v, want %v", c.name, got, c.want)
		}
	}
	for _, f := range []string{"config.toml", "director.store"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := gapIDs(t, sb, "new-copy", map[string]string{"copy_dir": dir}); !slices.Equal(got, []string{"copy-free"}) {
			t.Errorf("copy directory with %s: gaps %v, want copy-free", f, got)
		}
	}
}

// TestBuiltinGuildhallFacts: the director file's guildhall-pin and tailscale-ssh steps and the herdr file's
// herdr-session step, against stub guildhall, tailscale and herdr programs.
func TestBuiltinGuildhallFacts(t *testing.T) {
	requireUnix(t)
	sb := newSandbox(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	guildhall := shellStub(t, bin, "guildhall", `echo "guildhall v1.2.3 linux/amd64 go1.26"`+"\n")

	director := map[string]string{"guildhall": guildhall}
	if got := gapIDs(t, sb, "director", nil); slices.Contains(got, "guildhall-pin") {
		t.Errorf("no pin: gaps %v", got)
	}
	director["guildhall_pin"] = "v1.2.3"
	if got := gapIDs(t, sb, "director", director); slices.Contains(got, "guildhall-pin") {
		t.Errorf("pinned release: gaps %v", got)
	}
	director["guildhall_pin"] = "v1.2"
	if got := gapIDs(t, sb, "director", director); !slices.Contains(got, "guildhall-pin") {
		t.Errorf("another release: gaps %v, want guildhall-pin", got)
	}

	// guildhall-on-path: plain guildhall passes; an absolute guildhall_path must be what PATH finds.
	if got := gapIDs(t, sb, "director", nil); slices.Contains(got, "guildhall-on-path") {
		t.Errorf("plain guildhall: gaps %v", got)
	}
	if got := gapIDs(t, sb, "director", map[string]string{"guildhall": guildhall}); slices.Contains(got, "guildhall-on-path") {
		t.Errorf("guildhall_path on PATH: gaps %v", got)
	}
	elsewhere := shellStub(t, t.TempDir(), "guildhall", "exit 0\n")
	if got := gapIDs(t, sb, "director", map[string]string{"guildhall": elsewhere}); !slices.Contains(got, "guildhall-on-path") {
		t.Errorf("guildhall_path not on PATH: gaps %v, want guildhall-on-path", got)
	}

	for _, c := range []struct {
		name, prefs, netmap string
		gap                 bool
	}{
		{"no Tailscale SSH", `{"RunSSH": false}`, `{}`, false},
		{"accept rules", `{"RunSSH": true}`, `{"SSHPolicy": {"rules": [{"action": {"accept": true}}]}}`, false},
		{"check mode", `{"RunSSH": true}`, `{"SSHPolicy": {"rules": [{"action": {"holdAndDelegate": "https://x"}}]}}`, true},
	} {
		shellStub(t, bin, "tailscale", `case "$2" in
prefs) echo '`+c.prefs+`' ;;
netmap) echo '`+c.netmap+`' ;;
*) exit 1 ;;
esac
`)
		if got := gapIDs(t, sb, "director", nil); slices.Contains(got, "tailscale-ssh") != c.gap {
			t.Errorf("%s: gaps %v", c.name, got)
		}
	}

	status := `{"client": {"version": "0.9.5", "session": "lab"}, "server": {"status": "%s", "running": %s, "capabilities": {"health_check": true}, "session": "lab"}}`
	for _, c := range []struct {
		name, state, running string
		vars                 map[string]string
		gap                  bool
	}{
		{"no session", "stopped", "false", nil, false},
		{"running", "running", "true", map[string]string{"herdr_session": "lab"}, false},
		{"stopped", "stopped", "false", map[string]string{"herdr_session": "lab"}, true},
	} {
		shellStub(t, bin, "herdr", `if [ "$1" = --version ]; then echo "herdr 0.9.5"; exit 0; fi
printf '`+status+`\n' '`+c.state+`' '`+c.running+`'
`)
		if got := gapIDs(t, sb, "herdr", c.vars); slices.Contains(got, "herdr-session") != c.gap {
			t.Errorf("%s: gaps %v", c.name, got)
		}
	}
}
