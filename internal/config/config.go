// Package config parses and validates a Guildhall copy's configuration file.
//
// The file is TOML and versioned by schema_version.
// It never holds credentials; sign-in stays in each runtime's own store.
// Workflow definitions are separate JSON files (package workflow); repositories select them by name.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ndrwl/guildhall/internal/workflow"
)

// SchemaVersion is the newest config schema this Guildhall reads.
const SchemaVersion = 1

// ErrNewerSchema reports a config written for a newer Guildhall; it is refused rather than guessed at.
var ErrNewerSchema = errors.New("config schema is newer than this Guildhall supports")

// Mode is the role a Guildhall copy runs as.
type Mode string

const (
	ModeSteward  Mode = "steward"
	ModeDirector Mode = "director"
)

// MergePolicy decides how a repository's finished work is merged.
type MergePolicy string

const (
	// MergeApproval asks the Guildmaster before every merge; it is the default.
	MergeApproval MergePolicy = "approval"
	// MergeAuto merges only when checks pass, the independent review is complete
	// for the exact head and the work is within accepted scope; otherwise it asks.
	MergeAuto MergePolicy = "auto"
)

// DefaultWorkflow is the workflow a repository runs when it names none.
const DefaultWorkflow = "default"

// StallIntervalKey is the setting that sets the interpreter's worker-stalled interval (docs/configuration.md#settings).
const StallIntervalKey = "stall_interval"

// DefaultStallInterval is the stall interval of a config that does not set StallIntervalKey.
const DefaultStallInterval = 15 * time.Minute

// ChecksRefreshKey is the setting that sets how long a waiting validate step waits between reads of the same
// head's checks (docs/configuration.md#settings).
const ChecksRefreshKey = "checks_refresh"

// DefaultChecksRefresh is the checks refresh of a config that does not set ChecksRefreshKey.
const DefaultChecksRefresh = time.Minute

// HerdrSessionKey is the setting that names the Herdr session the Director's tab opens in
// (docs/configuration.md#settings).
const HerdrSessionKey = "herdr_session"

// Config is one copy's parsed configuration.
type Config struct {
	SchemaVersion int  `toml:"schema_version"`
	Mode          Mode `toml:"mode"`
	// Name is the Director's name; required in director mode.
	Name string `toml:"name"`
	// PrivateConfig points at the private preferences repository; Steward only.
	PrivateConfig *PrivateConfig `toml:"private_config"`
	// Managed is written only by Steward rollouts.
	Managed map[string]ManagedSetting `toml:"managed"`
	// Local is owned by this copy and may override managed keys marked overridable.
	Local        map[string]string `toml:"local"`
	Repositories []Repository      `toml:"repository"`
	// Profiles are the Director's named Artisan profiles, keyed by the name workflow steps give in "profile".
	Profiles map[string]Profile `toml:"profile"`
	// Director chooses who runs the Director's own session (docs/director.md#start); Director mode only.
	// It is omitted from JSON when unset, so the config digest of a config without it is unchanged.
	Director *Director `toml:"director" json:",omitempty"`
}

// Director chooses the runtime adapter, model and effort of the Director's session and optionally its role
// prompt. Like a profile it names no credential. Adapter is required to start a session, not to parse.
type Director struct {
	// Adapter names the runtime adapter, such as "claude"; a positional runtime overrides it for one command.
	Adapter string `toml:"adapter"`
	// Model and Effort are passed to the adapter as given; empty means the adapter's default.
	Model  string `toml:"model"`
	Effort string `toml:"effort"`
	// Role is a slash-separated path relative to the config file's directory; empty keeps the copy's
	// baseline/roles/director.md, else the release's.
	Role string `toml:"role"`
	// IdleAfter is how long a Director session with turn hooks may go without a turn signal or a wake before it
	// counts as idle, as a Go duration; empty means DefaultIdleAfter.
	IdleAfter string `toml:"idle_after" json:",omitempty"`
	// HandoffAfterTasks, HandoffContextPercent and HandoffIdle are the automatic handoff triggers
	// (docs/director.md#automatic-handoff): finished tasks per crossing, 0 for off; the used context percent, 0 to
	// 100 with 0 for off, DefaultHandoffContextPercent when unset; and a quiet period as a Go duration, "0s" for off
	// and when unset.
	HandoffAfterTasks     int    `toml:"handoff_after_tasks" json:",omitempty"`
	HandoffContextPercent *int   `toml:"handoff_context_percent" json:",omitempty"`
	HandoffIdle           string `toml:"handoff_idle" json:",omitempty"`
}

