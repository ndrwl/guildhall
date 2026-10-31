package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadVerified(t *testing.T) {
	root := t.TempDir()
	body := []byte("# Spec\n")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	for _, rel := range []string{"a/x.md", "..foo/x.md"} {
		file := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct {
		name, path, sha string
		want            []error
	}{
		{"empty", "", digest, []error{ErrPathEmpty, ErrUnsafePath}},
		{"absolute", "/abs", digest, []error{ErrPathAbsolute, ErrUnsafePath}},
		{"parent", "..", digest, []error{ErrPathEscapes, ErrUnsafePath}},
		{"parent child", "../x", digest, []error{ErrPathEscapes, ErrUnsafePath}},
		{"climbs out", "a/../../x", digest, []error{ErrPathEscapes, ErrUnsafePath}},
		{"backslash", `a\b`, digest, []error{ErrPathBackslash, ErrUnsafePath}},
		{"missing", "a/none.md", digest, []error{ErrMissing, fs.ErrNotExist}},
		{"mismatch", "a/x.md", strings.Repeat("0", 64), []error{ErrDigestMismatch}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadVerified(root, c.path, c.sha)
			if got != nil {
				t.Errorf("bytes = %q, want none", got)
			}
			for _, want := range c.want {
				if !errors.Is(err, want) {
					t.Errorf("err = %v, want errors.Is %v", err, want)
				}
			}
			if c.want[0] != ErrMissing && errors.Is(err, fs.ErrNotExist) || c.want[0] != ErrDigestMismatch && errors.Is(err, ErrDigestMismatch) {
				t.Errorf("err = %v matches another case's sentinel", err)
			}
		})
	}

	for _, c := range []struct{ name, path, sha string }{
		{"dot-dot prefixed name", "..foo/x.md", digest},
		{"uppercase digest", "a/x.md", strings.ToUpper(digest)},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadVerified(root, c.path, c.sha)
			if err != nil || !bytes.Equal(got, body) {
				t.Errorf("ReadVerified = %q, %v, want the file's bytes", got, err)
			}
		})
	}
}
