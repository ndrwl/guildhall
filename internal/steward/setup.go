package steward

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/strictjson"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Setup plan and step log schemas (docs/steward.md#takeover). A setup plan is its own kind beside the rollout plan,
// so rollout plans and their digests do not change.
const (
	SetupPlanSchema = setupPlanSchemaPrefix + "1"
	StepLogSchema   = stepLogSchemaPrefix + "1"

	setupPlanSchemaPrefix  = "guildhall.setup-plan/"
	setupPlanSchemaVersion = 1
	stepLogSchemaPrefix    = "guildhall.setup-steplog/"
	stepLogSchemaVersion   = 1
)

// SetupKindTakeover is a setup plan that adopts an existing Director copy into the roster as managed, keeping its
// store. Provisioning a new copy is a later slice of guildhall/d128.
const SetupKindTakeover = "takeover"

// The steps of a takeover, in the order they run.
const (
	StepPause   = "pause"
	StepDrain   = "drain"
	StepBackup  = "backup"
	StepManaged = "managed"
	StepEnrol   = "enrol"
	StepReload  = "reload"
	StepResume  = "resume"
	StepRoster  = "roster"
)

// What a step runs: a fixed remote command over ssh, an scp copy to the machine, or a commit to the private config.
const (
	StepKindRemote = "remote"
	StepKindCopy   = "copy"
	StepKindRoster = "roster"
)

// The setup plan files written beside plan.json.
const (
	setupPlanFile   = "plan.json"
	setupStepLog    = "steps.json"
	setupManagedDir = "files"
	// takeoverExportDir is where, under a copy without a [backup] table, a takeover writes its store export, one
	// directory per plan, named as the plan directory.
	takeoverExportDir = "state/steward/takeover"
)

var (
	// ErrPlanChanged reports a machine, copy, roster or planned file that changed since the approved dry run; no step
	// ran.
	ErrPlanChanged = errors.New("the machine, the copy, the roster or a planned file changed since the approved plan; run the dry run again")
	// ErrNotApproved reports a setup plan applied without --approve and its exact digest; no step ran.
	ErrNotApproved = errors.New("a setup plan runs only with --approve <its exact digest>, once the Guildmaster approved it")
	// ErrRosterConflict reports a roster that already holds the Director or its copy directory, or cannot take the
	// entry as planned.
	ErrRosterConflict = errors.New("the roster cannot take this Director")
	// ErrCopyMismatch reports a copy a takeover cannot adopt: not a standalone director copy with this name whose
	// repositories the machine owns.
	ErrCopyMismatch = errors.New("the copy cannot be taken over as planned")
	// ErrAbandoned reports a plan abandoned with --abandon, which never runs again.
	ErrAbandoned = errors.New("the plan was abandoned; make a new dry run to take the copy over")
)

// ErrStepFailed is a step that failed, with the diagnosis the Steward gives the Guildmaster.
type ErrStepFailed struct {
	Step, Diagnosis string
}

func (e *ErrStepFailed) Error() string { return "step " + e.Step + " failed: " + e.Diagnosis }

// TakeoverInput names the copy a takeover adopts: the roster machine label it lives on, the Director's name, its
// copy directory there and, for a machine not yet in the roster, the SSH alias that reaches it. PlanDir is the new
// local directory the plan is written to.
type TakeoverInput struct {
	Machine  string
	Director string
	CopyDir  string
	SSHAlias string
	PlanDir  string
}

// SetupPlan is a dry-run setup plan, approved by its digest before it runs.
type SetupPlan struct {
	Schema   string `json:"schema"`
	Digest   string `json:"digest"`
	Kind     string `json:"kind"`
	Machine  string `json:"machine"`
	Director string `json:"director"`
	CopyDir  string `json:"copy_dir"`
	SSHAlias string `json:"ssh_alias"`
	// Guildhall is the program the remote commands run, and Steward the label the copy is enrolled with.
	Guildhall string `json:"guildhall"`
	Steward   string `json:"steward"`
	PlanDir   string `json:"plan_dir"`
	// RosterDigest is the sha256 of roster.toml at the private config's HEAD the plan was made from, and RosterDiff
	// the change the roster step makes to it.
	RosterDigest string      `json:"roster_digest"`
	Roster       RosterEntry `json:"roster"`
	RosterDiff   string      `json:"roster_diff"`
	Probe        CopyProbe   `json:"probe"`
	// Files are the files the plan copies to the machine, kept under the plan directory.
	Files []PlannedFile `json:"files"`
	Steps []SetupStep   `json:"steps"`

	// session is the copy's session state the dry run saw, shown but not bound: it moves on its own.
	session string
	files   map[string][]byte
	input   TakeoverInput
}

