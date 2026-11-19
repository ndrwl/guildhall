package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPruneUntracked: prune_untracked is a repository key, false unless the repository's block sets it, and a
// config without it, or with it false, digests as before the key existed, since it is omitted from JSON when unset.
func TestPruneUntracked(t *testing.T) {
	encode := func(cfg *Config) string {
		t.Helper()
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	base := director(t)
	cfg, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range cfg.Repositories {
		if r.PruneUntracked {
			t.Errorf("repository %q: PruneUntracked set without prune_untracked", r.ID)
		}
	}
	before := encode(cfg)
	if strings.Contains(before, `"PruneUntracked"`) {
		t.Fatalf("config JSON without prune_untracked = %s", before)
	}

	off, err := Parse([]byte(strings.Replace(base, "no_ci = true\n", "no_ci = true\nprune_untracked = false\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if got := encode(off); got != before {
		t.Errorf("config JSON with prune_untracked = false = %s, want %s", got, before)
	}

	on, err := Parse([]byte(strings.Replace(base, "no_ci = true\n", "no_ci = true\nprune_untracked = true\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if on.Repositories[0].PruneUntracked || !on.Repositories[1].PruneUntracked {
		t.Errorf("PruneUntracked = %v, %v; want it set for site alone", on.Repositories[0].PruneUntracked, on.Repositories[1].PruneUntracked)
	}
	if got := encode(on); !strings.Contains(got, `"PruneUntracked":true`) {
		t.Errorf("config JSON with prune_untracked = %s", got)
	}
}
