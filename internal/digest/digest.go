// Package digest is Guildhall's sha256 content digest and commit id spelling.
//
// A Digest is always "sha256:<64 lowercase hex>". Wire formats that carry the
// bare hex keep it and convert through ParseHex and Hex, so neither spelling is
// assembled or cut by hand. See docs/contracts/digest.md.
package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const prefix = "sha256:"

// Digest is the sha256 of some bytes, "sha256:<64 lowercase hex>". One made by
// Of, Parse or ParseHex always has that spelling.
type Digest string

// Of returns the digest of data.
func Of(data []byte) Digest {
	sum := sha256.Sum256(data)
	return Digest(prefix + hex.EncodeToString(sum[:]))
}

// Parse accepts s only in the prefixed spelling, "sha256:<64 lowercase hex>".
func Parse(s string) (Digest, error) {
	if h, ok := strings.CutPrefix(s, prefix); ok && isLowerHex(h, 64) {
		return Digest(s), nil
	}
	return "", fmt.Errorf(`digest %q is not "sha256:<64 lowercase hex>"`, s)
}

// ParseHex accepts h only in the bare spelling, 64 lowercase hex characters.
func ParseHex(h string) (Digest, error) {
	if isLowerHex(h, 64) {
		return Digest(prefix + h), nil
	}
	return "", fmt.Errorf("sha256 %q is not 64 lowercase hex characters", h)
}

// Hex returns the bare spelling: d without its "sha256:" prefix.
func (d Digest) Hex() string { return strings.TrimPrefix(string(d), prefix) }

// ValidHead reports whether s is a commit object id: 40 (SHA-1) or 64
// (SHA-256) lowercase hex characters.
func ValidHead(s string) bool { return isLowerHex(s, 40) || isLowerHex(s, 64) }

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