// RosterEntry is the roster entry a takeover adds: the Director under its machine, and the machine itself when
// NewMachine is set.
type RosterEntry struct {
	Machine      string   `json:"machine"`
	NewMachine   bool     `json:"new_machine"`
	SSHAlias     string   `json:"ssh_alias"`
	OS           OS       `json:"os"`
	Director     string   `json:"director"`
	CopyDir      string   `json:"copy_dir"`
	Repositories []string `json:"repositories"`
}

// CopyProbe is what the dry run read on the copy, which the apply probes again.
type CopyProbe struct {
	// ConfigSHA256 is the copy's config.toml, which no step changes.
	ConfigSHA256 string `json:"config_sha256"`
	// Mode is the copy's mode from director mode --json, always standalone for a takeover.
	Mode string `json:"mode"`
	// Backup reports whether the config has a [backup] table, which decides the backup step.
	Backup bool `json:"backup"`
}

// PlannedFile is a file the plan copies to the machine: Name under the plan directory, Target on the machine.
type PlannedFile struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	SHA256 string `json:"sha256"`
}

// SetupStep is one step of a setup plan. Argv is the fixed remote command a remote step runs, or the scp command a
// copy step runs; Expect says what the step checks before it acts and verifies after.
type SetupStep struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Argv   []string `json:"argv,omitempty"`
	Expect string   `json:"expect"`
}

// Session is the copy's session state as the dry run saw it.
func (p *SetupPlan) Session() string { return p.session }

// Setup plans and applies setup plans from the Steward's private config checkout, reaching machines with the fixed
// remote commands and scp (docs/steward.md#takeover).
type Setup struct {
	// Private is the private config checkout; its roster.toml at HEAD is the roster.
	Private   git.WorkTree
	Transport Copier
	Runner    Runner
	// Steward is the label copies are enrolled with: the Steward config's [steward] name.
	Steward string
	Now     func() time.Time
}

func (s *Setup) signature() git.Signature {
	return git.Signature{Name: "Guildhall Steward", Email: "steward@guildhall.invalid", When: s.Now().UTC()}
}

// headRoster reads roster.toml at the private config's HEAD.
func (s *Setup) headRoster(ctx context.Context) ([]byte, *Roster, error) {
	data, ok, err := s.Private.Show(ctx, "HEAD", "roster.toml")
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, errors.New("the private config has no committed roster.toml")
	}
	r, err := ParseRoster(data)
	if err != nil {
		return nil, nil, err
	}
	return data, r, nil
}

