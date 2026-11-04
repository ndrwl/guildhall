//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/secrets"
)

// injectedSecret is the value the fake sops decrypts for API_KEY, which no output, error or model-driven
// environment may hold.
const injectedSecret = "sk-test-INJECTED-API-KEY"

// fakeSecrets puts fake sops and age-keygen first on PATH and points secrets.DirEnv at a new directory, which it
// returns. The fake sops decrypts to API_KEY and DB_PASS when the secrets file holds "good", and otherwise fails
// with injectedSecret on its standard output, so an error that held sops's output would show it.
func fakeSecrets(t *testing.T) string {
	t.Helper()
	bin, dir := t.TempDir(), t.TempDir()
	scripts := map[string]string{
		"sops": `[ "$1 $2 $3" = "--decrypt --output-type json" ] && [ -f "$SOPS_AGE_KEY_FILE" ] || exit 64
if [ "$(cat "$4")" = good ]; then echo '{"API_KEY": "` + injectedSecret + `", "DB_PASS": "db-password-value"}'; exit 0; fi
echo '{"API_KEY": "` + injectedSecret + `"}'; echo 'Error getting data key: 0 successful groups required, got 0' >&2; exit 128`,
		"age-keygen": `echo '# public key: age1machinepublickey'; echo 'AGE-SECRET-KEY-1MACHINESECRET'`,
	}
	for name, body := range scripts {
		writeFile(t, filepath.Join(bin, name), "#!/bin/sh\n"+body+"\n")
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(secrets.DirEnv, dir)
	return dir
}

// TestSecretsInit: secrets init creates an owner-only key and prints its public key, never the secret key.
func TestSecretsInit(t *testing.T) {
	dir := fakeSecrets(t)
	app, stdout, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"secrets", "init"}); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	out := stdout.String()
	if !strings.Contains(out, "public key: age1machinepublickey") || strings.Contains(out+stderr.String(), "AGE-SECRET-KEY") {
		t.Fatalf("output %q: want the public key and no secret key", out)
	}
	if fi, err := os.Stat(filepath.Join(dir, secrets.KeyFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key %v, %v; want mode 0600", fi, err)
	}
	if code := app.Run([]string{"secrets", "init", "now"}); code != 2 {
		t.Fatalf("secrets init with an argument: exit %d, want 2", code)
	}
}

// TestDirectorStartDecryptsSecrets: the start preflight injects nothing without a key or secrets file, names how
// many names a file holds without any value, and refuses a file that fails to decrypt, or a key inside the copy,
// naming no value.
func TestDirectorStartDecryptsSecrets(t *testing.T) {
	sdir := fakeSecrets(t)
	copyDir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	check := func() (int, string) {
		code, out, stderr := runDirectorCmdWith(t, nil, "start", "--check", "--copy", copyDir)
		if strings.Contains(out+stderr, injectedSecret) || strings.Contains(out+stderr, "db-password-value") {
			t.Fatalf("output holds a secret value:\n%s\n%s", out, stderr)
		}
		return code, out + stderr
	}
	if code, out := check(); code != 0 || !strings.Contains(out, "secrets: none injected") {
		t.Fatalf("no key or file: exit %d, want 0 and none injected:\n%s", code, out)
	}
	writeFile(t, filepath.Join(sdir, secrets.KeyFile), "AGE-SECRET-KEY-1X")
	writeFile(t, filepath.Join(sdir, secrets.File), "good")
	if code, out := check(); code != 0 || !strings.Contains(out, "secrets: 2 injected (API_KEY, DB_PASS)") {
		t.Fatalf("a good file: exit %d, want 0 and two names:\n%s", code, out)
	}
	writeFile(t, filepath.Join(sdir, secrets.File), "bad")
	if code, out := check(); code != 1 || !strings.Contains(out, "secrets: refused: secrets: the secrets file does not decrypt") {
		t.Fatalf("a bad file: exit %d, want 1 and a refusal:\n%s", code, out)
	}
	inside := filepath.Join(copyDir, "keys")
	t.Setenv(secrets.DirEnv, inside)
	if code, out := check(); code != 1 || !strings.Contains(out, "outside every copy") {
		t.Fatalf("a key inside the copy: exit %d, want 1 and a refusal:\n%s", code, out)
	}
}

// TestRuntimeGetsNoInjectedSecret: the supervisor keeps injected values in its own environment and lists their
// names, and the runtime's environment, built from it, drops every one.
func TestRuntimeGetsNoInjectedSecret(t *testing.T) {
	sdir := fakeSecrets(t)
	writeFile(t, filepath.Join(sdir, secrets.KeyFile), "AGE-SECRET-KEY-1X")
	writeFile(t, filepath.Join(sdir, secrets.File), "good")
	app, _, _ := newTestApp(nil, nil)
	v, err := app.sessionSecrets("director", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{secrets.NamesEnv: "OLD_NAME"}
	if err := injectSecrets(v, func(k string) string { return env[k] }, func(k, v string) error { env[k] = v; return nil }); err != nil {
		t.Fatal(err)
	}
	if env["API_KEY"] != injectedSecret || env[secrets.NamesEnv] != "API_KEY,DB_PASS,OLD_NAME" {
		t.Fatalf("supervisor environment lacks the values or their names: %s=%q", secrets.NamesEnv, env[secrets.NamesEnv])
	}
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	environ = append(environ, "OLD_NAME=old", "PATH=/bin")
	got := runtimeEnv(environ, "", []string{"GUILDHALL_SESSION=1"})
	for _, kv := range got {
		if name, _, _ := strings.Cut(kv, "="); slices.Contains([]string{"API_KEY", "DB_PASS", "OLD_NAME"}, name) {
			t.Fatalf("the runtime's environment holds %s: %q", name, got)
		}
	}
	if !slices.Contains(got, "PATH=/bin") || !slices.Contains(got, "GUILDHALL_SESSION=1") {
		t.Fatalf("the runtime's environment lost an ordinary entry: %q", got)
	}
}

// TestArtisanInheritsOnlyInheritEnv: an owned Artisan inherits only the names its profile's inherit_env lists that
// the secrets file holds, with secrets.RedactEnv naming them; a profile listing none decrypts nothing.
func TestArtisanInheritsOnlyInheritEnv(t *testing.T) {
	sdir := fakeSecrets(t)
	writeFile(t, filepath.Join(sdir, secrets.KeyFile), "AGE-SECRET-KEY-1X")
	writeFile(t, filepath.Join(sdir, secrets.File), "good")
	copyDir := directorCopy(t, "[profile.builder]\nadapter = \"claude\"\ninherit_env = [\"API_KEY\", \"NOT_HELD\"]\n", "/exit")
	app, _, _ := newTestApp(nil, nil)
	env, err := app.inheritedSecrets(copyDir, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"API_KEY=" + injectedSecret, secrets.RedactEnv + "=API_KEY"}; !slices.Equal(env, want) {
		t.Fatalf("builder inherits %d entries, want API_KEY and its redaction list", len(env))
	}
	// A profile listing nothing never runs sops, so even a file that fails to decrypt is not read.
	writeFile(t, filepath.Join(sdir, secrets.File), "bad")
	if env, err := app.inheritedSecrets(copyDir, "reviewer"); err != nil || len(env) != 0 {
		t.Fatalf("reviewer inherits %d entries, %v; want none", len(env), err)
	}
	if _, err := app.inheritedSecrets(copyDir, "builder"); err == nil || strings.Contains(err.Error(), injectedSecret) {
		t.Fatalf("a bad file for builder = %v, want a refusal naming no value", err)
	}
}
