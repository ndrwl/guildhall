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
	"time"

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
	// Authorization is the revision event that authorizes ManagedDigest, when Authorized: its source and time say
	// when the digest on disk was applied.
	Authorization *store.ManagedRevision `json:"authorization,omitempty"`
	// Untrusted is the observed-untrusted state of the unauthorized ManagedDigest, once the store records it
	// observed: the alert decision that answers it and whether it waits or was rejected.
	Untrusted *Alert `json:"untrusted,omitempty"`
	// inline is set when config.toml defines an inline [managed] table, and approved when the Guildmaster approved
	// the observed digest's alert decision but its approval is not recorded as a revision yet.
	inline, approved bool
	// inlineErr is the error that left inline undecided: config.toml does not parse as TOML.
	inlineErr error
}

// Inline reports whether the copy's config.toml defines an inline [managed] table, which the boundary refuses in
// every mode, and the parse error that left it undecided.
func (a Admission) Inline() (bool, error) { return a.inline, a.inlineErr }

// mode is the mode table (docs/director-modes.md#mode): the mode of a copy with managed facts v and a managed.toml
// present or not, the Steward it is enrolled with, and, for mode refused, the refusal. A managed copy's revision
// trust and its pin are the caller's.
func mode(v store.ManagedView, present bool) (m, steward, refusal string) {
	enrolled := v.Enrolment != nil && v.Enrolment.Event == store.Enrolled
	switch {
	case enrolled && present:
		return ModeManaged, v.Enrolment.Steward, ""
	case enrolled:
		return ModeRefused, v.Enrolment.Steward, fmt.Sprintf("this copy is enrolled with steward %q but %s is missing: the managed settings are missing; restore the %s the Steward published", v.Enrolment.Steward, ManagedFile, ManagedFile)
	case !present:
		return ModeStandalone, "", ""
	case v.Enrolment == nil:
		return ModeRefused, "", fmt.Sprintf("%s is present but this copy is not enrolled: run guildhall director enrol --steward <label>, or remove %s", ManagedFile, ManagedFile)
	default:
		return ModeRefused, "", fmt.Sprintf("this copy left managed mode (keeping %s) but a %s is present: a stale or unexpected managed file; remove it, or run guildhall director enrol --steward <label>", v.Enrolment.Revision, ManagedFile)
	}
}

// Managed is the boundary's one managed verdict for the Director copy in dir, for commands that must refuse to act
// on a managed copy: whether its mode is not standalone, from its managed.toml and the director.store beside it. It
// reads no config.toml, loads no effective configuration and never writes. A copy the mode table refuses is
// managed, with a RefusalError carrying the refusal; its pin never changes the verdict. A store or managed.toml
// that cannot be read is its error, and the verdict stays what the file's presence alone says: a managed.toml that
// exists but cannot be read, or one beside a store that cannot be read, is still managed.
func Managed(dir string) (bool, error) {
	b, err := ReadManaged(dir)
	if err != nil {
		// Any error other than the file not existing means it exists.
		return true, err
	}
	cfgPath := filepath.Join(dir, "config.toml")
	v, _, err := managedFacts(cfgPath, fileLockWait(cfgPath))
	if err != nil {
		return b != nil, fmt.Errorf("managed mode: %w", err)
	}
	m, _, refusal := mode(v, b != nil)
	if m == ModeRefused {
		return true, RefusalError{refusal}
	}
	return m != ModeStandalone, nil
}

// ReadManaged reads the managed.toml in the copy directory dir: nil when it does not exist, and empty, not nil,
// when it exists but is empty; any other read error is returned.
func ReadManaged(dir string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManagedFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err == nil && b == nil {
		b = []byte{}
	}
	return b, err
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
// as config.LoadUnadmitted returns it, and so is a managed.toml that exists but cannot be read.
func ReadFiles(cfgPath string) (Files, error) {
	f := Files{CfgPath: cfgPath}
	var err error
	if f.Local, err = os.ReadFile(cfgPath); err != nil {
		return f, err
	}
	f.Managed, err = ReadManaged(f.Dir())
	return f, err
}

// Load is config.LoadUnadmitted of the files read: the combined config with a managed.toml, and the config alone
// without.
func (f Files) Load() (*config.Config, error) {
	if f.Managed == nil {
		return config.Parse(f.Local)
	}
	return config.ParseCombined(f.Local, f.Managed)
}

// ManagedView reads the managed facts of the Director copy whose config is at cfgPath, from the director.store
// beside it: none when that store does not exist, as for a config that is not in a copy.
func ManagedView(cfgPath string) (store.ManagedView, error) {
	v, _, err := managedFacts(cfgPath, fileLockWait(cfgPath))
	return v, err
}

// localLockWait is the store lock wait for admission's own store reads: the lock_wait that the config.toml bytes
// local set, or config.DefaultLockWait when they are empty, do not parse or give no positive wait. It reads
// config.toml alone on purpose: admission has not yet trusted managed.toml, and an unauthorized lock_wait never
// decides anything (docs/director-modes.md#mode), so a lock_wait set only in managed.toml governs every later store
// open but not the admission read. The full config's errors are still reported by the later Files.Load.
func localLockWait(local []byte) time.Duration {
	if len(local) == 0 {
		return config.DefaultLockWait
	}
	cfg, err := config.Parse(local)
	if err != nil {
		return config.DefaultLockWait
	}
	d, err := cfg.LockWait()
	if err != nil || d <= 0 {
		return config.DefaultLockWait
	}
	return d
}

// fileLockWait is localLockWait of the config.toml at cfgPath; a missing or unreadable file gives the default, so
// callers that only have a path keep their error behaviour.
func fileLockWait(cfgPath string) time.Duration {
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return config.DefaultLockWait
	}
	return localLockWait(b)
}

