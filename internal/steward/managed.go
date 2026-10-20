package steward

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
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

// Render returns the copy's config with its managed section replaced by src and the changes that makes.
// Everything else in the file, including local values, repositories and comments, is kept byte for byte.
// It refuses a copy that is not the target's Director, names another owner machine or other repositories,
// or whose local values a non-overridable managed key would silently override.
func Render(current []byte, src *ManagedSource, target Target) ([]byte, []Change, error) {
	before, err := config.Parse(current)
	if err != nil {
		return nil, nil, fmt.Errorf("current config: %w", err)
	}
	if err := checkTarget(before, target); err != nil {
		return nil, nil, err
	}
	oldManaged := before.Managed
	want := src.Settings()
	after := splice(current, want)
	cfg, err := config.Parse(after)
	if err != nil {
		return nil, nil, fmt.Errorf("rendered config: %w", err)
	}
	// The splice is textual; refuse unless exactly the managed section changed, as intended.
	managedOK := reflect.DeepEqual(cfg.Managed, want) || len(cfg.Managed) == 0 && len(want) == 0
	rest := *cfg
	rest.Managed, before.Managed = nil, nil
	if !managedOK || !reflect.DeepEqual(&rest, before) {
		return nil, nil, errors.New("managed settings in this copy must be written as [managed.<key>] tables; edit it by hand first")
	}
	return after, changes(oldManaged, src), nil
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

var managedHeader = regexp.MustCompile(`^\s*\[\s*managed\s*[.\]]`)

// splice replaces every [managed] and [managed.<key>] table in a TOML file with the given settings,
// keeping every other line, and the comments and blank lines that lead into the next table, unchanged.
// The result is idempotent; Render checks it semantically.
func splice(current []byte, managed map[string]config.ManagedSetting) []byte {
	text := string(current)
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	var out, dropped []string
	insertAt, firstHeader := -1, -1
	inManaged := false
	endManaged := func() {
		// Comments and blank lines ending a managed table lead into what follows, so they stay.
		i := len(dropped)
		for i > 0 && isTrivia(dropped[i-1]) {
			i--
		}
		out = append(out, dropped[i:]...)
		dropped = nil
	}
	for line := range strings.Lines(text) {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "[") {
			isManaged := managedHeader.MatchString(trimmed)
			switch {
			case isManaged && !inManaged:
				if insertAt < 0 {
					insertAt = len(out)
				}
			case isManaged:
				dropped = nil
			case inManaged:
				endManaged()
			}
			inManaged = isManaged
			if firstHeader < 0 && !isManaged {
				firstHeader = len(out)
			}
		}
		if inManaged {
			dropped = append(dropped, line)
		} else {
			out = append(out, line)
		}
	}
	if inManaged {
		endManaged()
	}

	block := renderManaged(managed, nl)
	if insertAt < 0 {
		if block == "" {
			return []byte(strings.Join(out, ""))
		}
		// A copy without managed tables gets them before its first table and the comments leading into it, or at the end.
		insertAt = len(out)
		if firstHeader >= 0 {
			insertAt = firstHeader
			for insertAt > 0 && isTrivia(out[insertAt-1]) {
				insertAt--
			}
		}
		if insertAt > 0 && !isBlank(out[insertAt-1]) {
			block = nl + block
		}
		if insertAt < len(out) && !isBlank(out[insertAt]) {
			block += nl
		}
	}
	if insertAt > 0 && !strings.HasSuffix(out[insertAt-1], "\n") {
		out[insertAt-1] += nl
	}
	return []byte(strings.Join(out[:insertAt], "") + block + strings.Join(out[insertAt:], ""))
}

func isBlank(line string) bool { return strings.TrimSpace(line) == "" }

func isTrivia(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// renderManaged writes managed settings as tables in key order, separated by blank lines, with no trailing blank line.
func renderManaged(managed map[string]config.ManagedSetting, nl string) string {
	var tables []string
	for _, key := range slices.Sorted(maps.Keys(managed)) {
		m := managed[key]
		table := "[managed." + key + "]" + nl + "value = " + tomlString(m.Value) + nl
		if m.Overridable {
			table += "overridable = true" + nl
		}
		tables = append(tables, table)
	}
	return strings.Join(tables, nl)
}

// tomlString encodes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
