package steward

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
)

// ManagedSchemaVersion is the newest managed-settings source schema this Guildhall reads.
const ManagedSchemaVersion = 1

// ManagedSource is the Steward's private managed settings, rolled out to every copy it targets.
type ManagedSource struct {
	SchemaVersion int                      `toml:"schema_version"`
	Managed       map[string]SourceSetting `toml:"managed"`
}

// SourceSetting is one managed setting as the Steward keeps it.
type SourceSetting struct {
	Value       string `toml:"value"`
	Overridable bool   `toml:"overridable"`
	// Policy marks a key, such as a review or approval rule, whose every change needs Guildmaster approval.
	// It stays in the private source and is not written to copies.
	Policy bool `toml:"policy"`
}

// ParseManaged decodes and validates a managed-settings source.
func ParseManaged(data []byte) (*ManagedSource, error) {
	var src ManagedSource
	md, err := decodeStrict("managed settings", data, ManagedSchemaVersion, &src)
	if err != nil {
		return nil, err
	}
	// The TOML decoder leaves a map empty, without an error, when the file gives it a plain value.
	if typ := md.Type("managed"); typ != "" && typ != "Hash" {
		return nil, errors.New("managed settings: managed must hold [managed.<key>] tables")
	}
	var problems []string
	for _, key := range slices.Sorted(maps.Keys(src.Managed)) {
		if err := config.CheckKey(key); err != nil {
			problems = append(problems, fmt.Sprintf("managed %q: %v", key, err))
		}
	}
	if len(problems) > 0 {
		return nil, errors.New("invalid managed settings: " + strings.Join(problems, "; "))
	}
	return &src, nil
}

// Settings returns the managed section a copy receives.
func (s *ManagedSource) Settings() map[string]config.ManagedSetting {
	out := make(map[string]config.ManagedSetting, len(s.Managed))
	for k, v := range s.Managed {
		out[k] = config.ManagedSetting{Value: v.Value, Overridable: v.Overridable}
	}
	return out
}

// Target is one Director copy a rollout writes to.
type Target struct {
	Machine  string
	Director Director
}

// ChangeKind says how a managed key changes.
type ChangeKind string

const (
	Added   ChangeKind = "added"
	Changed ChangeKind = "changed"
	Removed ChangeKind = "removed"
)

// Change is one managed key's change on one copy.
type Change struct {
	Key    string                 `json:"key"`
	Kind   ChangeKind             `json:"kind"`
	Before *config.ManagedSetting `json:"before,omitempty"`
	After  *config.ManagedSetting `json:"after,omitempty"`
	// NeedsApproval is set for changes outside routine rollout authority; Reason says why.
	NeedsApproval bool   `json:"needs_approval,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

func checkTarget(cfg *config.Config, target Target) error {
	if cfg.Mode != config.ModeDirector {
		return fmt.Errorf("not a director copy (mode %q)", cfg.Mode)
	}
	if cfg.Name != target.Director.Name {
		return fmt.Errorf("copy names director %q, but the roster expects %q", cfg.Name, target.Director.Name)
	}
	var ids []string
	for _, r := range cfg.Repositories {
		if r.OwnerMachine != target.Machine {
			return fmt.Errorf("repository %q names owner machine %q, but the roster places this copy on %q", r.ID, r.OwnerMachine, target.Machine)
		}
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	listed := slices.Sorted(slices.Values(target.Director.Repositories))
	if !slices.Equal(ids, listed) {
		return fmt.Errorf("roster lists repositories %s, but the copy has %s; update the roster through enrolment", strings.Join(listed, ", "), strings.Join(ids, ", "))
	}
	return nil
}

func changes(before map[string]config.ManagedSetting, src *ManagedSource) []Change {
	var out []Change
	keys := slices.Collect(maps.Keys(before))
	for k := range src.Managed {
		if _, had := before[k]; !had {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		old, had := before[key]
		next, has := src.Managed[key]
		setting := config.ManagedSetting{Value: next.Value, Overridable: next.Overridable}
		c := Change{Key: key}
		switch {
		case had && has && old == setting:
			continue
		case !had:
			c.Kind, c.After = Added, &setting
		case !has:
			c.Kind, c.Before = Removed, &old
			c.NeedsApproval, c.Reason = true, "removing a managed key lets copies set it locally"
		default:
			c.Kind, c.Before, c.After = Changed, &old, &setting
			if !old.Overridable && setting.Overridable {
				c.NeedsApproval, c.Reason = true, "making a managed key overridable lets copies replace it"
			}
		}
		if has && next.Policy {
			c.NeedsApproval, c.Reason = true, "the key is marked policy"
		}
		out = append(out, c)
	}
	return out
}
