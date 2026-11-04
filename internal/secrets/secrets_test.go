package secrets_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/secrets"
)

func TestStripDropsTheInjectedNamesOnly(t *testing.T) {
	env := []string{"PATH=/bin", "API_KEY=v1", "OTHER=keep", secrets.NamesEnv + "=API_KEY,DB_PASS", "DB_PASS=v2", "HOME=/h"}
	got := secrets.Strip(env)
	want := []string{"PATH=/bin", "OTHER=keep", secrets.NamesEnv + "=API_KEY,DB_PASS", "HOME=/h"}
	if !slices.Equal(got, want) {
		t.Fatalf("Strip = %q, want %q", got, want)
	}
	// Without the names variable nothing is dropped.
	plain := []string{"PATH=/bin", "API_KEY=v1"}
	if got := secrets.Strip(plain); !slices.Equal(got, plain) {
		t.Fatalf("Strip = %q, want %q unchanged", got, plain)
	}
}

func TestNames(t *testing.T) {
	if got := secrets.SplitNames(" A, B,,A,1X,b_2 "); !slices.Equal(got, []string{"A", "B", "b_2"}) {
		t.Fatalf("SplitNames = %q", got)
	}
	if got := secrets.JoinNames([]string{"B", "A"}, []string{"A", "C"}); got != "A,B,C" {
		t.Fatalf("JoinNames = %q", got)
	}
}

func TestRedactor(t *testing.T) {
	r := secrets.NewRedactor([]string{"hunter2-secret", "abc", "hunter2"})
	got := r.String("pw hunter2-secret and hunter2 but abc stays")
	if want := "pw [redacted] and [redacted] but abc stays"; got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}
	var buf bytes.Buffer
	w := r.Writer(&buf)
	if n, err := fmt.Fprint(w, "x hunter2 y\n"); err != nil || n != len("x hunter2 y\n") {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if buf.String() != "x [redacted] y\n" {
		t.Fatalf("wrote %q", buf.String())
	}
	var none *secrets.Redactor
	if none.String("hunter2") != "hunter2" || none.Writer(&buf) != &buf {
		t.Fatal("a nil Redactor must redact nothing")
	}
	if secrets.NewRedactor([]string{"ab", ""}) != nil {
		t.Fatal("values shorter than MinRedacted make no Redactor")
	}
}

func TestFromEnv(t *testing.T) {
	env := map[string]string{secrets.RedactEnv: "API_KEY,MISSING", "API_KEY": "s3cr3t-value"}
	r := secrets.FromEnv(func(k string) string { return env[k] })
	if got := r.String("key=s3cr3t-value"); got != "key=[redacted]" {
		t.Fatalf("String = %q", got)
	}
	if secrets.FromEnv(func(string) string { return "" }) != nil {
		t.Fatal("no names make no Redactor")
	}
}

func TestPathsOutside(t *testing.T) {
	home := t.TempDir()
	t.Setenv(secrets.DirEnv, "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	p, err := secrets.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != filepath.Join(home, ".config", "guildhall", "age.key") || p.File != filepath.Join(home, ".config", "guildhall", "secrets.sops.yaml") {
		t.Fatalf("paths %+v", p)
	}
	t.Setenv(secrets.DirEnv, filepath.Join(home, "elsewhere"))
	if p, err := secrets.DefaultPaths(); err != nil || p.Key != filepath.Join(home, "elsewhere", "age.key") {
		t.Fatalf("paths with %s = %+v, %v", secrets.DirEnv, p, err)
	}
	p, _ = secrets.DefaultPaths()
	if err := p.Outside(filepath.Join(home, "copy")); err != nil {
		t.Fatalf("Outside a sibling copy: %v", err)
	}
	if err := p.Outside(home); err == nil || !strings.Contains(err.Error(), "outside every copy") {
		t.Fatalf("Outside the home itself = %v, want a refusal", err)
	}
}

// TestPathsOutsideThroughALink: a copy reached through a symbolic link still holds a key named by its other path
// in a directory not yet created, as macOS temporary directories sit behind /var -> /private/var.
func TestPathsOutsideThroughALink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
	p := secrets.Paths{Key: filepath.Join(real, "keys", "deeper", "age.key"), File: filepath.Join(real, "keys", "deeper", "secrets.sops.yaml")}
	if err := p.Outside(link); err == nil || !strings.Contains(err.Error(), "outside every copy") {
		t.Fatalf("a missing key under the linked copy's real path = %v, want a refusal", err)
	}
	p = secrets.Paths{Key: filepath.Join(link, "keys", "age.key"), File: filepath.Join(link, "keys", "secrets.sops.yaml")}
	if err := p.Outside(real); err == nil {
		t.Fatal("a missing key under the link, against the real copy, was not refused")
	}
	if err := p.Outside(filepath.Join(real, "other")); err != nil {
		t.Fatalf("a sibling: %v", err)
	}
}

func TestValuesNeverPrintAValue(t *testing.T) {
	var v secrets.Values
	if v.Len() != 0 || len(v.Env()) != 0 {
		t.Fatal("the zero Values holds nothing")
	}
}
