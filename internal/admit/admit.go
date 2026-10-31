// Package admit is the shared configuration-admission boundary for a Director copy (docs/director-modes.md#mode).
// Every Director command that needs effective settings, and every Artisan attempt launched from a Director copy,
// loads the copy's config through it: it refuses inline [managed] in every mode, applies the mode table from the
// copy's director.store and refuses a managed.toml digest that store has not authorized.
package admit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
)

// ManagedFile is the managed settings file a Steward rollout publishes beside a Director copy's config.toml.
const ManagedFile = "managed.toml"

// StoreFile is a Director copy's store, beside its config.toml.
const StoreFile = "director.store"

// A Director copy's management mode (docs/director-modes.md#mode): the newest enrolment event in its store and
// whether managed.toml is present decide it. A copy the mode table refuses is in mode refused.
const (
	ModeStandalone = "standalone"
	ModeManaged    = "managed"
	ModeRefused    = "refused"
)

// Admission is what the boundary found for a Director copy: its mode, the first refusal, if any, and the managed
// facts behind them.
type Admission struct {
	// Mode is standalone, managed or refused.
	Mode string `json:"mode"`
	// Refusal names the state that refuses the copy's settings and the command that fixes it; empty when the
	// settings are admitted. A managed copy whose revision is not authorized has one.
	Refusal string `json:"refusal,omitempty"`
	// Steward is the label of the Steward the copy is enrolled with.
	Steward string `json:"steward,omitempty"`
	// ManagedDigest is the digest of the managed.toml on disk, if there is one.
	ManagedDigest string `json:"managed_digest,omitempty"`
	// Authorized reports whether the copy's store authorizes ManagedDigest, by a reload or the Guildmaster's
	// approval recorded under the current enrolment; enrolment alone never does.
	Authorized bool `json:"authorized"`
	// Enrolment is the newest enrolment event, and LastRevision the newest managed-revision event of it.
	Enrolment    *store.Enrolment       `json:"enrolment,omitempty"`
	LastRevision *store.ManagedRevision `json:"last_revision,omitempty"`
	// inline is set when config.toml defines an inline [managed] table.
	inline bool
}

// RefusalError is a refusal of the boundary, as Admit returns it.
type RefusalError struct{ Text string }

func (e RefusalError) Error() string { return e.Text }

// Files is a Director copy's config.toml and its managed.toml, read once so that every check judges the same
// bytes; Managed is nil when the copy has no managed.toml.
type Files struct {
	CfgPath string
	Local   []byte
	Managed []byte
}

// Dir is the copy's directory, the one holding its config.
func (f Files) Dir() string { return filepath.Dir(f.CfgPath) }

// ReadFiles reads the config at cfgPath and the managed.toml beside it. An unreadable config is its read error,
// as config.Load returns it, and so is a managed.toml that exists but cannot be read.
func ReadFiles(cfgPath string) (Files, error) {
	f := Files{CfgPath: cfgPath}
	var err error
	if f.Local, err = os.ReadFile(cfgPath); err != nil {
		return f, err
	}
	if f.Managed, err = os.ReadFile(filepath.Join(f.Dir(), ManagedFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return f, err
	}
	return f, nil
}

// Load is config.Load of the files read: the combined config with a managed.toml, and the config alone without.
func (f Files) Load() (*config.Config, error) {
	if f.Managed == nil {
		return config.Parse(f.Local)
	}
	return config.ParseCombined(f.Local, f.Managed)
}

// ManagedView reads the managed facts of the Director copy whose config is at cfgPath, from the director.store
// beside it: none when that store does not exist, as for a config that is not in a copy.
func ManagedView(cfgPath string) (store.ManagedView, error) {
	path := filepath.Join(filepath.Dir(cfgPath), StoreFile)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return store.ManagedView{Revisions: []store.ManagedRevision{}}, nil
	}
	s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true, LockTimeout: config.DefaultLockWait})
	if err != nil {
		return store.ManagedView{}, err
	}
	v, err := s.ManagedView()
	return v, errors.Join(err, s.Close())
}

