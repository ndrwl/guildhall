//go:build unix

package secrets_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/secrets"
)

// testSecret is the value the fake sops decrypts, which no error may hold.
const testSecret = "sk-test-INJECTED-SECRET-VALUE"

// fakeTools puts fake sops and age-keygen first on PATH. The fake sops prints $FAKE_SOPS_OUT's file to stdout, or
// fails with testSecret on stdout when it is unset, so an error that held sops's standard output would show it. The
// fake age-keygen prints a key, or with -y the public key of the file it is given.
func fakeTools(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	scripts := map[string]string{
		"sops": `[ "$1 $2 $3" = "--decrypt --output-type json" ] || exit 64
[ -n "$SOPS_AGE_KEY_FILE" ] || exit 65
[ -n "$FAKE_SOPS_OUT" ] && { cat "$FAKE_SOPS_OUT"; exit 0; }
echo '{"LEAK": "` + testSecret + `"}'; echo 'Failed to get the data key required to decrypt the SOPS file.' >&2; exit 128`,
		"age-keygen": `if [ "$1" = -y ]; then sed -n 's/^# public key: //p' "$2"; exit 0; fi
echo '# created: 2026-11-04T00:00:00Z'
echo '# public key: age1fakepublickey0000000000000000000000000000000000000000000'
echo 'AGE-SECRET-KEY-1FAKESECRETKEY'`,
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func paths(t *testing.T) secrets.Paths {
	d := secrets.Dir(t.TempDir())
	return secrets.Paths{Key: filepath.Join(d, secrets.KeyFile), File: filepath.Join(d, secrets.File)}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWithoutKeyOrFileInjectsNothing(t *testing.T) {
	fakeTools(t)
	p := paths(t)
	if v, err := secrets.Load(context.Background(), p, nil); err != nil || v.Len() != 0 {
		t.Fatalf("Load with neither = %v, %v; want none", v, err)
	}
	write(t, p.File, "ciphertext")
	if v, err := secrets.Load(context.Background(), p, nil); err != nil || v.Len() != 0 {
		t.Fatalf("Load with no key = %v, %v; want none", v, err)
	}
}

func TestLoadDecrypts(t *testing.T) {
	fakeTools(t)
	p := paths(t)
	write(t, p.Key, "AGE-SECRET-KEY-1X")
	write(t, p.File, "ciphertext")
	out := filepath.Join(t.TempDir(), "plain.json")
	write(t, out, `{"API_KEY": "`+testSecret+`", "OTHER": "two", "sops": {"version": "3"}}`)
	t.Setenv("FAKE_SOPS_OUT", out)
	v, err := secrets.Load(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(v.Names(), ","); got != "API_KEY,OTHER" {
		t.Fatalf("names %q", got)
	}
	if s, _ := v.Get("API_KEY"); s != testSecret {
		t.Fatal("API_KEY did not decrypt to its value")
	}
	if strings.Contains(fmt.Sprint(v), testSecret) || strings.Contains(fmt.Sprintf("%#v %+v", v, v), testSecret) {
		t.Fatal("printing Values shows a value")
	}
	if env := v.Pick([]string{"OTHER", "NONE"}).Env(); len(env) != 1 || env[0] != "OTHER=two" {
		t.Fatalf("Pick = %q", env)
	}
}

func TestLoadRefusesAFileThatFailsToDecrypt(t *testing.T) {
	fakeTools(t)
	p := paths(t)
	write(t, p.Key, "AGE-SECRET-KEY-1X")
	write(t, p.File, "ciphertext")
	_, err := secrets.Load(context.Background(), p, nil)
	if !errors.Is(err, secrets.ErrDecrypt) || !strings.Contains(err.Error(), "Failed to get the data key") {
		t.Fatalf("Load = %v, want ErrDecrypt with sops's error output", err)
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Fatalf("the error holds sops's standard output: %v", err)
	}
	// Plaintext that is not a flat map of names to strings is refused naming no value.
	for _, plain := range []string{`not json ` + testSecret, `{"1BAD": "` + testSecret + `"}`, `{"NESTED": {"v": "` + testSecret + `"}}`} {
		out := filepath.Join(t.TempDir(), "plain.json")
		write(t, out, plain)
		t.Setenv("FAKE_SOPS_OUT", out)
		_, err := secrets.Load(context.Background(), p, nil)
		if !errors.Is(err, secrets.ErrDecrypt) || strings.Contains(err.Error(), testSecret) {
			t.Fatalf("Load of %q = %v, want ErrDecrypt naming no value", plain, err)
		}
	}
}

func TestInitCreatesAnOwnerOnlyKeyAndPrintsOnlyThePublicKey(t *testing.T) {
	fakeTools(t)
	p := paths(t)
	pub, created, err := secrets.Init(context.Background(), p, nil)
	if err != nil || !created || pub != "age1fakepublickey0000000000000000000000000000000000000000000" {
		t.Fatalf("Init = %q, %v, %v", pub, created, err)
	}
	fi, err := os.Stat(p.Key)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key %v, %v; want mode 0600", fi, err)
	}
	if di, err := os.Stat(filepath.Dir(p.Key)); err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("key directory %v, %v; want mode 0700", di, err)
	}
	if data, _ := os.ReadFile(p.Key); !strings.Contains(string(data), "AGE-SECRET-KEY-") {
		t.Fatalf("key file %q holds no key", data)
	}
	// A second init keeps the key and reads its public key.
	again, created, err := secrets.Init(context.Background(), p, nil)
	if err != nil || created || again != pub {
		t.Fatalf("second Init = %q, %v, %v; want the same key kept", again, created, err)
	}
}