// managedFacts is ManagedView with the reserved task's alert decisions; it waits up to wait for the store's lock.
func managedFacts(cfgPath string, wait time.Duration) (store.ManagedView, []store.Decision, error) {
	path := filepath.Join(filepath.Dir(cfgPath), StoreFile)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return store.ManagedView{Revisions: []store.ManagedRevision{}}, nil, nil
	}
	s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true, LockTimeout: wait})
	if err != nil {
		return store.ManagedView{}, nil, err
	}
	v, err := s.ManagedView()
	var ds []store.Decision
	if err == nil {
		ds, err = alertDecisions(s)
	}
	return v, ds, errors.Join(err, s.Close())
}

// JudgeAlerts is Judge with the reserved task's alert decisions, which it uses to say whether an observed-untrusted
// digest waits for the Guildmaster or was rejected (Admission.Untrusted).
func JudgeAlerts(f Files, v store.ManagedView, decisions []store.Decision) Admission {
	a := Judge(f, v)
	a.alerted(v, decisions)
	return a
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
	inline, err := config.InlineManaged(f.Local)
	a.inlineErr = err
	if err == nil && inline {
		a.inline = true
		refuse("%s has an inline [managed] section, which a Director copy refuses in every mode: move those settings to %s in the checksum-bearing format (examples/managed.toml)", f.CfgPath, ManagedFile)
	}
	var refusal string
	a.Mode, a.Steward, refusal = mode(v, f.Managed != nil)
	if refusal != "" {
		refuse("%s", refusal)
	} else if a.Mode == ModeManaged {
		if auth := v.Authorization(a.ManagedDigest); auth != nil {
			a.Authorized, a.Authorization = true, auth
		} else {
			refuse("%s %s is not authorized: this copy's store records no reload or Guildmaster approval of that digest, so its settings are not applied", ManagedFile, a.ManagedDigest)
		}
	}
	if pin := PinRefusal(f.Dir(), v.ReleasePin); pin != "" {
		refuse("%s", pin)
	}
	return a
}

// Inspect is the boundary's report form, for commands that report and never refuse: the admission of the Director
// copy whose config is at cfgPath, and its config as config.LoadUnadmitted gives it, with its load error.
func Inspect(cfgPath string) (Admission, *config.Config, error) {
	f, err := ReadFiles(cfgPath)
	if err != nil {
		return Admission{}, nil, err
	}
	v, ds, err := managedFacts(cfgPath, localLockWait(f.Local))
	if err != nil {
		return Admission{}, nil, err
	}
	cfg, err := f.Load()
	return JudgeAlerts(f, v, ds), cfg, err
}

// Reported is the config of the Director copy whose config is at cfgPath, through the boundary's report form, for
// commands that only read and show it: config.LoadUnadmitted's result, whatever the copy's mode or revision trust,
// so it reads only the files and never the store. Nothing that acts on settings may use it; Applied is for those.
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
// read or load is its error, as config.LoadUnadmitted returns it.
//
// On first load of a managed digest the store has not authorized, Admit records it observed-untrusted, once per
// digest and enrolment, and opens one approve/reject alert decision for the Guildmaster on the reserved task
// (docs/director-modes.md#guard-rails); the revision is still refused. An alert the Guildmaster answered approve
// is recorded as a guildmaster-approved revision instead, which admits the digest. A failure to record leaves the
// refusal standing, saying so.
func Admit(cfgPath string) (*config.Config, Admission, error) {
	f, err := ReadFiles(cfgPath)
	if err != nil {
		return nil, Admission{}, err
	}
	wait := localLockWait(f.Local)
	v, ds, err := managedFacts(cfgPath, wait)
	if err != nil {
		return nil, Admission{}, fmt.Errorf("managed mode: %w", err)
	}
	a := JudgeAlerts(f, v, ds)
	if a.inline || a.Mode == ModeRefused {
		return nil, a, RefusalError{a.Refusal}
	}
	cfg, err := f.Load()
	if err != nil {
		return nil, a, err
	}
	if a.Mode == ModeManaged && !a.Authorized && (a.Untrusted == nil || a.Untrusted.State == AlertNotOpened || a.approved) {
		v, ds, err := observe(cfgPath, a, wait)
		if err != nil {
			return nil, a, RefusalError{a.Refusal + "; recording the observation failed: " + err.Error()}
		}
		a = JudgeAlerts(f, v, ds)
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