// planNamePattern is a plan directory's base name, which names the takeover's export directory on the machine.
var planNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// PlanTakeover reads the copy in.CopyDir on in.Machine and plans its takeover, changing nothing: it fetches the
// copy's config.toml, reads its mode and session with the read-only remote commands, renders the first managed.toml,
// which has no managed key and so changes no effective setting, and proposes the roster entry. It refuses with
// ErrRosterConflict a Director or copy directory the roster already holds, and with ErrCopyMismatch a copy that is
// not a standalone director copy of that name whose repositories name the machine as their owner.
func (s *Setup) PlanTakeover(ctx context.Context, in TakeoverInput) (*SetupPlan, error) {
	switch {
	case !workflow.ValidID(in.Machine):
		return nil, fmt.Errorf("machine label %q must be a lowercase id", in.Machine)
	case !workflow.ValidID(in.Director):
		return nil, fmt.Errorf("director name %q must be a lowercase id", in.Director)
	case !workflow.ValidID(s.Steward):
		return nil, fmt.Errorf("the Steward's label %q, its [steward] name, must be a lowercase id", s.Steward)
	case !validDir(posixDirPattern, in.CopyDir):
		return nil, fmt.Errorf("copy directory %q must be an absolute Linux path of letters, digits, '.', '_', '-' and '/'", in.CopyDir)
	}
	planDir, err := filepath.Abs(in.PlanDir)
	if err != nil {
		return nil, err
	}
	if !planNamePattern.MatchString(filepath.Base(planDir)) {
		return nil, fmt.Errorf("plan directory name %q must be letters, digits, '.', '_' and '-', as it names the export directory on the machine", filepath.Base(planDir))
	}
	in.PlanDir = planDir
	rosterData, roster, err := s.headRoster(ctx)
	if err != nil {
		return nil, err
	}
	entry := RosterEntry{Machine: in.Machine, Director: in.Director, CopyDir: in.CopyDir}
	guildhall := DefaultGuildhall
	if m, ok := roster.Machine(in.Machine); ok {
		if in.SSHAlias != "" && in.SSHAlias != m.SSHAlias {
			return nil, fmt.Errorf("%w: machine %q is in the roster with ssh_alias %q, not %q", ErrRosterConflict, m.Label, m.SSHAlias, in.SSHAlias)
		}
		if !slices.Contains(m.Roles, RoleDirector) {
			return nil, fmt.Errorf("%w: machine %q lacks the director role; add it in roster.toml first", ErrRosterConflict, m.Label)
		}
		for _, d := range m.Directors {
			if d.Name == in.Director || d.CopyDir == in.CopyDir {
				return nil, fmt.Errorf("%w: machine %q already lists Director %q at %s", ErrRosterConflict, m.Label, d.Name, d.CopyDir)
			}
		}
		entry.SSHAlias, entry.OS, guildhall = m.SSHAlias, m.OS, m.Guildhall()
	} else {
		if in.SSHAlias == "" {
			return nil, fmt.Errorf("machine %q is not in the roster; name the SSH alias that reaches it with --ssh-alias", in.Machine)
		}
		if !aliasPattern.MatchString(in.SSHAlias) {
			return nil, fmt.Errorf("ssh alias %q must be a plain Host from your SSH config", in.SSHAlias)
		}
		for _, m := range roster.Machines {
			if m.SSHAlias == in.SSHAlias {
				return nil, fmt.Errorf("%w: ssh_alias %q is machine %q's", ErrRosterConflict, in.SSHAlias, m.Label)
			}
		}
		entry.NewMachine, entry.SSHAlias, entry.OS = true, in.SSHAlias, OSLinux
	}
	if entry.OS != OSLinux {
		return nil, fmt.Errorf("machine %q is %s: takeover is built for Linux machines only so far", in.Machine, entry.OS)
	}

	p := &SetupPlan{Schema: SetupPlanSchema, Kind: SetupKindTakeover, Machine: in.Machine, Director: in.Director, CopyDir: in.CopyDir,
		SSHAlias: entry.SSHAlias, Guildhall: guildhall, Steward: s.Steward, PlanDir: planDir,
		RosterDigest: digest.Of(rosterData).Hex(), input: in, files: map[string][]byte{}}
	local, err := s.fetch(ctx, Remote{entry.SSHAlias, in.CopyDir + "/config.toml"})
	if err != nil {
		return nil, fmt.Errorf("reading the copy's config.toml: %w", err)
	}
	p.Probe.ConfigSHA256 = digest.Of(local).Hex()
	if inline, err := config.InlineManaged(local); err != nil || inline {
		return nil, fmt.Errorf("%w: config.toml has inline [managed] settings or does not parse (%v); move them to managed.toml first", ErrCopyMismatch, err)
	}
	cfg, err := config.Parse(local)
	if err != nil {
		return nil, fmt.Errorf("%w: config.toml: %v", ErrCopyMismatch, err)
	}
	if cfg.Mode != config.ModeDirector {
		return nil, fmt.Errorf("%w: not a director copy (mode %q)", ErrCopyMismatch, cfg.Mode)
	}
	if cfg.Name != in.Director {
		return nil, fmt.Errorf("%w: the copy names Director %q, not %q", ErrCopyMismatch, cfg.Name, in.Director)
	}
	for _, r := range cfg.Repositories {
		if r.OwnerMachine != in.Machine {
			return nil, fmt.Errorf("%w: repository %q names owner machine %q, not %q; take it over under the label its repositories name", ErrCopyMismatch, r.ID, r.OwnerMachine, in.Machine)
		}
		entry.Repositories = append(entry.Repositories, r.ID)
	}
	if len(entry.Repositories) == 0 {
		return nil, fmt.Errorf("%w: the copy has no repository, and a roster Director needs one", ErrCopyMismatch)
	}
	p.Probe.Backup = cfg.Backup != nil
	mode, err := s.mode(ctx, p)
	if err != nil {
		return nil, err
	}
	if mode.Mode != "standalone" || mode.ManagedDigest != "" {
		return nil, fmt.Errorf("%w: the copy is %s (managed.toml %q, Steward %q); a takeover adopts a standalone copy with no managed.toml", ErrCopyMismatch, mode.Mode, mode.ManagedDigest, mode.Steward)
	}
	p.Probe.Mode = mode.Mode
	st, err := s.status(ctx, p)
	if err != nil {
		return nil, err
	}
	p.session = st.Session.State

	managed, err := config.EncodeManaged(map[string]config.ManagedSetting{})
	if err != nil {
		return nil, err
	}
	if _, err := config.ParseCombined(local, managed); err != nil {
		return nil, fmt.Errorf("%w: the first managed.toml does not load with config.toml: %v", ErrCopyMismatch, err)
	}
	managedName := setupManagedDir + "/managed.toml"
	p.files[managedName] = managed
	p.Files = []PlannedFile{{Name: managedName, Target: in.CopyDir + "/managed.toml", SHA256: digest.Of(managed).Hex()}}

	p.Roster = entry
	after, err := proposeRoster(rosterData, roster, entry)
	if err != nil {
		return nil, err
	}
	p.RosterDiff = lineDiff(string(rosterData), string(after))

	backup := BackupCommand(guildhall, in.CopyDir)
	backupExpect := "the copy's store is backed up to its [backup] branch"
	if !p.Probe.Backup {
		out := in.CopyDir + "/" + takeoverExportDir + "/" + filepath.Base(planDir)
		backup = ExportCommand(guildhall, out, in.CopyDir)
		backupExpect = "the copy has no [backup] table, so its store is exported to " + out
	}
	p.Steps = []SetupStep{
		{ID: StepPause, Kind: StepKindRemote, Argv: PauseCommand(guildhall, in.CopyDir), Expect: "a running session is paused, so it starts no new work; a session already paused, stopped or absent is left as it is"},
		{ID: StepDrain, Kind: StepKindRemote, Argv: StatusCommand(guildhall, in.CopyDir), Expect: "no attempt is running; while one is, the apply waits and is run again later"},
		{ID: StepBackup, Kind: StepKindRemote, Argv: backup, Expect: backupExpect},
		{ID: StepManaged, Kind: StepKindCopy, Argv: scpCommand(filepath.Join(planDir, filepath.FromSlash(managedName)), Remote{entry.SSHAlias, in.CopyDir + "/managed.toml"}.String()),
			Expect: "managed.toml, with no managed key, is published beside config.toml and read back with digest " + string(digest.Of(managed))},
		{ID: StepEnrol, Kind: StepKindRemote, Argv: EnrolCommand(guildhall, s.Steward, in.CopyDir), Expect: "the copy is enrolled with Steward " + s.Steward},
		{ID: StepReload, Kind: StepKindRemote, Argv: ReloadCommand(guildhall, in.CopyDir), Expect: "director mode reports the copy managed, with its managed.toml authorized"},
		{ID: StepResume, Kind: StepKindRemote, Argv: ResumeCommand(guildhall, in.CopyDir), Expect: "a session the takeover paused runs again"},
		{ID: StepRoster, Kind: StepKindRoster, Expect: "roster.toml holds the entry, committed alone to the private config and pushed"},
	}
	p.Digest = p.digest()
	return p, nil
}

