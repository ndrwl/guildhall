package steward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/admit"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/setup"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// SetupKindProvision is a setup plan that makes a new managed Director copy on a roster machine that already has
// Guildhall (docs/steward.md#provisioning).
const SetupKindProvision = "provision"

// ProvisionRequestSchemaVersion is the schema_version of a provisioning request.
const ProvisionRequestSchemaVersion = 1

// The Guildhall steps of a provisioning plan, after the machine's gaps, in the order they run; the managed, enrol,
// reload and roster steps are the takeover's.
const (
	StepConfig  = "config"
	StepStore   = "store"
	StepAdopt   = "adopt"
	StepStart   = "start"
	StepSummary = "summary"
	// A clone step is "clone-" and the repository id; a copy step for a Steward-written step file "steps-" and its
	// name.
	stepClonePrefix = "clone-"
	stepFilesPrefix = "steps-"
)

// What a provisioning step closes: a fixable gap the Steward fixes with setup doctor --fix --only, or a human gap
// the Guildmaster closes with the wizard on the machine, which the Steward never runs.
const (
	StepKindFix    = "fix"
	StepKindWizard = "wizard"
	// StepKindSummary is the last step, which verifies the copy and writes summary.json.
	StepKindSummary = "summary"
)

// directorSteps is the built-in step file whose copy-dir step makes the new copy's directory and whose other steps
// check the Guildhall pin and Tailscale SSH; every provisioning plan runs it first.
const directorSteps = "director"

// preflightSteps is the built-in step file whose gaps are a copy directory with a config or store and a clone path
// that is not empty. A plan runs its doctor before it acts and refuses with ErrCopyExists on any gap; it is never one
// of the plan's StepFiles, whose gaps become steps.
const preflightSteps = "new-copy"

// DefaultStepFiles are the step files a request that names none is checked against: the copy's directory, Herdr,
// Go, the administrator installs and the sign-ins.
var DefaultStepFiles = []string{directorSteps, "herdr", "go", "git-and-claude", "sign-in"}

// The plan files of a provisioning plan beside plan.json: the generated config.toml, Steward-written step files and
// the verified summary.
const (
	setupConfigFile = setupManagedDir + "/config.toml"
	setupStepsDir   = setupManagedDir + "/steps"
	setupSummary    = "summary.json"
	// stewardStepsDir is where, under the new copy, the plan's Steward-written step files are copied.
	stewardStepsDir = "state/steward/setup"
)

var (
	// ErrCopyExists reports a copy directory that already holds a config or store, or a clone path that is not
	// empty; provisioning changes nothing there.
	ErrCopyExists = errors.New("provisioning refuses an existing copy, store or non-empty clone path")
	// ErrWizardRecord reports a wizard completion record that is missing, malformed or of another step file.
	ErrWizardRecord = errors.New("the wizard's completion record is missing, malformed or of another plan")
)

// ProvisionRequest is what the Guildmaster asks the Steward to provision, strict TOML: the roster machine, the new
// Director's name, its copy and setup directories there, the Herdr session its tab opens in, the step files the
// doctor checks the machine against (built-in names or step file paths, the latter Steward-written and relative to
// the request), the pins, and one or more repositories it clones.
type ProvisionRequest struct {
	SchemaVersion int      `toml:"schema_version" json:"schema_version"`
	Machine       string   `toml:"machine" json:"machine"`
	Director      string   `toml:"director" json:"director"`
	CopyDir       string   `toml:"copy_dir" json:"copy_dir"`
	SetupDir      string   `toml:"setup_dir" json:"setup_dir"`
	HerdrSession  string   `toml:"herdr_session" json:"herdr_session"`
	StepFiles     []string `toml:"step_files" json:"step_files"`
	// Bootstrap lets the dry run place Guildhall on a bare machine, one with no file at the roster's guildhall_path:
	// the plan then only copies the Steward's own binary there and installs it (docs/steward.md#bootstrapping-a-bare-machine).
	Bootstrap    bool                  `toml:"bootstrap" json:"bootstrap,omitempty"`
	Pins         Pins                  `toml:"pins" json:"pins"`
	Repositories []ProvisionRepository `toml:"repository" json:"repositories"`
}

