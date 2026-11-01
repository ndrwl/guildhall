package config

import (
	"fmt"

	"github.com/ndrwl/guildhall/internal/release"
)

// ReleaseKey is the managed key naming the release a managed copy pins (docs/releases.md#the-pin): the Steward
// changes it by rollout, and `guildhall config reload --copy` pins the copy to it once it has authorized the
// revision and verified the release.
const ReleaseKey = "release"

// ReleaseSHA256Key is the managed key holding the sha256 of the binary of the release ReleaseKey names, which
// config reload verifies an installed release against, or installs one from releases/.incoming/ against.
const ReleaseSHA256Key = "release_sha256"

func checkReleaseVersion(v string) error {
	if err := release.ValidVersion(v); err != nil {
		return fmt.Errorf("%s: %w", ReleaseKey, err)
	}
	return nil
}

func checkReleaseSHA256(v string) error {
	if _, err := release.NormalizeSHA256(v); err != nil {
		return fmt.Errorf("%s: %w", ReleaseSHA256Key, err)
	}
	return nil
}

// checkRelease reports why the release keys cannot stand: they are managed keys only, never local, since a
// standalone copy's pin is set by guildhall release use; they are never overridable; and each needs the other.
func (c *Config) checkRelease() []string {
	var problems []string
	for _, key := range []string{ReleaseKey, ReleaseSHA256Key} {
		if _, ok := c.Local[key]; ok {
			problems = append(problems, fmt.Sprintf("local %s: the release pin is not a local setting; a standalone copy sets it with guildhall release use", key))
		}
		if m, ok := c.Managed[key]; ok && m.Overridable {
			problems = append(problems, fmt.Sprintf("managed %s cannot be overridable: a managed copy's release comes from its rollout only", key))
		}
	}
	_, version := c.Managed[ReleaseKey]
	_, sum := c.Managed[ReleaseSHA256Key]
	if version != sum {
		problems = append(problems, fmt.Sprintf("managed %s and %s go together: set both or neither", ReleaseKey, ReleaseSHA256Key))
	}
	return problems
}

// ManagedRelease returns the release the managed section pins and its sha256, in lowercase, and false when it pins
// none. Validation has checked both values.
func (c *Config) ManagedRelease() (version, sha256 string, ok bool) {
	v, okV := c.Managed[ReleaseKey]
	s, okS := c.Managed[ReleaseSHA256Key]
	if !okV || !okS {
		return "", "", false
	}
	sum, err := release.NormalizeSHA256(s.Value)
	if err != nil {
		return "", "", false
	}
	return v.Value, sum, true
}