func (p *SetupPlan) digest() string {
	q := *p
	q.Digest = ""
	data, err := json.Marshal(&q)
	if err != nil {
		panic(err) // A plan is plain data and always encodes.
	}
	return string(digest.Of(data))
}

// fetch copies a file from a machine and returns its bytes.
func (s *Setup) fetch(ctx context.Context, src Remote) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "guildhall-setup-")
	if err != nil {
		return nil, err
	}
	// The fetched file is scratch; failing to remove it changes no outcome.
	defer func() { _ = os.RemoveAll(tmp) }()
	local := filepath.Join(tmp, "fetched")
	if err := s.Transport.Fetch(ctx, src, local); err != nil {
		return nil, err
	}
	return os.ReadFile(local)
}

// remoteMode is what director mode --json prints, as a takeover reads it.
type remoteMode struct {
	Mode          string `json:"mode"`
	Steward       string `json:"steward"`
	ManagedDigest string `json:"managed_digest"`
	Authorized    bool   `json:"authorized"`
	Refusal       string `json:"refusal"`
}

// remoteStatus is what director status --json prints, as a takeover reads it.
type remoteStatus struct {
	Session struct {
		State string `json:"state"`
	} `json:"session"`
	Running []struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
		Step       string `json:"step"`
		Attempt    int    `json:"attempt"`
	} `json:"running"`
}

