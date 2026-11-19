package secrets_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/secrets"
)

func TestValidSHA256(t *testing.T) {
	hex := strings.Repeat("0123456789abcdef", 4)
	for _, c := range []struct {
		name string
		in   string
		want bool
	}{
		{"64 lowercase hex", hex, true},
		{"uppercase", strings.ToUpper(hex), false},
		{"63 characters", hex[:63], false},
		{"65 characters", hex + "a", false},
		{"sha256: prefix", "sha256:" + hex, false},
		{"leading space", " " + hex, false},
		{"trailing space", hex + " ", false},
		{"trailing newline", hex + "\n", false},
		{"non-hex character", "g" + hex[1:], false},
		{"empty", "", false},
	} {
		if got := secrets.ValidSHA256(c.in); got != c.want {
			t.Errorf("%s: ValidSHA256(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// TestNoSHA256HexCopies is the d260 regression guard: the sha256 hex spelling has one owner,
// internal/digest, so no non-test Go file under internal may keep its own sha256Hex.
func TestNoSHA256HexCopies(t *testing.T) {
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "sha256Hex") {
				t.Errorf("%s:%d: sha256Hex copy; check through internal/digest instead: %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