// Judge applies the boundary to files f and the copy's managed facts v: the inline [managed] refusal in every mode,
// the mode table, and, for a managed copy, revision trust. It loads nothing; a config that does not load is the
// caller's to report.
func Judge(f Files, v store.ManagedView) Admission {
	a := Admission{Enrolment: v.Enrolment, LastRevision: v.Latest()}
	if f.Managed != nil {
		a.ManagedDigest = string(digest.Of(f.Managed))
	}
	refuse := func(format string, args ...any) {
		if a.Refusal == "" {
			a.Refusal = fmt.Sprintf(format, args...)
		}
	}
	if inline, err := config.InlineManaged(f.Local); err == nil && inline {
		a.inline = true
		refuse("%s has an inline [managed] section, which a Director copy refuses in every mode: move those settings to %s in the checksum-bearing format (examples/managed.toml)", f.CfgPath, ManagedFile)
	}
	enrolled := v.Enrolment != nil && v.Enrolment.Event == store.Enrolled
	present := f.Managed != nil
	switch {
	case enrolled && present:
		a.Mode, a.Steward = ModeManaged, v.Enrolment.Steward
		if auth := v.Authorization(a.ManagedDigest); auth != nil {
			a.Authorized = true
		} else {
			refuse("%s %s is not authorized: this copy's store records no reload or Guildmaster approval of that digest, so its settings are not applied", ManagedFile, a.ManagedDigest)
		}
	case enrolled:
		a.Mode, a.Steward = ModeRefused, v.Enrolment.Steward
		refuse("this copy is enrolled with steward %q but %s is missing: the managed settings are missing; restore the %s the Steward published", v.Enrolment.Steward, ManagedFile, ManagedFile)
	case !present:
		a.Mode = ModeStandalone
	case v.Enrolment == nil:
		a.Mode = ModeRefused
		refuse("%s is present but this copy is not enrolled: run guildhall director enrol --steward <label>, or remove %s", ManagedFile, ManagedFile)
	default:
		a.Mode = ModeRefused
		refuse("this copy left managed mode (keeping %s) but a %s is present: a stale or unexpected managed file; remove it, or run guildhall director enrol --steward <label>", v.Enrolment.Revision, ManagedFile)
	}
	return a
}

// Inspect is the boundary's report form, for commands that report and never refuse: the admission of the Director
// copy whose config is at cfgPath, and its config as config.Load gives it, with its load error.
func Inspect(cfgPath string) (Admission, *config.Config, error) {
	f, err := ReadFiles(cfgPath)
	if err != nil {
		return Admission{}, nil, err
	}
	v, err := ManagedView(cfgPath)
	if err != nil {
		return Admission{}, nil, err
	}
	cfg, err := f.Load()
	return Judge(f, v), cfg, err
}

// Reported is the config of the Director copy whose config is at cfgPath, through the boundary's report form, for
// commands that only read and show it: config.Load's result, whatever the copy's mode or revision trust, so it reads
// only the files and never the store. Nothing that acts on settings may use it; Applied is for those.
func Reported(cfgPath string) (*config.Config, error) {
	f, err := ReadFiles(cfgPath)
	if err != nil {
		return nil, err
	}
	return f.Load()
}

// Admit is the boundary's refusing form, for every Director command and launch that needs effective settings: it
// refuses inline [managed] in every mode, loads and validates the files, applies the mode table and refuses a
// managed revision the copy's store has not authorized, and otherwise returns the effective config with the
// admission. A refusal is a RefusalError naming the state and the command that fixes it; a config that does not
// read or load is its error, as config.Load returns it.
func Admit(cfgPath string) (*config.Config, Admission, error) {
	f, err := ReadFiles(cfgPath)
	if err != nil {
		return nil, Admission{}, err
	}
	v, err := ManagedView(cfgPath)
	if err != nil {
		return nil, Admission{}, fmt.Errorf("managed mode: %w", err)
	}
	a := Judge(f, v)
	if a.inline || a.Mode == ModeRefused {
		return nil, a, RefusalError{a.Refusal}
	}
	cfg, err := f.Load()
	if err != nil {
		return nil, a, err
	}
	if a.Refusal != "" {
		return nil, a, RefusalError{a.Refusal}
	}
	return cfg, a, nil
}

// Applied is the config a command that acts but never refuses applies to the Director copy whose config is at
// cfgPath: the admitted effective config when the boundary admits the copy, and otherwise config.toml alone with no
// managed settings at all (standalone defaults), so a managed.toml or inline [managed] the copy has not authorized
// is never applied. refusal is the boundary's refusal in that case, and empty when the copy was admitted.
func Applied(cfgPath string) (cfg *config.Config, refusal string, err error) {
	cfg, a, err := Admit(cfgPath)
	var refused RefusalError
	if !errors.As(err, &refused) {
		return cfg, "", err
	}
	f, err := ReadFiles(cfgPath)
	if err != nil {
		return nil, a.Refusal, err
	}
	if cfg, err = config.Parse(f.Local); err != nil {
		return nil, a.Refusal, err
	}
	cfg.Managed = nil
	return cfg, a.Refusal, nil
}