// Pins are the versions the provisioning installs or checks: Herdr's version, download URL and sha256 for the
// herdr step file, and the Guildhall release the director step file's guildhall-pin step checks the machine runs
// and the summary compares the copy's pin against.
type Pins struct {
	HerdrVersion string `toml:"herdr_version" json:"herdr_version,omitempty"`
	HerdrURL     string `toml:"herdr_url" json:"herdr_url,omitempty"`
	HerdrSHA256  string `toml:"herdr_sha256" json:"herdr_sha256,omitempty"`
	Guildhall    string `toml:"guildhall" json:"guildhall,omitempty"`
	// GoVersion, GoURL and GoSHA256 are the Go release archive the go step file unpacks when the machine has no Go.
	GoVersion string `toml:"go_version" json:"go_version,omitempty"`
	GoURL     string `toml:"go_url" json:"go_url,omitempty"`
	GoSHA256  string `toml:"go_sha256" json:"go_sha256,omitempty"`
}

// ProvisionRepository is one repository the new copy works on, cloned from Remote into the empty Path.
type ProvisionRepository struct {
	ID     string `toml:"id" json:"id"`
	Remote string `toml:"remote" json:"remote"`
	Path   string `toml:"path" json:"path"`
}

// StepFile is a step file a provisioning plan checks the machine against: a built-in, or one the Steward wrote,
// kept under the plan directory as File and copied to Ref on the machine. Ref is what setup doctor's --steps gets,
// SHA256 its digest, and Sets the --set values it is given.
type StepFile struct {
	Name    string   `json:"name"`
	Builtin bool     `json:"builtin"`
	SHA256  string   `json:"sha256"`
	Ref     string   `json:"ref"`
	File    string   `json:"file,omitempty"`
	Sets    []string `json:"sets,omitempty"`
}

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// ParseProvisionRequest decodes a request strictly and checks it; a step file path in it is taken relative to dir
// and made absolute.
func ParseProvisionRequest(data []byte, dir string) (*ProvisionRequest, error) {
	var r ProvisionRequest
	if _, err := decodeStrict("provisioning request", data, ProvisionRequestSchemaVersion, &r); err != nil {
		return nil, err
	}
	for i, ref := range r.StepFiles {
		if strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".toml") {
			if !filepath.IsAbs(ref) {
				ref = filepath.Join(dir, ref)
			}
			abs, err := filepath.Abs(ref)
			if err != nil {
				return nil, err
			}
			r.StepFiles[i] = abs
		}
	}
	if err := r.check(); err != nil {
		return nil, fmt.Errorf("provisioning request: %w", err)
	}
	return &r, nil
}