func (s *Setup) mode(ctx context.Context, p *SetupPlan) (remoteMode, error) {
	var m remoteMode
	out, err := s.Runner.Run(ctx, p.SSHAlias, ModeCommand(p.Guildhall, p.CopyDir))
	if err == nil {
		err = json.Unmarshal(out, &m)
	}
	if err == nil && m.Mode == "" {
		err = errors.New("it printed no mode")
	}
	if err != nil {
		return m, fmt.Errorf("reading the copy's mode with director mode --json: %w", err)
	}
	return m, nil
}

func (s *Setup) status(ctx context.Context, p *SetupPlan) (remoteStatus, error) {
	var st remoteStatus
	out, err := s.Runner.Run(ctx, p.SSHAlias, StatusCommand(p.Guildhall, p.CopyDir))
	if err == nil {
		err = json.Unmarshal(out, &st)
	}
	if err == nil && st.Session.State == "" {
		err = errors.New("it printed no session state")
	}
	if err != nil {
		return st, fmt.Errorf("reading the copy's status with director status --json: %w", err)
	}
	return st, nil
}

// WriteSetupPlan writes plan.json and the files it copies into the plan directory. A plan directory whose apply has
// started is refused; a dry run again into one that has not replaces its plan.
func WriteSetupPlan(p *SetupPlan) error {
	if _, err := os.Stat(filepath.Join(p.PlanDir, setupStepLog)); err == nil {
		return fmt.Errorf("%s holds a plan whose apply has started; use a new directory", p.PlanDir)
	}
	if data, err := os.ReadFile(filepath.Join(p.PlanDir, setupPlanFile)); err == nil && !bytes.Contains(data, []byte(`"`+setupPlanSchemaPrefix)) {
		return fmt.Errorf("%s already holds another kind of plan; use a new directory", p.PlanDir)
	}
	for _, f := range p.Files {
		path := filepath.Join(p.PlanDir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, p.files[f.Name], 0o600); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.PlanDir, setupPlanFile), append(data, '\n'), 0o600)
}

// ReadSetupPlan reads a plan written by WriteSetupPlan, decoded strictly.
func ReadSetupPlan(planDir string) (*SetupPlan, error) {
	data, err := os.ReadFile(filepath.Join(planDir, setupPlanFile))
	if err != nil {
		return nil, err
	}
	var p SetupPlan
	if err := strictjson.Decode(data, &p, strictjson.Options{Schema: setupPlanSchemaPrefix, Version: setupPlanSchemaVersion}); err != nil {
		return nil, fmt.Errorf("setup plan: %w", err)
	}
	if p.Kind != SetupKindTakeover {
		return nil, fmt.Errorf("setup plan: kind %q is not built yet", p.Kind)
	}
	p.input = TakeoverInput{Machine: p.Machine, Director: p.Director, CopyDir: p.CopyDir, PlanDir: p.PlanDir}
	if p.Roster.NewMachine {
		p.input.SSHAlias = p.SSHAlias
	}
	return &p, nil
}

