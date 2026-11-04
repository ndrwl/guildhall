package release

import (
	"regexp"
	"strings"
)

// semverPattern is a Go module version: v, then major.minor.patch with no leading zeros, an optional pre-release
// and optional build metadata, as `go install` and pseudo-versions name a release.
var semverPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// CompareVersions orders release versions a and b by semantic versioning precedence, as Go orders module versions,
// so a pseudo-version orders by its time: -1 when a is older, 0 when they are of equal precedence, 1 when a is newer.
// ok is false when either is not a semantic version, such as "dev", and then the two cannot be ordered.
func CompareVersions(a, b string) (order int, ok bool) {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range 3 {
		if c := compareNumeric(pa.core[i], pb.core[i]); c != 0 {
			return c, true
		}
	}
	return comparePre(pa.pre, pb.pre), true
}

type semver struct {
	core [3]string
	pre  []string
}

func parseSemver(v string) (semver, bool) {
	m := semverPattern.FindStringSubmatch(v)
	if m == nil {
		return semver{}, false
	}
	s := semver{core: [3]string{m[1], m[2], m[3]}}
	if m[4] != "" {
		s.pre = strings.Split(m[4], ".")
	}
	return s, true
}

// compareNumeric orders two decimal strings with no leading zeros, of any length.
func compareNumeric(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// comparePre orders pre-release identifiers: none is newer than any, numeric identifiers order numerically and
// before alphanumeric ones, alphanumeric ones in ASCII order, and a shorter list that is a prefix is older.
func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		numA, numB := isDigits(a[i]), isDigits(b[i])
		var c int
		switch {
		case numA && numB:
			c = compareNumeric(strings.TrimLeft(a[i], "0"), strings.TrimLeft(b[i], "0"))
		case numA:
			c = -1
		case numB:
			c = 1
		default:
			c = strings.Compare(a[i], b[i])
		}
		if c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
