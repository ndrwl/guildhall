package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
)

// modeCopy selects the Director copy a mode command acts on, --copy or copy discovery, and returns its directory,
// config and store paths; code is non-negative when the command is done, having printed a usage error.
func (a *App) modeCopy(name string, args []string, register func(*flag.FlagSet)) (dir, cfgPath, storePath string, code int) {
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	if register != nil {
		register(fl)
	}
	if err := parseInterspersed(fl, args); err != nil {
		return "", "", "", a.usageError(name + ": " + err.Error())
	}
	switch err := cp.apply(a, nil, &cfgPath, nil); {
	case err != nil:
		return "", "", "", a.usageError(name + ": " + err.Error())
	case fl.NArg() > 0:
		return "", "", "", a.usageError(fmt.Sprintf("%s: unexpected argument %q", name, fl.Arg(0)))
	case cp.dir == "":
		return "", "", "", a.usageError(name + ": " + cp.missing(a).Error())
	}
	storePath = filepath.Join(cp.dir, directorStoreFile)
	if copyStoreFile(cp.dir) == stewardStoreFile {
		return "", "", "", a.fail(fmt.Errorf("%s: %s is a Steward copy, which is never managed", name, cp.dir))
	}
	if _, err := os.Stat(storePath); errors.Is(err, fs.ErrNotExist) {
		return "", "", "", a.fail(fmt.Errorf("%s: %w", name, noStore(storePath)))
	}
	return cp.dir, cfgPath, storePath, -1
}

