package cli

import (
	"fmt"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
)

// configCmd runs `guildhall config <command>`: only reload.
func (a *App) configCmd(args []string) int {
	if len(args) == 0 || args[0] != "reload" {
		return a.usageError("config: the command is reload")
	}
	return a.configReload(args[1:])
}

// configReload runs `guildhall config reload [--copy <dir>]`, which a Steward rollout runs over SSH on each copy it
// wrote and the Guildmaster may run by hand (docs/director-modes.md#how-the-steward-acts): it loads and validates
// the managed copy's config.toml and managed.toml together, checks the copy's mode, and records a managed-revision
// event with source reload authorizing that exact managed.toml digest in the copy's own store, with the digest it
// authorized before. Reloading a digest already authorized by reload replays its event, so it is idempotent per
// digest. It refuses, recording nothing, an inline [managed], a copy that is not managed, a config that does not
// load or validate, and a config that is not a director's. It changes no task: active tasks keep their bound
// config, and the new revision reaches new tasks only.
func (a *App) configReload(args []string) int {
	const name = "config reload"
	_, cfgPath, storePath, code := a.modeCopy(name, args, nil)
	if code >= 0 {
		return code
	}
	fail := func(format string, args ...any) int { return a.fail(fmt.Errorf(name+": "+format, args...)) }
	f, err := readCopyFiles(cfgPath)
	if err != nil {
		return fail("%w", err)
	}
	v, err := withStore(storePath, true, func(s *store.Store) (store.ManagedView, error) { return s.ManagedView() })
	if err != nil {
		return fail("%w", err)
	}
	adm := judge(f, v)
	switch inline, err := config.InlineManaged(f.Local); {
	case err != nil:
		return fail("%w", err)
	case inline:
		return fail("%s", adm.Refusal)
	case adm.Mode == modeStandalone:
		return fail("this copy is standalone, so it has no managed revision to authorize; a copy is managed only once the Guildmaster runs guildhall director enrol --steward <label> on it")
	case adm.Mode != modeManaged:
		return fail("this copy is %s: %s", adm.Mode, adm.Refusal)
	}
	cfg, err := f.Load()
	if err != nil {
		return fail("%s %s does not load with %s, so it is not authorized: %w", managedFile, adm.ManagedDigest, cfgPath, err)
	}
	if cfg.Mode != config.ModeDirector {
		return fail("%s is a %s config, not a director's", cfgPath, cfg.Mode)
	}
	// The previous digest is the one applied before: the newest this enrolment authorized, unless it is this one.
	// A replay returns the event already recorded, with the previous digest it named.
	previous := ""
	for _, r := range v.Revisions {
		if r.Source.Authorizes() {
			previous = r.Digest
		}
	}
	if previous == adm.ManagedDigest {
		previous = ""
	}
	got, err := withStoreClock(storePath, false, a.Now, func(s *store.Store) (store.ManagedRevision, error) {
		r, _, err := s.RecordManagedRevision(store.ManagedRevision{Source: store.SourceReload, Digest: adm.ManagedDigest, Previous: previous})
		return r, err
	})
	if err != nil {
		return fail("%w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s authorized by reload (event %d", managedFile, got.Digest, got.Sequence)
	if got.Previous != "" {
		fmt.Fprintf(&b, ", previous %s", got.Previous)
	}
	fmt.Fprintf(&b, "); new tasks get its settings, and active tasks keep their bound config\n")
	if _, err := fmt.Fprint(a.Stdout, b.String()); err != nil {
		return 1
	}
	return 0
}
