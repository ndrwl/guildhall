package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrUnsafePath is wrapped by each of ReadVerified's path refusals, so a caller can test the whole class at once.
var ErrUnsafePath = errors.New("unsafe document path")

// ReadVerified's refusals, each a distinct error; the four path refusals wrap ErrUnsafePath and ErrMissing wraps
// fs.ErrNotExist. Their text may name paths, so a caller must not put it on a page.
var (
	ErrPathEmpty      = fmt.Errorf("document path is empty: %w", ErrUnsafePath)
	ErrPathBackslash  = fmt.Errorf("document path has a backslash: %w", ErrUnsafePath)
	ErrPathAbsolute   = fmt.Errorf("document path is absolute: %w", ErrUnsafePath)
	ErrPathEscapes    = fmt.Errorf("document path leaves the document directory: %w", ErrUnsafePath)
	ErrMissing        = fmt.Errorf("document file is missing: %w", fs.ErrNotExist)
	ErrDigestMismatch = errors.New("document file does not match its recorded sha256")
)

// ReadVerified reads the slash-separated path under root and returns its bytes when the path stays inside root and
// the file's hex sha256 equals sha256, compared without regard to case.
func ReadVerified(root string, path, sha256 string) ([]byte, error) {
	clean, err := cleanDocPath(path)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(clean)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrMissing, clean)
	}
	if err != nil {
		return nil, fmt.Errorf("read document %s: %w", clean, err)
	}
	if digestHex(body) != strings.ToLower(sha256) {
		return nil, fmt.Errorf("%w: %s", ErrDigestMismatch, clean)
	}
	return body, nil
}

// cleanDocPath is p cleaned, or the refusal of a p that is empty, has a backslash, is absolute or leaves its root.
func cleanDocPath(p string) (string, error) {
	if p == "" {
		return "", ErrPathEmpty
	}
	if strings.Contains(p, `\`) {
		return "", ErrPathBackslash
	}
	clean := path.Clean(p)
	switch {
	case path.IsAbs(clean):
		return "", ErrPathAbsolute
	case clean == ".." || strings.HasPrefix(clean, "../"):
		return "", ErrPathEscapes
	}
	return clean, nil
}

func digestHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