// directorMode runs `guildhall director mode [--json] [--copy <dir>]`: the copy's mode as the configuration-admission
// boundary finds it, with any refusal, the Steward's label, the managed digest on disk and whether the store
// authorizes it, the newest enrolment event and the newest revision event (docs/director-modes.md). It reports and
// never refuses: a refused copy exits 0 with its refusal.
func (a *App) directorMode(args []string) int {
	const name = "director mode"
	var asJSON bool
	_, cfgPath, _, code := a.modeCopy(name, args, func(fl *flag.FlagSet) { fl.BoolVar(&asJSON, "json", false, "print JSON") })
	if code >= 0 {
		return code
	}
	adm, _, err := inspectDirector(cfgPath)
	if err != nil && adm.Mode == "" {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if adm.Refusal == "" && err != nil {
		adm.Refusal = "the config does not load: " + err.Error()
	}
	if asJSON {
		return a.printJSON(adm)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "mode: %s\n", adm.Mode)
	if adm.Steward != "" {
		fmt.Fprintf(&b, "steward: %s\n", adm.Steward)
	}
	if adm.ManagedDigest != "" {
		auth := "not authorized"
		if adm.Authorized {
			auth = "authorized"
		}
		fmt.Fprintf(&b, "managed digest: %s (%s)\n", adm.ManagedDigest, auth)
	}
	if e := adm.Enrolment; e != nil {
		what := "enrolled with steward " + e.Steward
		if e.Event == store.Left {
			what = "left, keeping " + e.Revision
		}
		fmt.Fprintf(&b, "enrolment: %s (event %d, %s)\n", what, e.Sequence, e.Time.Format(time.RFC3339))
	}
	if r := adm.LastRevision; r != nil {
		fmt.Fprintf(&b, "last revision: %s %s (event %d, %s)\n", r.Source, r.Digest, r.Sequence, r.Time.Format(time.RFC3339))
	}
	if adm.Refusal != "" {
		fmt.Fprintf(&b, "refused: %s\n", adm.Refusal)
	}
	if _, err := io.WriteString(a.Stdout, b.String()); err != nil {
		return 1
	}
	return 0
}

// directorEnrol runs `guildhall director enrol --steward <label> [--copy <dir>]`: it records that the copy is
// managed by the Steward with that label, with the digest of the valid managed.toml beside its config
// (docs/director-modes.md#enrol-and-leave). It accepts only a copy that is not enrolled, or has left, and has a
// managed.toml; enrolling the same copy with the same Steward and digest again replays the event. Enrolment never
// authorizes the digest, and it changes no task.
func (a *App) directorEnrol(args []string) int {
	const name = "director enrol"
	var steward string
	_, cfgPath, storePath, code := a.modeCopy(name, args, func(fl *flag.FlagSet) {
		fl.StringVar(&steward, "steward", "", "the label of the Steward that manages this copy")
	})
	if code >= 0 {
		return code
	}
	if steward == "" {
		return a.usageError(name + ": --steward <label> is required")
	}
	f, err := readCopyFiles(cfgPath)
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	switch inline, err := config.InlineManaged(f.Local); {
	case err != nil:
		return a.fail(fmt.Errorf("%s: %w", name, err))
	case inline:
		return a.fail(fmt.Errorf("%s: %s", name, judge(f, store.ManagedView{}).Refusal))
	case f.Managed == nil:
		return a.fail(fmt.Errorf("%s: enrolment needs a valid %s beside %s, in the checksum-bearing format (examples/managed.toml)", name, managedFile, cfgPath))
	}
	cfg, err := f.Load()
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if cfg.Mode != config.ModeDirector {
		return a.fail(fmt.Errorf("%s: %s is a %s config, not a director's", name, cfgPath, cfg.Mode))
	}
	e := store.Enrolment{Event: store.Enrolled, Steward: steward, Digest: string(digest.Of(f.Managed))}
	got, err := withStoreClock(storePath, false, a.Now, func(s *store.Store) (store.Enrolment, error) {
		got, _, err := s.RecordEnrolment(e)
		return got, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	_, err = fmt.Fprintf(a.Stdout, "enrolled with steward %s (event %d); %s %s is not authorized by enrolment, so commands that need effective settings refuse until it is authorized\n",
		got.Steward, got.Sequence, managedFile, got.Digest)
	if err != nil {
		return 1
	}
	return 0
}

// directorLeave runs `guildhall director leave [--copy <dir>]`, on the Guildmaster's word only: it records that the
// copy left managed mode, writes the effective values of the managed keys into [local] of config.toml so no guard
// rail loosens, and renames managed.toml to managed.toml.rev-<first 16 hex digits of its sha256>
// (docs/director-modes.md#enrol-and-leave). It is the only command that writes config.toml: a targeted line edit of
// [local], written atomically after the result is re-parsed and shown to keep every effective setting, and refused,
// writing nothing, when [local] cannot be edited safely. A managed copy must be admitted, so an unauthorized revision
// refuses it. The left event is recorded before the files change, so leave run again after an interruption
// finishes the edit and the rename, and leave run on a copy that has left prints the event again. It changes no
// task.
func (a *App) directorLeave(args []string) int {
	const name = "director leave"
	dir, cfgPath, storePath, code := a.modeCopy(name, args, nil)
	if code >= 0 {
		return code
	}
	fail := func(err error) int { return a.fail(fmt.Errorf("%s: %w", name, err)) }
	v, err := withStore(storePath, true, func(s *store.Store) (store.ManagedView, error) { return s.ManagedView() })
	if err != nil {
		return fail(err)
	}
	f, err := readCopyFiles(cfgPath)
	if err != nil {
		return fail(err)
	}
	resume := false
	switch e := v.Enrolment; {
	case e == nil:
		return fail(errors.New("this copy is not enrolled, so it has nothing to leave"))
	case e.Event == store.Left && f.Managed == nil:
		return a.printLeft(*e)
	case e.Event == store.Left && string(digest.Of(f.Managed)) != e.Digest:
		return fail(errors.New(judge(f, v).Refusal))
	case e.Event == store.Left:
		resume = true
	default:
		if _, err := admitDirector(cfgPath); err != nil {
			return fail(err)
		}
	}
	managed, err := config.DecodeManaged(f.Managed)
	if err != nil {
		return fail(err)
	}
	edited, err := leaveEdit(f.Local, managed)
	if err != nil {
		return fail(fmt.Errorf("%s left unchanged: %w", cfgPath, err))
	}
	d := digest.Of(f.Managed)
	left := store.Enrolment{Event: store.Left, Digest: string(d), Revision: store.RevisionName(d)}
	if !resume {
		if left, err = withStoreClock(storePath, false, a.Now, func(s *store.Store) (store.Enrolment, error) {
			got, _, err := s.RecordEnrolment(left)
			return got, err
		}); err != nil {
			return fail(err)
		}
	} else {
		left = *v.Enrolment
	}
	if string(edited) != string(f.Local) {
		if err := atomicfile.Write(cfgPath, edited); err != nil {
			return fail(fmt.Errorf("the left event is recorded; run leave again to finish: %w", err))
		}
	}
	if err := os.Rename(filepath.Join(dir, managedFile), filepath.Join(dir, left.Revision)); err != nil {
		return fail(fmt.Errorf("the left event is recorded; run leave again to finish: %w", err))
	}
	return a.printLeft(left)
}

func (a *App) printLeft(e store.Enrolment) int {
	if _, err := fmt.Fprintf(a.Stdout, "left managed mode (event %d); %s kept as %s; the copy is standalone\n", e.Sequence, managedFile, e.Revision); err != nil {
		return 1
	}
	return 0
}

// leaveEdit is config.toml text local with every managed key's effective value written into [local]: the local
// value of an overridable key that has one, else the managed value. It re-parses the result alone, as the copy will
// load it once managed.toml is gone, and refuses one that gives any setting a different effective value or changes
// anything but [local].
func leaveEdit(local []byte, managed map[string]config.ManagedSetting) ([]byte, error) {
	before, err := config.Parse(local)
	if err != nil {
		return nil, err
	}
	before.Managed = managed
	values := map[string]string{}
	for k := range managed {
		values[k], _ = before.Setting(k)
	}
	edited, err := config.SetLocal(local, values)
	if err != nil {
		return nil, err
	}
	after, err := config.Parse(edited)
	if err != nil {
		return nil, fmt.Errorf("the edited config does not parse: %w", err)
	}
	keys := slices.Sorted(maps.Keys(managed))
	for k := range before.Local {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		want, wok := before.Setting(k)
		got, gok := after.Setting(k)
		if want != got || wok != gok {
			return nil, fmt.Errorf("the edited config gives %s %q, not its effective %q", k, got, want)
		}
	}
	rest := func(c *config.Config) string {
		q := *c
		q.Managed, q.Local = nil, nil
		b, err := json.Marshal(&q)
		if err != nil {
			return err.Error()
		}
		return string(b)
	}
	if rest(before) != rest(after) {
		return nil, errors.New("the edit changed more than [local]")
	}
	return edited, nil
}