// check refuses a request whose values a plan cannot use as they are.
func (r *ProvisionRequest) check() error {
	var problems []string
	fault := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if r.SchemaVersion != ProvisionRequestSchemaVersion {
		fault("schema_version is %d, want %d", r.SchemaVersion, ProvisionRequestSchemaVersion)
	}
	if !workflow.ValidID(r.Machine) {
		fault("machine %q must be a lowercase id", r.Machine)
	}
	if !workflow.ValidID(r.Director) {
		fault("director %q must be a lowercase id", r.Director)
	}
	for key, dir := range map[string]string{"copy_dir": r.CopyDir, "setup_dir": r.SetupDir} {
		if !validDir(posixDirPattern, dir) {
			fault("%s %q must be an absolute Linux path of letters, digits, '.', '_', '-' and '/'", key, dir)
		}
	}
	if r.HerdrSession == "" || !SetPattern.MatchString("herdr_session="+r.HerdrSession) {
		fault("herdr_session %q must name the Herdr session the Director's tab opens in, in letters, digits, '.', '_', '-'", r.HerdrSession)
	}
	if len(r.Repositories) == 0 {
		fault("name at least one [[repository]]")
	}
	ids, paths := map[string]bool{}, map[string]bool{r.CopyDir: true}
	for i, repo := range r.Repositories {
		where := fmt.Sprintf("repository %d", i+1)
		if !workflow.ValidID(repo.ID) {
			fault("%s: id %q must be a lowercase id", where, repo.ID)
		} else if ids[repo.ID] {
			fault("%s: id %q is named twice", where, repo.ID)
		}
		ids[repo.ID] = true
		if !ValidRemote(repo.Remote) {
			fault("%s: remote %q must be an https:// URL or git@host:path of letters, digits, '.', '_', '~', '-' and '/'", where, repo.Remote)
		}
		if !validDir(posixDirPattern, repo.Path) {
			fault("%s: path %q must be an absolute Linux path of letters, digits, '.', '_', '-' and '/'", where, repo.Path)
		} else if paths[repo.Path] {
			fault("%s: path %q is the copy directory or another repository's", where, repo.Path)
		}
		paths[repo.Path] = true
	}
	seen := map[string]bool{}
	for _, ref := range r.StepFiles {
		if seen[ref] {
			fault("step file %q is named twice", ref)
		}
		seen[ref] = true
		if !filepath.IsAbs(ref) {
			if _, err := setup.BuiltinBytes(ref); err != nil {
				fault("step file %q: %v", ref, err)
			} else if ref == preflightSteps {
				fault("step file %q is the check every plan runs before it acts; leave it out", ref)
			}
		}
	}
	p := r.Pins
	switch {
	case p.HerdrVersion == "" && p.HerdrURL == "" && p.HerdrSHA256 == "":
	case !versionPattern.MatchString(p.HerdrVersion) || !strings.HasPrefix(p.HerdrURL, "https://") || !SetPattern.MatchString("herdr_url="+p.HerdrURL) || !hexPattern.MatchString(p.HerdrSHA256):
		fault("[pins] herdr_version, herdr_url and herdr_sha256 go together: a version, an https:// URL of letters, digits and '.', '_', '~', '+', ':', '/', '-', and a lowercase hex sha256")
	}
	switch {
	case p.GoVersion == "" && p.GoURL == "" && p.GoSHA256 == "":
	case !versionPattern.MatchString(p.GoVersion) || !strings.HasPrefix(p.GoURL, "https://") || !SetPattern.MatchString("go_url="+p.GoURL) || !hexPattern.MatchString(p.GoSHA256):
		fault("[pins] go_version, go_url and go_sha256 go together: a version, an https:// URL of letters, digits and '.', '_', '~', '+', ':', '/', '-', and a lowercase hex sha256")
	}
	if p.Guildhall != "" && !versionPattern.MatchString(p.Guildhall) {
		fault("[pins] guildhall %q is not a release version", p.Guildhall)
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// stepFileRefs are the step files the plan checks, the director built-in first.
func (r *ProvisionRequest) stepFileRefs() []string {
	refs := r.StepFiles
	if len(refs) == 0 {
		refs = DefaultStepFiles
	}
	if !slices.Contains(refs, directorSteps) {
		refs = append([]string{directorSteps}, refs...)
	}
	return refs
}

// RenderConfig is the config.toml a provisioning plan writes for the new copy: a director config with the request's
// name, its Herdr session in [local] and each repository owned by the request's machine. It is checked to load,
// with the plan's first managed.toml, as config check would.
func RenderConfig(r *ProvisionRequest) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# Written by the Steward's provisioning plan (docs/steward.md#provisioning).\n")
	fmt.Fprintf(&b, "schema_version = 1\nmode = %q\nname = %q\n\n[local]\n%s = %q\n", config.ModeDirector, r.Director, config.HerdrSessionKey, r.HerdrSession)
	for _, repo := range r.Repositories {
		fmt.Fprintf(&b, "\n[[repository]]\nid = %q\npath = %q\nremote = %q\nowner_machine = %q\n", repo.ID, repo.Path, repo.Remote, r.Machine)
	}
	data := []byte(b.String())
	if _, err := config.Parse(data); err != nil {
		return nil, err
	}
	return data, nil
}

// PlanProvision plans the provisioning of req on its roster machine into the new plan directory planDir, changing
// nothing: it runs setup doctor --json on the machine for each built-in step file and turns each fixable gap into a
// step the Steward fixes and each human gap into a wizard step for the Guildmaster, lists every Steward-written step
// file with its digest and every one of its steps, renders config.toml and the first managed.toml, and plans the
// Guildhall steps after them. It refuses with ErrCopyExists a copy directory with a config or store and a clone
// path holding a repository, and with ErrRosterConflict a Director, copy directory or repository the roster has.
func (s *Setup) PlanProvision(ctx context.Context, req *ProvisionRequest, planDir string) (*SetupPlan, error) {
	if err := req.check(); err != nil {
		return nil, fmt.Errorf("provisioning request: %w", err)
	}
	if !workflow.ValidID(s.Steward) {
		return nil, fmt.Errorf("the Steward's label %q, its [steward] name, must be a lowercase id", s.Steward)
	}
	planDir, err := filepath.Abs(planDir)
	if err != nil {
		return nil, err
	}
	rosterData, roster, err := s.headRoster(ctx)
	if err != nil {
		return nil, err
	}
	m, ok := roster.Machine(req.Machine)
	switch {
	case !ok:
		return nil, fmt.Errorf("%w: machine %q is not in the roster; add it to roster.toml first", ErrRosterConflict, req.Machine)
	case !slices.Contains(m.Roles, RoleDirector):
		return nil, fmt.Errorf("%w: machine %q lacks the director role; add it in roster.toml first", ErrRosterConflict, m.Label)
	case m.OS != OSLinux:
		return nil, fmt.Errorf("machine %q is %s: provisioning is built for Linux machines only so far", m.Label, m.OS)
	}
	entry := RosterEntry{Machine: m.Label, SSHAlias: m.SSHAlias, OS: m.OS, Director: req.Director, CopyDir: req.CopyDir}
	for _, repo := range req.Repositories {
		entry.Repositories = append(entry.Repositories, repo.ID)
	}
	for _, other := range roster.Machines {
		for _, d := range other.Directors {
			if d.Name == req.Director || (other.Label == m.Label && d.CopyDir == req.CopyDir) {
				return nil, fmt.Errorf("%w: machine %q already lists Director %q at %s", ErrRosterConflict, other.Label, d.Name, d.CopyDir)
			}
			for _, id := range d.Repositories {
				if slices.Contains(entry.Repositories, id) {
					return nil, fmt.Errorf("%w: repository %q is Director %q's on %q", ErrRosterConflict, id, d.Name, other.Label)
				}
			}
		}
	}
	reqCopy := *req
	reqCopy.StepFiles = slices.Clone(req.stepFileRefs())
	reqCopy.Repositories = slices.Clone(req.Repositories)
	if req.Bootstrap {
		bare, err := s.bareMachine(ctx, m)
		if err != nil {
			return nil, err
		}
		if bare {
			return s.planBootstrap(m, &reqCopy, entry, rosterData, planDir)
		}
	}
	p := &SetupPlan{Schema: SetupPlanSchema, Kind: SetupKindProvision, Machine: m.Label, Director: req.Director, CopyDir: req.CopyDir,
		SSHAlias: m.SSHAlias, Guildhall: m.Guildhall(), Steward: s.Steward, PlanDir: planDir, SetupDir: req.SetupDir,
		RosterDigest: digest.Of(rosterData).Hex(), Roster: entry, Request: &reqCopy, files: map[string][]byte{}}

	cfg, err := RenderConfig(req)
	if err != nil {
		return nil, fmt.Errorf("provisioning request: the config.toml it makes does not load: %w", err)
	}
	managed, err := config.EncodeManaged(map[string]config.ManagedSetting{})
	if err != nil {
		return nil, err
	}
	if _, err := config.ParseCombined(cfg, managed); err != nil {
		return nil, fmt.Errorf("provisioning request: the config.toml it makes does not load with the first managed.toml: %w", err)
	}

	var written []StepFile
	for _, ref := range reqCopy.StepFiles {
		sf, data, err := s.planStepFile(p, ref)
		if err != nil {
			return nil, err
		}
		if !sf.Builtin {
			p.files[sf.File] = data
			written = append(written, sf)
		}
		p.StepFiles = append(p.StepFiles, sf)
	}
	if err := uniqueNames(p.StepFiles); err != nil {
		return nil, err
	}

	// The preflight doctor runs first: a copy directory with a config or store, or a clone path that is not empty,
	// refuses the plan before anything else is read.
	var clonePaths []string
	for _, repo := range req.Repositories {
		clonePaths = append(clonePaths, repo.Path)
	}
	pre, err := preflightFile(req.CopyDir, clonePaths)
	if err != nil {
		return nil, err
	}
	p.Preflight = &pre
	r, err := s.doctor(ctx, p, pre)
	if err != nil {
		return nil, err
	}
	if err := copyExists(m.Label, r); err != nil {
		return nil, err
	}
	p.Doctor = append(p.Doctor, r)
	for _, sf := range p.StepFiles {
		if !sf.Builtin {
			continue
		}
		r, err := s.doctor(ctx, p, sf)
		if err != nil {
			return nil, err
		}
		p.Doctor = append(p.Doctor, r)
	}

	g := p.Guildhall
	for i, sf := range p.StepFiles {
		if !sf.Builtin {
			continue
		}
		r := p.Doctor[slices.IndexFunc(p.Doctor, func(r setup.Report) bool { return r.Steps == sf.Name })]
		for _, gap := range r.Gaps {
			if gap.Tag == setup.Fixable && sf.Name == "herdr" && req.Pins.HerdrURL == "" {
				return nil, fmt.Errorf("the machine has no Herdr (%s), and the request pins none to install; set [pins] herdr_version, herdr_url and herdr_sha256", gap.Detail)
			}
			if gap.ID == "go" && sf.Name == "go" && req.Pins.GoURL == "" {
				return nil, fmt.Errorf("the machine has no Go (%s), and the request pins none to install; set [pins] go_version, go_url and go_sha256", oneLine(gap.Detail))
			}
			p.Steps = append(p.Steps, gapStep(g, p.SetupDir, p.StepFiles[i], gap.ID, gap.Tag, gap.Title, gap.Detail))
		}
	}
	for _, sf := range written {
		p.Files = append(p.Files, PlannedFile{Name: sf.File, Target: sf.Ref, SHA256: sf.SHA256})
		p.Steps = append(p.Steps, SetupStep{ID: stepFilesPrefix + sf.Name, Kind: StepKindCopy,
			Argv:   scpCommand(filepath.Join(planDir, filepath.FromSlash(sf.File)), Remote{m.SSHAlias, sf.Ref}.String()),
			Expect: "the Steward-written step file " + sf.Name + " is copied to " + sf.Ref + " and read back with sha256 " + sf.SHA256})
		steps, err := setup.Parse(p.files[sf.File], sf.SHA256)
		if err != nil {
			return nil, err
		}
		for _, st := range steps.Steps {
			if st.ForOS("linux") {
				p.Steps = append(p.Steps, gapStep(g, p.SetupDir, sf, st.ID, st.Tag, st.Title, ""))
			}
		}
	}

	p.files[setupConfigFile], p.files[setupManagedDir+"/managed.toml"] = cfg, managed
	p.Files = append(p.Files,
		PlannedFile{Name: setupConfigFile, Target: req.CopyDir + "/config.toml", SHA256: digest.Of(cfg).Hex()},
		PlannedFile{Name: setupManagedDir + "/managed.toml", Target: req.CopyDir + "/managed.toml", SHA256: digest.Of(managed).Hex()})
	put := func(id, name, what string) SetupStep {
		target := Remote{m.SSHAlias, req.CopyDir + "/" + filepath.Base(name)}
		return SetupStep{ID: id, Kind: StepKindCopy, Argv: scpCommand(filepath.Join(planDir, filepath.FromSlash(name)), target.String()),
			Expect: what + " is copied to " + target.String() + " and read back with digest " + string(digest.Of(p.files[name]))}
	}
	p.Steps = append(p.Steps,
		put(StepConfig, setupConfigFile, "the generated config.toml, which loads with the first managed.toml,"),
		put(StepManaged, setupManagedDir+"/managed.toml", "managed.toml, with no managed key,"),
		SetupStep{ID: StepStore, Kind: StepKindRemote, Argv: StoreInitCommand(g, req.CopyDir), Expect: "the copy's new " + admit.StoreFile + " is made; store init refuses one that exists"})
	for _, repo := range req.Repositories {
		p.Steps = append(p.Steps, SetupStep{ID: stepClonePrefix + repo.ID, Kind: StepKindRemote, Argv: CloneCommand(repo.Remote, repo.Path),
			Expect: "repository " + repo.ID + " is cloned into " + repo.Path + "; git clone refuses a path that is not empty"})
	}
	p.Steps = append(p.Steps,
		SetupStep{ID: StepAdopt, Kind: StepKindRemote, Argv: AdoptCommand(g, req.CopyDir), Expect: "the guildhall on the machine's PATH is the copy's first release pin"},
		SetupStep{ID: StepEnrol, Kind: StepKindRemote, Argv: EnrolCommand(g, s.Steward, req.CopyDir), Expect: "the copy is enrolled with Steward " + s.Steward},
		SetupStep{ID: StepReload, Kind: StepKindRemote, Argv: ReloadCommand(g, req.CopyDir), Expect: "director mode reports the copy managed, with its managed.toml authorized"},
		SetupStep{ID: StepStart, Kind: StepKindRemote, Argv: StartTabCommand(g, req.CopyDir), Expect: "the Director's session starts in its Herdr tab in session " + req.HerdrSession + ", and director status reports it running with the tab recorded"},
		SetupStep{ID: StepRoster, Kind: StepKindRoster, Expect: "roster.toml holds the entry, committed alone to the private config and pushed"},
		SetupStep{ID: StepSummary, Kind: StepKindSummary, Expect: "the copy is managed, its session's tab is recorded, the doctor is run again for every step file, and summary.json lists the versions against the pins and any outstanding manual item"})
	seen := map[string]bool{}
	for _, st := range p.Steps {
		if seen[st.ID] {
			return nil, fmt.Errorf("step id %q is used twice in the plan; rename the step in the Steward-written step file", st.ID)
		}
		seen[st.ID] = true
	}

	after, err := proposeRoster(rosterData, roster, entry)
	if err != nil {
		return nil, err
	}
	p.RosterDiff = lineDiff(string(rosterData), string(after))
	p.Digest = p.digest()
	return p, nil
}

// planStepFile resolves one of the request's step files: a built-in by name, with the --set values the plan gives
// it, or a Steward-written file read from its path, which the plan keeps and copies to the copy's
// state/steward/setup under its name and digest.
func (s *Setup) planStepFile(p *SetupPlan, ref string) (StepFile, []byte, error) {
	if !filepath.IsAbs(ref) {
		data, err := setup.BuiltinBytes(ref)
		if err != nil {
			return StepFile{}, nil, err
		}
		sf := StepFile{Name: ref, Builtin: true, SHA256: setup.Digest(data), Ref: ref}
		pins := p.Request.Pins
		switch ref {
		case directorSteps:
			sf.Sets = []string{"copy_dir=" + p.CopyDir, "guildhall=" + p.Guildhall}
			if pins.Guildhall != "" {
				sf.Sets = append(sf.Sets, "guildhall_pin="+pins.Guildhall)
			}
		case "herdr":
			if pins.HerdrURL != "" {
				sf.Sets = []string{"herdr_sha256=" + pins.HerdrSHA256, "herdr_url=" + pins.HerdrURL, "herdr_version=" + pins.HerdrVersion}
			}
			sf.Sets = append(sf.Sets, "herdr_session="+p.Request.HerdrSession)
		case "go":
			if pins.GoURL != "" {
				sf.Sets = []string{"go_sha256=" + pins.GoSHA256, "go_url=" + pins.GoURL, "go_version=" + pins.GoVersion}
			}
		}
		for _, kv := range sf.Sets {
			if !SetPattern.MatchString(kv) {
				return StepFile{}, nil, fmt.Errorf("step file %s: --set %q is outside the restricted character set", ref, kv)
			}
		}
		return sf, data, nil
	}
	data, err := os.ReadFile(ref)
	if err != nil {
		return StepFile{}, nil, fmt.Errorf("the Steward-written step file: %w", err)
	}
	steps, err := setup.Parse(data, "")
	if err != nil {
		return StepFile{}, nil, fmt.Errorf("the Steward-written step file %s: %w", ref, err)
	}
	if _, err := setup.BuiltinBytes(steps.Name); err == nil {
		return StepFile{}, nil, fmt.Errorf("the Steward-written step file %s is named %q, a built-in's name; name it otherwise", ref, steps.Name)
	}
	sum := setup.Digest(data)
	return StepFile{Name: steps.Name, SHA256: sum, File: setupStepsDir + "/" + steps.Name + ".toml",
		Ref: p.CopyDir + "/" + stewardStepsDir + "/" + steps.Name + "-" + sum[:12] + ".toml"}, data, nil
}

// preflightFile is the built-in new-copy step file given copyDir, when it is not empty, and the clone paths.
func preflightFile(copyDir string, clonePaths []string) (StepFile, error) {
	data, err := setup.BuiltinBytes(preflightSteps)
	if err != nil {
		return StepFile{}, err
	}
	sf := StepFile{Name: preflightSteps, Builtin: true, SHA256: setup.Digest(data), Ref: preflightSteps}
	if copyDir != "" {
		sf.Sets = append(sf.Sets, "copy_dir="+copyDir)
	}
	sf.Sets = append(sf.Sets, "clone_paths="+strings.Join(clonePaths, ":"))
	return sf, nil
}

// copyExists is ErrCopyExists naming each gap of a new-copy report, or nil when it has none.
func copyExists(machine string, r setup.Report) error {
	if len(r.Gaps) == 0 {
		return nil
	}
	var seen []string
	for _, g := range r.Gaps {
		seen = append(seen, g.ID+": "+strings.Join(strings.Fields(g.Detail), " "))
	}
	return fmt.Errorf("%w: on %s, %s; it is left as it is", ErrCopyExists, machine, strings.Join(seen, "; "))
}

func uniqueNames(files []StepFile) error {
	seen := map[string]bool{}
	for _, sf := range files {
		if seen[sf.Name] {
			return fmt.Errorf("two step files are named %q", sf.Name)
		}
		seen[sf.Name] = true
	}
	return nil
}

// gapStep is the plan step that closes gap id of step file sf: a fix step running setup doctor --fix --only for a
// fixable gap, or a wizard step whose Argv is the command the Guildmaster runs on the machine for a human one.
func gapStep(guildhall, setupDir string, sf StepFile, id string, tag setup.Tag, title, detail string) SetupStep {
	saw := ""
	if detail != "" {
		saw = " (the doctor saw: " + oneLine(detail) + ")"
	}
	if tag == setup.Human {
		argv := []string{guildhall, "setup", "wizard", "--steps", sf.Ref, "--digest", sf.SHA256, "--only", id, "--setup-dir", setupDir}
		for _, kv := range sf.Sets {
			argv = append(argv, "--set", kv)
		}
		return SetupStep{ID: id, Kind: StepKindWizard, Argv: argv, StepFile: sf.Name, GapIDs: []string{id},
			Expect: "the Guildmaster runs this wizard on the machine to " + lowerFirst(title) + saw + "; the Steward reads its completion record back, and the doctor no longer reports " + id}
	}
	return SetupStep{ID: id, Kind: StepKindFix, Argv: FixCommand(guildhall, id, sf.Ref, sf.SHA256, setupDir, sf.Sets), StepFile: sf.Name, GapIDs: []string{id},
		Expect: "the doctor fixes " + id + " to " + lowerFirst(title) + saw + ", checking it first, and then no longer reports it"}
}

// oneLine is the first line of a doctor's detail, bounded.
func oneLine(s string) string { return firstLine(s, 200) }

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// stepFile returns the plan's step file named name.
func (p *SetupPlan) stepFile(name string) (StepFile, bool) {
	i := slices.IndexFunc(p.StepFiles, func(sf StepFile) bool { return sf.Name == name })
	if i < 0 {
		return StepFile{}, false
	}
	return p.StepFiles[i], true
}

// doctor runs setup doctor --json on the machine for sf and returns its report, refusing one of another step file.
func (s *Setup) doctor(ctx context.Context, p *SetupPlan, sf StepFile) (setup.Report, error) {
	return s.doctorRun(ctx, p, sf, DoctorCommand(p.Guildhall, sf.Ref, sf.SHA256, p.SetupDir, sf.Sets))
}

func (s *Setup) doctorRun(ctx context.Context, p *SetupPlan, sf StepFile, argv []string) (setup.Report, error) {
	var r setup.Report
	out, err := s.Runner.Run(ctx, p.SSHAlias, argv)
	if err == nil {
		err = json.Unmarshal(out, &r)
	}
	if err == nil && (r.Schema != setup.ReportSchema || r.Digest != sf.SHA256) {
		err = fmt.Errorf("it printed a report of %q, digest %s, not %s's %s", r.Schema, r.Digest, sf.Name, sf.SHA256)
	}
	if err != nil {
		return r, fmt.Errorf("running setup doctor --json for step file %s: %w", sf.Name, err)
	}
	return r, nil
}

// recordPath is where on the machine the wizard writes the completion record of sf, as setup.RecordPath names it.
func (p *SetupPlan) recordPath(sf StepFile) string {
	return filepath.ToSlash(setup.RecordPath(p.SetupDir, sf.Name, sf.SHA256))
}
