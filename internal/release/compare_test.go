package release_test

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/release"
)

// TestCompareVersions: versions order by semantic versioning precedence, pseudo-versions by their time, and a
// version that is not semantic cannot be ordered.
func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		order int
		ok    bool
	}{
		{"v0.4.0", "v0.5.0", -1, true},
		{"v0.10.0", "v0.9.9", 1, true},
		{"v1.2.3", "v1.2.3", 0, true},
		{"v1.2.3+meta", "v1.2.3", 0, true},
		{"v1.2.3-rc.1", "v1.2.3", -1, true},
		{"v1.2.3-rc.2", "v1.2.3-rc.10", -1, true},
		{"v1.2.3-rc.1", "v1.2.3-rc.1.1", -1, true},
		{"v1.2.3-1", "v1.2.3-alpha", -1, true},
		{"v0.0.0-20261103120000-aaaaaaaaaaaa", "v0.0.0-20261104072620-32d94e3e3ccf", -1, true},
		{"v0.5.1-0.20261104072620-32d94e3e3ccf", "v0.5.0", 1, true},
		{"v0.5.1-0.20261104072620-32d94e3e3ccf", "v0.5.1", -1, true},
		{"dev", "v0.5.0", 0, false},
		{"v0.5", "v0.5.0", 0, false},
		{"v01.0.0", "v1.0.0", 0, false},
	} {
		order, ok := release.CompareVersions(c.a, c.b)
		if order != c.order || ok != c.ok {
			t.Errorf("CompareVersions(%q, %q) = %d, %t; want %d, %t", c.a, c.b, order, ok, c.order, c.ok)
		}
		if ok {
			if back, _ := release.CompareVersions(c.b, c.a); back != -order {
				t.Errorf("CompareVersions(%q, %q) = %d, not the reverse of %d", c.b, c.a, back, order)
			}
		}
	}
}