// DefaultHandoffContextPercent is the [director] handoff_context_percent a config that does not set it gets.
const DefaultHandoffContextPercent = 70

// Handoff is a config's effective automatic handoff triggers; zero turns a trigger off.
type Handoff struct {
	AfterTasks     int
	ContextPercent int
	Idle           time.Duration
}

// DefaultIdleAfter is the [director] idle_after a config that does not set it gets.
const DefaultIdleAfter = 5 * time.Minute

// ErrUnknownProfile reports a profile name that the Director config does not declare.
var ErrUnknownProfile = errors.New("unknown profile")

// Profile chooses who runs an Artisan stage that names it: the runtime adapter, model and effort,
// and optionally a role prompt replacing the release's Artisan role.
// It names no credential; sign-in stays in each runtime's own store.
type Profile struct {
	// Adapter names the runtime adapter, such as "claude"; required.
	Adapter string `toml:"adapter"`
	// Model and Effort are passed to the adapter as given; empty means the adapter's default.
	Model  string `toml:"model"`
	Effort string `toml:"effort"`
	// Role is a slash-separated path relative to the config file's directory; empty keeps the release role.
	Role string `toml:"role"`
}

// PrivateConfig is a reference to the private config repository, never a credential.
type PrivateConfig struct {
	Location string `toml:"location"`
}

// ManagedSetting is one Steward-managed value and whether a local value may replace it.
type ManagedSetting struct {
	Value       string `toml:"value"`
	Overridable bool   `toml:"overridable"`
}

// Repository is one repository a Director owns.
type Repository struct {
	ID           string      `toml:"id"`
	Path         string      `toml:"path"`
	Remote       string      `toml:"remote"`
	OwnerMachine string      `toml:"owner_machine"`
	MergePolicy  MergePolicy `toml:"merge_policy"`
	// Workflow names the Director's workflow definition this repository runs.
	Workflow string `toml:"workflow"`
	// WorkflowOverrides chooses step actors for this repository only, keyed by step id (workflow.ApplyOverrides).
	WorkflowOverrides map[string]workflow.Override `toml:"workflow_overrides"`
	// NoCI declares that the repository runs no checks, so validate passes a head its forge reports none for.
	NoCI bool `toml:"no_ci" json:",omitempty"`
}

