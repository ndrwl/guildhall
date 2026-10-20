// Package steward implements the Steward's routine rollouts of managed settings to enrolled Director copies.
//
// The roster and the managed settings live in the private config directory and are never written here:
// enrolment, accounts and sign-in happen before a machine appears in the roster.
// A dry run fetches copies without remote writes; applying requires its saved plan,
// copying files with scp so the user's own SSH aliases, agent, keys and host-key checks apply.
package steward

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// RosterSchemaVersion is the newest roster schema this Guildhall reads.
const RosterSchemaVersion = 1

// OS is an enrolled machine's operating system, which decides its path form.
type OS string

const (
	OSLinux   OS = "linux"
	OSWindows OS = "windows"
	OSDarwin  OS = "darwin"
)

// Role is what an enrolled machine hosts.
type Role string

const (
	RoleSteward  Role = "steward"
	RoleDirector Role = "director"
)

// Roster lists the enrolled machines, their Directors and repositories.
// Every entry is an authorised rollout target; a rollout never adds one.
type Roster struct {
	SchemaVersion int       `toml:"schema_version"`
	Machines      []Machine `toml:"machine"`
}

// Machine is one enrolled machine, reached through one SSH alias for its one Guildhall account.
type Machine struct {
	Label string `toml:"label"`
	// SSHAlias is a Host entry in the Steward user's SSH config, which holds the address, user and key.
	SSHAlias  string     `toml:"ssh_alias"`
	OS        OS         `toml:"os"`
	Roles     []Role     `toml:"roles"`
	Directors []Director `toml:"director"`
}

// Director is one Director copy on its machine.
type Director struct {
	Name string `toml:"name"`
	// CopyDir holds the copy's config.toml and managed.toml; Windows paths use forward slashes.
	CopyDir      string   `toml:"copy_dir"`
	Repositories []string `toml:"repositories"`
}

// ConfigPath is the copy's config file on its machine.
func (d Director) ConfigPath() string { return d.CopyDir + "/config.toml" }

var (
	// aliasPattern admits plain SSH aliases only: no user, port, option or shell syntax.
	aliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	// Remote paths are restricted to characters no remote shell or scp interprets.
	posixDirPattern   = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
	windowsDirPattern = regexp.MustCompile(`^[A-Za-z]:(/[A-Za-z0-9._-]+)+$`)
)

// ParseRoster decodes and validates a roster.
func ParseRoster(data []byte) (*Roster, error) {
	var r Roster
	if _, err := decodeStrict("roster", data, RosterSchemaVersion, &r); err != nil {
		return nil, err
	}
	if problems := r.validate(); len(problems) > 0 {
		return nil, errors.New("invalid roster: " + strings.Join(problems, "; "))
	}
	return &r, nil
}

// decodeStrict decodes a versioned TOML file, refusing newer schemas and unknown keys.
func decodeStrict(what string, data []byte, supported int, v any) (toml.MetaData, error) {
	var head struct {
		SchemaVersion int `toml:"schema_version"`
	}
	if _, err := toml.Decode(string(data), &head); err != nil {
		return toml.MetaData{}, fmt.Errorf("%s: %w", what, err)
	}
	switch {
	case head.SchemaVersion > supported:
		return toml.MetaData{}, fmt.Errorf("%s: %w: schema_version %d (supported: %d)", what, config.ErrNewerSchema, head.SchemaVersion, supported)
	case head.SchemaVersion < 1:
		return toml.MetaData{}, fmt.Errorf("%s: schema_version must be set to a positive version", what)
	}
	md, err := toml.Decode(string(data), v)
	if err != nil {
		return toml.MetaData{}, fmt.Errorf("%s: %w", what, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
		}
		return md, fmt.Errorf("%s: unknown keys: %s", what, strings.Join(keys, ", "))
	}
	return md, nil
}

func (r *Roster) validate() []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	labels, aliases := map[string]bool{}, map[string]bool{}
	for i, m := range r.Machines {
		name := fmt.Sprintf("machine %d", i+1)
		if !workflow.ValidID(m.Label) {
			add("%s: invalid machine label %q", name, m.Label)
		} else {
			name = fmt.Sprintf("machine %q", m.Label)
		}
		if labels[m.Label] {
			add("duplicate machine label %q", m.Label)
		}
		labels[m.Label] = true
		if !aliasPattern.MatchString(m.SSHAlias) {
			add("%s: invalid ssh_alias %q: name a Host from your SSH config, without user or options", name, m.SSHAlias)
		} else if aliases[m.SSHAlias] {
			add("ssh_alias %q is used by more than one machine", m.SSHAlias)
		}
		aliases[m.SSHAlias] = true
		dirPattern := posixDirPattern
		switch m.OS {
		case OSLinux, OSDarwin:
		case OSWindows:
			dirPattern = windowsDirPattern
		default:
			add(`%s: os must be "linux", "windows" or "darwin", not %q`, name, m.OS)
		}
		if len(m.Roles) == 0 {
			add("%s: needs at least one role", name)
		}
		for j, role := range m.Roles {
			if role != RoleSteward && role != RoleDirector {
				add("%s: unknown role %q", name, role)
			} else if slices.Contains(m.Roles[:j], role) {
				add("%s: duplicate role %q", name, role)
			}
		}
		if len(m.Directors) > 0 && !slices.Contains(m.Roles, RoleDirector) {
			add("%s: lists directors but lacks the director role", name)
		}
		directors, dirs := map[string]bool{}, map[string]bool{}
		for _, d := range m.Directors {
			dname := fmt.Sprintf("%s director %q", name, d.Name)
			if !workflow.ValidID(d.Name) {
				add("%s: invalid director name %q", name, d.Name)
			} else if directors[d.Name] {
				add("%s: duplicate director %q", name, d.Name)
			}
			directors[d.Name] = true
			if !validDir(dirPattern, d.CopyDir) {
				add("%s: copy_dir %q must be an absolute %s path of letters, digits, '.', '_', '-' and '/' with no '.' or '..' segments", dname, d.CopyDir, m.OS)
			} else if dirs[d.CopyDir] {
				add("%s: copy_dir is used by more than one director", dname)
			}
			dirs[d.CopyDir] = true
			if len(d.Repositories) == 0 {
				add("%s: needs at least one repository", dname)
			}
			for j, id := range d.Repositories {
				if !workflow.ValidID(id) {
					add("%s: invalid repository id %q", dname, id)
				} else if slices.Contains(d.Repositories[:j], id) {
					add("%s: duplicate repository %q", dname, id)
				}
			}
		}
	}
	return problems
}

func validDir(pattern *regexp.Regexp, dir string) bool {
	if !pattern.MatchString(dir) {
		return false
	}
	for seg := range strings.SplitSeq(dir, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// Machine returns the enrolled machine with this label.
func (r *Roster) Machine(label string) (Machine, bool) {
	for _, m := range r.Machines {
		if m.Label == label {
			return m, true
		}
	}
	return Machine{}, false
}