// IsSetupPlan reports whether the plan.json in planDir is a setup plan rather than a rollout plan.
func IsSetupPlan(planDir string) bool {
	data, err := os.ReadFile(filepath.Join(planDir, setupPlanFile))
	if err != nil {
		return false
	}
	var head struct {
		Schema string `json:"schema"`
	}
	return json.Unmarshal(data, &head) == nil && strings.HasPrefix(head.Schema, setupPlanSchemaPrefix)
}

// machineHeader is a [[machine]] table header line in roster.toml.
var machineHeader = regexp.MustCompile(`^[ \t]*\[\[[ \t]*machine[ \t]*\]\][ \t]*(#.*)?$`)

// proposeRoster returns roster.toml text data with entry added as text: a new machine's table at the end, or the
// Director's table at the end of its machine's, so comments and layout are kept. It refuses, with
// ErrRosterConflict, a result that does not parse to exactly r with the entry added.
func proposeRoster(data []byte, r *Roster, e RosterEntry) ([]byte, error) {
	want := Roster{SchemaVersion: r.SchemaVersion, Machines: slices.Clone(r.Machines)}
	d := Director{Name: e.Director, CopyDir: e.CopyDir, Repositories: slices.Clone(e.Repositories)}
	quoted := make([]string, len(e.Repositories))
	for i, id := range e.Repositories {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	block := fmt.Sprintf("[[machine.director]]\nname = %q\ncopy_dir = %q\nrepositories = [%s]\n", e.Director, e.CopyDir, strings.Join(quoted, ", "))
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if e.NewMachine {
		want.Machines = append(want.Machines, Machine{Label: e.Machine, SSHAlias: e.SSHAlias, OS: e.OS, Roles: []Role{RoleDirector}, Directors: []Director{d}})
		text += fmt.Sprintf("\n[[machine]]\nlabel = %q\nssh_alias = %q\nos = %q\nroles = [%q]\n\n", e.Machine, e.SSHAlias, e.OS, RoleDirector) + block
	} else {
		k := slices.IndexFunc(r.Machines, func(m Machine) bool { return m.Label == e.Machine })
		if k < 0 {
			return nil, fmt.Errorf("%w: machine %q is not in the roster", ErrRosterConflict, e.Machine)
		}
		m := want.Machines[k]
		m.Directors = append(slices.Clone(m.Directors), d)
		want.Machines[k] = m
		lines := strings.SplitAfter(text, "\n")
		var headers []int
		for i, line := range lines {
			if machineHeader.MatchString(strings.TrimRight(line, "\r\n")) {
				headers = append(headers, i)
			}
		}
		if len(headers) != len(r.Machines) {
			return nil, fmt.Errorf("%w: roster.toml does not write each machine as a [[machine]] table, so the entry cannot be added safely; add it by hand", ErrRosterConflict)
		}
		if k+1 < len(headers) {
			at := headers[k+1]
			text = strings.Join(lines[:at], "") + block + "\n" + strings.Join(lines[at:], "")
		} else {
			text += "\n" + block
		}
	}
	got, err := ParseRoster([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("%w: the roster with the entry added does not parse: %v", ErrRosterConflict, err)
	}
	if !reflect.DeepEqual(*got, want) {
		return nil, fmt.Errorf("%w: the roster with the entry added is not the roster plus the entry; add it by hand", ErrRosterConflict)
	}
	return []byte(text), nil
}

// hasEntry reports whether r holds e exactly: its machine, as planned when new, and its Director.
func hasEntry(r *Roster, e RosterEntry) bool {
	m, ok := r.Machine(e.Machine)
	if !ok || m.SSHAlias != e.SSHAlias || m.OS != e.OS {
		return false
	}
	for _, d := range m.Directors {
		if d.Name == e.Director {
			return d.CopyDir == e.CopyDir && slices.Equal(d.Repositories, e.Repositories)
		}
	}
	return false
}
