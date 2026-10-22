package config

import (
	"errors"
	"fmt"
	"strconv"
)

// The guard rails (docs/director.md#guard-rails) are limits the binary enforces on a Director whatever it asks:
// the repositories it may act on, each repository's merge rule, and caps on the Artisan attempts it starts.
// The caps are settings read through Setting, so a Steward's managed value is read-only to the Director and a
// standalone copy sets them under [local].
const (
	// MaxRunningAttemptsKey is the setting that caps how many attempts may run at once across the Director.
	MaxRunningAttemptsKey = "max_running_attempts"
	// MaxAttemptsPerDayKey is the setting that caps how many attempts the Director may start in one UTC day.
	MaxAttemptsPerDayKey = "max_attempts_per_day"
)

// DefaultMaxRunningAttempts and DefaultMaxAttemptsPerDay are the caps of a config that does not set them.
const (
	DefaultMaxRunningAttempts = 4
	DefaultMaxAttemptsPerDay  = 40
)

// ErrRepositoryNotListed reports a repository the config does not list: the repositories guard rail.
var ErrRepositoryNotListed = errors.New("guard rail repositories: the config does not list repository")

// GuardRails is the effective value of every guard rail.
type GuardRails struct {
	// Repositories are the ids of the listed repositories, in config order; every other repository is refused.
	Repositories []string `json:"repositories"`
	// MergePolicies is each listed repository's merge_policy, which deliver enforces.
	MergePolicies map[string]MergePolicy `json:"merge_policies"`
	// MaxRunningAttempts and MaxAttemptsPerDay are the attempt caps, each at least 1.
	MaxRunningAttempts int `json:"max_running_attempts"`
	MaxAttemptsPerDay  int `json:"max_attempts_per_day"`
}

// GuardRails returns the effective guard rails: the listed repositories and their merge policies, and each cap's
// effective setting, its default when unset. A cap that is not a positive decimal integer is an error naming its
// key; Parse refuses one already.
func (c *Config) GuardRails() (GuardRails, error) {
	g := GuardRails{Repositories: []string{}, MergePolicies: map[string]MergePolicy{}}
	for _, r := range c.Repositories {
		g.Repositories = append(g.Repositories, r.ID)
		g.MergePolicies[r.ID] = r.MergePolicy
	}
	var err error
	if g.MaxRunningAttempts, err = c.capSetting(MaxRunningAttemptsKey, DefaultMaxRunningAttempts); err != nil {
		return GuardRails{}, err
	}
	if g.MaxAttemptsPerDay, err = c.capSetting(MaxAttemptsPerDayKey, DefaultMaxAttemptsPerDay); err != nil {
		return GuardRails{}, err
	}
	return g, nil
}

func (c *Config) capSetting(key string, def int) (int, error) {
	v, ok := c.Setting(key)
	if !ok {
		return def, nil
	}
	return parseCap(key, v)
}

// ListedRepository returns the repository with this id, or an error wrapping ErrRepositoryNotListed that names it.
// Every command that acts on a repository finds it here.
func (c *Config) ListedRepository(id string) (Repository, error) {
	r, ok := c.Repository(id)
	if !ok {
		return Repository{}, fmt.Errorf("%w %q", ErrRepositoryNotListed, id)
	}
	return r, nil
}

func parseCap(key, v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || strconv.Itoa(n) != v {
		return 0, fmt.Errorf("%s %q must be a positive whole number", key, v)
	}
	return n, nil
}