var (
	keyPattern        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	credentialPattern = regexp.MustCompile(`(?i)(token|passw|secret|api_?key|credential|private_?key)`)
	drivePathPattern  = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

// ownerAbsolute reports whether path is absolute on its owner machine, which may run another OS than this host:
// a POSIX path from the root or a Windows path from a drive letter.
func ownerAbsolute(path string) bool {
	return strings.HasPrefix(path, "/") || drivePathPattern.MatchString(path)
}

// configRelative reports whether p names a file below the config directory on every platform:
// slash-separated, relative, without dot segments, backslashes or a drive letter.
func configRelative(p string) bool {
	return fs.ValidPath(p) && p != "." && !strings.Contains(p, `\`) && !strings.Contains(p, ":")
}

func ownerPathKey(p string) string {
	if drivePathPattern.MatchString(p) {
		return strings.ToLower(p[:2]) + path.Clean(strings.ReplaceAll(p[2:], `\`, "/"))
	}
	return path.Clean(p)
}

// Parse decodes and validates a config, applying defaults.
func Parse(data []byte) (*Config, error) {
	var head struct {
		SchemaVersion int `toml:"schema_version"`
	}
	if _, err := toml.Decode(string(data), &head); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	switch {
	case head.SchemaVersion > SchemaVersion:
		return nil, fmt.Errorf("%w: schema_version %d (supported: %d)", ErrNewerSchema, head.SchemaVersion, SchemaVersion)
	case head.SchemaVersion < 1:
		return nil, errors.New("config: schema_version must be set to a positive version")
	}
	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("config: unknown keys: %s", strings.Join(keys, ", "))
	}
	cfg.applyDefaults()
	if problems := cfg.validate(); len(problems) > 0 {
		return nil, errors.New("invalid config: " + strings.Join(problems, "; "))
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	for i := range c.Repositories {
		r := &c.Repositories[i]
		if r.MergePolicy == "" {
			r.MergePolicy = MergeApproval
		}
		if r.Workflow == "" {
			r.Workflow = DefaultWorkflow
		}
	}
}

func (c *Config) validate() []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	switch c.Mode {
	case ModeDirector:
		if c.Name == "" {
			add("a director needs a name")
		} else if !workflow.ValidID(c.Name) {
			add("invalid director name %q", c.Name)
		}
		if c.PrivateConfig != nil {
			add("private_config is for the steward only")
		}
		if len(c.Repositories) == 0 {
			add("a director needs at least one repository")
		}
	case ModeSteward:
		if c.PrivateConfig == nil || strings.TrimSpace(c.PrivateConfig.Location) == "" {
			add("the steward needs private_config.location")
		} else if hasCredential(c.PrivateConfig.Location) {
			add("private_config.location must not contain credentials")
		}
		if len(c.Repositories) > 0 {
			add("repositories belong to a director, not the steward")
		}
		if len(c.Profiles) > 0 {
			add("profiles belong to a director, not the steward")
		}
		if c.Director != nil {
			add("the director table belongs to a director, not the steward")
		}
	default:
		add(`mode must be "steward" or "director", not %q`, c.Mode)
	}

	for _, key := range slices.Sorted(maps.Keys(c.Managed)) {
		problems = append(problems, checkKey("managed", key)...)
	}
	for _, key := range slices.Sorted(maps.Keys(c.Local)) {
		problems = append(problems, checkKey("local", key)...)
		if m, managed := c.Managed[key]; managed && !m.Overridable {
			add("local %q overrides a managed key that is not overridable", key)
		}
	}

	if m, ok := c.Managed[StallIntervalKey]; ok {
		if _, err := parseStallInterval(m.Value); err != nil {
			add("managed %v", err)
		}
	}
	if v, ok := c.Local[StallIntervalKey]; ok {
		if _, err := parseStallInterval(v); err != nil {
			add("local %v", err)
		}
	}
	for _, key := range []string{MaxRunningAttemptsKey, MaxAttemptsPerDayKey} {
		if m, ok := c.Managed[key]; ok {
			if _, err := parseCap(key, m.Value); err != nil {
				add("managed %v", err)
			}
		}
		if v, ok := c.Local[key]; ok {
			if _, err := parseCap(key, v); err != nil {
				add("local %v", err)
			}
		}
	}
	if m, ok := c.Managed[HerdrSessionKey]; ok {
		if err := checkHerdrSession(m.Value); err != nil {
			add("managed %v", err)
		}
	}
	if v, ok := c.Local[HerdrSessionKey]; ok {
		if err := checkHerdrSession(v); err != nil {
			add("local %v", err)
		}
	}
	if m, ok := c.Managed[ChecksRefreshKey]; ok {
		if _, err := parseChecksRefresh(m.Value); err != nil {
			add("managed %v", err)
		}
	}
	if v, ok := c.Local[ChecksRefreshKey]; ok {
		if _, err := parseChecksRefresh(v); err != nil {
			add("local %v", err)
		}
	}
	problems = append(problems, c.checkQuality()...)

	for _, name := range slices.Sorted(maps.Keys(c.Profiles)) {
		p := c.Profiles[name]
		if !workflow.ValidID(name) {
			add("invalid profile name %q", name)
			continue
		}
		switch {
		case p.Adapter == "":
			add("profile %q: adapter is required", name)
		case !workflow.ValidID(p.Adapter):
			add("profile %q: invalid adapter %q", name, p.Adapter)
		}
		if p.Role != "" && !configRelative(p.Role) {
			add("profile %q: role must be a relative slash-separated path inside the config directory", name)
		}
	}

	if d := c.Director; d != nil {
		if d.Adapter != "" && !workflow.ValidID(d.Adapter) {
			add("director: invalid adapter %q", d.Adapter)
		}
		if d.Role != "" && !configRelative(d.Role) {
			add("director: role must be a relative slash-separated path inside the config directory")
		}
		if d.IdleAfter != "" {
			if _, err := parseIdleAfter(d.IdleAfter); err != nil {
				add("director: %v", err)
			}
		}
		if d.HandoffAfterTasks < 0 {
			add("director: handoff_after_tasks %d must not be negative", d.HandoffAfterTasks)
		}
		if p := d.HandoffContextPercent; p != nil && (*p < 0 || *p > 100) {
			add("director: handoff_context_percent %d must be between 0 and 100", *p)
		}
		if d.HandoffIdle != "" {
			if _, err := parseHandoffIdle(d.HandoffIdle); err != nil {
				add("director: %v", err)
			}
		}
	}

	seenID, seenPath := map[string]bool{}, map[string]bool{}
	var machines []string
	for i, r := range c.Repositories {
		name := fmt.Sprintf("repository %d", i+1)
		if !workflow.ValidID(r.ID) {
			add("%s: invalid repository id %q", name, r.ID)
		} else {
			name = fmt.Sprintf("repository %q", r.ID)
		}
		if seenID[r.ID] {
			add("duplicate repository id %q", r.ID)
		}
		seenID[r.ID] = true
		if !ownerAbsolute(r.Path) {
			add("%s: path must be absolute", name)
		} else if clean := ownerPathKey(r.Path); seenPath[clean] {
			add("%s: path is already used by another repository", name)
		} else {
			seenPath[clean] = true
		}
		if strings.TrimSpace(r.Remote) == "" {
			add("%s: remote is required", name)
		} else if hasCredential(r.Remote) {
			add("%s: remote must not contain credentials", name)
		}
		if r.OwnerMachine == "" {
			add("%s: owner_machine is required", name)
		} else if !slices.Contains(machines, r.OwnerMachine) {
			machines = append(machines, r.OwnerMachine)
		}
		if r.MergePolicy != MergeApproval && r.MergePolicy != MergeAuto {
			add(`%s: merge_policy must be "approval" or "auto", not %q`, name, r.MergePolicy)
		}
		if !workflow.ValidID(r.Workflow) {
			add("%s: invalid workflow name %q", name, r.Workflow)
		}
	}
	if len(machines) > 1 {
		add("a director has one owner machine, but repositories name %s", strings.Join(machines, ", "))
	}
	return problems
}

// CheckKey reports why key cannot name a managed or local setting, or nil if it can.
func CheckKey(key string) error {
	if problems := checkKey("setting", key); len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func checkKey(section, key string) []string {
	var problems []string
	if !keyPattern.MatchString(key) {
		problems = append(problems, fmt.Sprintf("%s: invalid key %q", section, key))
	}
	if credentialPattern.MatchString(key) {
		problems = append(problems, fmt.Sprintf("%s: key %q looks like a credential; credentials never belong in config", section, key))
	}
	return problems
}

// hasCredential reports whether a location embeds a password or, for web URLs, any user info.
// SSH user names such as git@host are not credentials.
func hasCredential(location string) bool {
	u, err := url.Parse(location)
	if err != nil || u.User == nil {
		return false
	}
	if _, set := u.User.Password(); set {
		return true
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// Setting returns the effective value of a preference:
// the local value where the managed key allows it or no managed key exists, otherwise the managed value.
func (c *Config) Setting(key string) (string, bool) {
	m, managed := c.Managed[key]
	if v, local := c.Local[key]; local && (!managed || m.Overridable) {
		return v, true
	}
	return m.Value, managed
}

// StallInterval returns the effective StallIntervalKey setting as a duration: DefaultStallInterval when
// it is not set, and zero, which disables worker-stalled observations, for "0s".
// A value that is not a nonnegative Go duration is an error; Parse refuses one already.
func (c *Config) StallInterval() (time.Duration, error) {
	v, ok := c.Setting(StallIntervalKey)
	if !ok {
		return DefaultStallInterval, nil
	}
	return parseStallInterval(v)
}

func parseStallInterval(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s %q must be a nonnegative duration such as \"15m\", or \"0s\" to disable", StallIntervalKey, v)
	}
	return d, nil
}

// ChecksRefresh returns the effective ChecksRefreshKey setting as a duration: DefaultChecksRefresh when it is
// not set, and zero, which reads the checks at every advance, for "0s".
// A value that is not a nonnegative Go duration is an error; Parse refuses one already.
func (c *Config) ChecksRefresh() (time.Duration, error) {
	v, ok := c.Setting(ChecksRefreshKey)
	if !ok {
		return DefaultChecksRefresh, nil
	}
	return parseChecksRefresh(v)
}

func parseChecksRefresh(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s %q must be a nonnegative duration such as \"1m\", or \"0s\" to read at every advance", ChecksRefreshKey, v)
	}
	return d, nil
}

// HerdrSession returns the effective HerdrSessionKey setting: the named Herdr session the Director's tab opens
// in, or "" when it is not set.
// A value Herdr would not take as a session name, or Herdr's default session, is an error; Parse refuses one
// already.
func (c *Config) HerdrSession() (string, error) {
	v, ok := c.Setting(HerdrSessionKey)
	if !ok {
		return "", nil
	}
	return v, checkHerdrSession(v)
}

// herdrSessionName is the shape of a Herdr session name the herdr client accepts.
var herdrSessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func checkHerdrSession(v string) error {
	switch {
	case v == "default":
		return fmt.Errorf(`%s "default" is refused: name a dedicated Herdr session`, HerdrSessionKey)
	case !herdrSessionName.MatchString(v):
		return fmt.Errorf("%s %q must be letters, digits, '-' or '_', starting with a letter or digit", HerdrSessionKey, v)
	}
	return nil
}

// IdleAfter returns the effective [director] idle_after as a duration: DefaultIdleAfter when it is not set.
// A value that is not a positive Go duration is an error; Parse refuses one already.
func (c *Config) IdleAfter() (time.Duration, error) {
	if c.Director == nil || c.Director.IdleAfter == "" {
		return DefaultIdleAfter, nil
	}
	return parseIdleAfter(c.Director.IdleAfter)
}

// Handoff returns the effective [director] handoff triggers: DefaultHandoffContextPercent and the others off when
// they are not set. A value Parse would refuse is an error.
func (c *Config) Handoff() (Handoff, error) {
	h := Handoff{ContextPercent: DefaultHandoffContextPercent}
	d := c.Director
	if d == nil {
		return h, nil
	}
	h.AfterTasks = d.HandoffAfterTasks
	if d.HandoffContextPercent != nil {
		h.ContextPercent = *d.HandoffContextPercent
	}
	var err error
	if d.HandoffIdle != "" {
		h.Idle, err = parseHandoffIdle(d.HandoffIdle)
	}
	if err == nil && (h.AfterTasks < 0 || h.ContextPercent < 0 || h.ContextPercent > 100) {
		err = fmt.Errorf("handoff thresholds %d tasks and %d%% are out of range", h.AfterTasks, h.ContextPercent)
	}
	return h, err
}

func parseHandoffIdle(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("handoff_idle %q must be a duration such as \"30m\", or \"0s\" for off", v)
	}
	return d, nil
}

func parseIdleAfter(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("idle_after %q must be a positive duration such as \"5m\"", v)
	}
	return d, nil
}

// Profile returns the profile with this name, or an error wrapping ErrUnknownProfile.
func (c *Config) Profile(name string) (Profile, error) {
	p, ok := c.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("%w %q", ErrUnknownProfile, name)
	}
	return p, nil
}

// CheckProfiles refuses a workflow whose steps name a profile this config does not declare,
// listing every such step in one error that wraps ErrUnknownProfile.
// Run it with the repository's effective workflow before a task binds it.
func (c *Config) CheckProfiles(def *workflow.Definition) error {
	var problems []string
	for _, step := range def.Steps {
		if name := step.Profile(); name != "" {
			if _, ok := c.Profiles[name]; !ok {
				problems = append(problems, fmt.Sprintf("step %q names unknown profile %q", step.ID, name))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("workflow %q: %w: %s", def.Name, ErrUnknownProfile, strings.Join(problems, "; "))
	}
	return nil
}

// Repository returns the repository with this id.
func (c *Config) Repository(id string) (Repository, bool) {
	for _, r := range c.Repositories {
		if r.ID == id {
			return r, true
		}
	}
	return Repository{}, false
}
