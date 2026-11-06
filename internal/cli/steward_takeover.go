package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
)

// stewardSetupTask is the Steward store's reserved task of setup plan approvals, one decision per takeover plan
// (docs/steward.md#takeover); only the Guildmaster answers them.
const stewardSetupTask = "setup"

// setupApplyTimeout bounds one takeover apply, its ssh runs, copies, commit and push together.
const setupApplyTimeout = 30 * time.Minute

// setupDecisionID is the id of the approval decision of setup plan digest d: "setup-" and its first 12 hex digits.
func setupDecisionID(d string) string {
	hex := strings.TrimPrefix(d, "sha256:")
	return "setup-" + hex[:min(12, len(hex))]
}

// stewardTakeoverCmd runs `guildhall steward takeover`: --dry-run plans the takeover of an existing Director copy and
// writes the plan, --apply carries out an approved plan, resuming at its first step not done, and --abandon gives
// up a plan before its copy is changed, resuming a session it paused (docs/steward.md#takeover).
func (a *App) stewardTakeoverCmd(args []string) int {
	fs := flag.NewFlagSet("steward takeover", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", "", "")
	machine := fs.String("machine", "", "")
	director := fs.String("director", "", "")
	copyDir := fs.String("copy", "", "")
	alias := fs.String("ssh-alias", "", "")
	machineOS := fs.String("os", "", "")
	planDir := fs.String("plan", "", "")
	dryRun := fs.Bool("dry-run", false, "")
	apply := fs.Bool("apply", false, "")
	abandon := fs.Bool("abandon", false, "")
	approve := fs.String("approve", "", "")
	err := fs.Parse(args)
	modes := 0
	for _, on := range []bool{*dryRun, *apply, *abandon} {
		if on {
			modes++
		}
	}
	switch {
	case err != nil:
	case fs.NArg() > 0:
		err = fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case modes != 1:
		err = errors.New("give exactly one of --dry-run, --apply and --abandon")
	case *cfgPath == "" || *planDir == "":
		err = errors.New("--config and --plan are required")
	case *dryRun && (*machine == "" || *director == "" || *copyDir == ""):
		err = errors.New("--dry-run needs --machine, --director and --copy")
	case !*dryRun && (*machine != "" || *director != "" || *copyDir != "" || *alias != "" || *machineOS != ""):
		err = errors.New("--machine, --director, --copy, --ssh-alias and --os belong to --dry-run; --apply and --abandon read them from the plan")
	case *machineOS != "" && (*alias == "" || (*machineOS != string(steward.OSLinux) && *machineOS != string(steward.OSWindows))):
		err = errors.New("--os, linux or windows, names the operating system of a machine new to the roster, with --ssh-alias")
	case !*apply && *approve != "":
		err = errors.New("--approve applies only with --apply")
	}
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: steward takeover: %v\n\n%s", err, stewardUsage)
		return 2
	}
	s, err := a.loadSetup(*cfgPath)
	if err != nil {
		return a.fail(fmt.Errorf("steward takeover: %w", err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), setupApplyTimeout)
	defer cancel()
	switch {
	case *dryRun:
		p, err := s.PlanTakeover(ctx, steward.TakeoverInput{Machine: *machine, Director: *director, CopyDir: *copyDir, SSHAlias: *alias, OS: steward.OS(*machineOS), PlanDir: *planDir})
		if err != nil {
			return a.fail(fmt.Errorf("steward takeover: %w", err))
		}
		if err := steward.WriteSetupPlan(p); err != nil {
			return a.fail(fmt.Errorf("steward takeover: %w", err))
		}
		if _, err := io.WriteString(a.Stdout, renderSetupPlan(p, *cfgPath)); err != nil {
			return 1
		}
		return 0
	case *abandon:
		out, err := s.Abandon(ctx, *planDir)
		return a.setupOutcome("steward takeover", out, err)
	}
	return a.applySetup(ctx, "steward takeover", steward.SetupKindTakeover, s, *cfgPath, *planDir, *approve)
}

// applySetup applies the approved setup plan of kind in planDir for the command name and prints its outcome.
func (a *App) applySetup(ctx context.Context, name, kind string, s *steward.Setup, cfgPath, planDir, approve string) int {
	if saved, err := steward.ReadSetupPlan(planDir); err == nil && saved.Kind != kind {
		return a.fail(fmt.Errorf("%s: %s holds a %s plan; apply it with guildhall steward %s", name, planDir, saved.Kind, saved.Kind))
	}
	// An agent in a session, the Steward's among them, never approves a setup plan itself: its --approve stands only
	// on the Guildmaster's recorded answer to the plan's approval decision.
	if approve != "" && a.inSession() {
		storePath := filepath.Join(filepath.Dir(cfgPath), stewardStoreFile)
		ok, err := stewardDecisionApproved(storePath, stewardSetupTask, setupDecisionID(approve), approve)
		if err != nil {
			return a.fail(fmt.Errorf("%s: %w", name, err))
		}
		if !ok {
			return a.fail(fmt.Errorf("%s: inside a Guildhall session --approve %s needs the Guildmaster's answer %q to decision %s/%s/%s in %s; open it with guildhall steward approval request --plan %s and ask the Guildmaster to answer it",
				name, approve, stewardApprove, stewardRepository, stewardSetupTask, setupDecisionID(approve), storePath, shellQuote([]string{planDir})))
		}
	}
	out, err := s.ApplySetup(ctx, planDir, approve)
	if errors.Is(err, steward.ErrNotApproved) {
		if saved, rerr := steward.ReadSetupPlan(planDir); rerr == nil {
			err = fmt.Errorf("%w; open its approval decision with guildhall steward approval request --plan %s, and once the Guildmaster approves, add --approve %s",
				err, shellQuote([]string{planDir}), saved.Digest)
		}
	}
	return a.setupOutcome(name, out, err)
}

// setupOutcome prints each step's record and the outcome, and returns 0 only when the plan is done.
func (a *App) setupOutcome(name string, out *steward.Outcome, err error) int {
	if out != nil {
		var b strings.Builder
		for _, r := range out.Log.Steps {
			fmt.Fprintf(&b, "%s: %s", r.ID, r.State)
			for _, s := range []string{r.Note, r.Error} {
				if s != "" {
					fmt.Fprintf(&b, ": %s", s)
				}
			}
			b.WriteByte('\n')
		}
		if out.Log.Abandoned {
			b.WriteString("abandoned\n")
		} else if out.State != "" {
			b.WriteString(out.String() + "\n")
		}
		if _, werr := io.WriteString(a.Stdout, b.String()); werr != nil {
			return 1
		}
	}
	var stepErr *steward.ErrStepFailed
	switch {
	case errors.As(err, &stepErr):
		return 1
	case err != nil:
		return a.fail(fmt.Errorf("%s: %w", name, err))
	case out.State != steward.OutcomeDone:
		return 1
	}
	return 0
}

// loadSetup is the Setup over the Steward copy's config and private config checkout: it enrols copies with the
// Steward's [steward] name.
func (a *App) loadSetup(cfgPath string) (*steward.Setup, error) {
	cfg, err := admitStewardConfig(filepath.Dir(cfgPath), cfgPath)
	if err != nil {
		return nil, err
	}
	private, err := a.privateCheckout(cfg)
	if err != nil {
		return nil, err
	}
	gitPath, err := a.LookPath("git")
	if err != nil {
		return nil, err
	}
	copier, runner := a.Copier, a.Runner
	if copier == nil {
		scp, err := a.LookPath("scp")
		if err != nil {
			return nil, fmt.Errorf("scp is needed to read and publish a copy's files: %w", err)
		}
		copier = steward.Transport{Path: scp, Timeout: scpTimeout}
	}
	if runner == nil {
		ssh, err := a.LookPath("ssh")
		if err != nil {
			return nil, fmt.Errorf("ssh is needed to run the takeover's remote commands: %w", err)
		}
		runner = steward.SSH{Path: ssh, Timeout: sshTimeout, LongTimeout: sshLongTimeout}
	}
	return &steward.Setup{Private: git.WorkTree{Program: git.Program{Path: gitPath, Timeout: scpTimeout}, Dir: private},
		Transport: copier, Runner: runner, Steward: cfg.Steward.Name, Now: a.now, Self: a.selfBinary}, nil
}

// selfBinary is this Steward's own Guildhall release, which provisioning copies to a bare machine: the running
// binary, which must report a release version, not modified, with a release contract, and the PATH launcher, the
// home's when it has one, else one found beside the binary or on PATH.
func (a *App) selfBinary() (steward.SelfBinary, error) {
	if a.Executable == nil {
		return steward.SelfBinary{}, errors.New("the running binary cannot be found")
	}
	exe, err := a.Executable()
	if err != nil {
		return steward.SelfBinary{}, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	r, err := a.releaseProbe(context.Background(), exe)
	if err != nil {
		return steward.SelfBinary{}, fmt.Errorf("%s: version --json: %w", exe, err)
	}
	if r.Version == "dev" || r.Modified || r.ReleaseContract == 0 {
		return steward.SelfBinary{}, fmt.Errorf("%s reports version %s (modified %v, release contract %d), which is not a release; run the Steward on an installed release, such as go install github.com/ndrwl/guildhall/cmd/guildhall@<version>", exe, r.Version, r.Modified, r.ReleaseContract)
	}
	launcher := ""
	if h, err := a.releaseHome(); err == nil {
		if info, err := os.Stat(h.Launcher()); err == nil && info.Mode().IsRegular() {
			launcher = h.Launcher()
		}
	}
	if launcher == "" {
		if launcher, err = a.findLauncher("", exe); err != nil {
			return steward.SelfBinary{}, err
		}
	}
	return steward.SelfBinary{Path: exe, Launcher: launcher, Version: r.Version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, nil
}

// sshLongTimeout bounds each remote command of a setup plan that may take minutes: a clone, a fix that downloads,
// a release adopt and a session start.
const sshLongTimeout = 15 * time.Minute

// bootstrapSource names the release a bootstrap plan places: this Steward's own, or the cross-built one in the
// request's bootstrap_from.
func bootstrapSource(p *steward.SetupPlan) string {
	if p.Request != nil && p.Request.BootstrapFrom != "" {
		return "the cross-built release from " + p.Request.BootstrapFrom + ","
	}
	return "this Steward's own"
}

// renderSetupPlan describes a dry-run setup plan: the copy, the roster change, the files it copies, a provisioning's
// step files, each Steward-written one in full, and every step with its command.
func renderSetupPlan(p *steward.SetupPlan, cfgPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s plan %s\ndry run: nothing was changed\n\n", p.Kind, p.Digest)
	switch {
	case p.Bootstrap != nil:
		bs := p.Bootstrap
		fmt.Fprintf(&b, "%s (%s): a bare machine, with nothing at guildhall_path %s\n", p.Machine, p.SSHAlias, bs.Guildhall)
		fmt.Fprintf(&b, "this plan only bootstraps Guildhall: it copies %s guildhall %s (%s) there and the launcher to %s, checks each with %s before it first runs, and installs it with release install --from --sha256\n", bootstrapSource(p), bs.Version, bs.Platform, bs.Launcher, steward.HashCommand(p.Roster.OS, "")[0])
		fmt.Fprintf(&b, "once it is done, make the dry run again with the same request to plan Director %s at %s\n", p.Director, p.CopyDir)
	case p.Kind == steward.SetupKindProvision:
		fmt.Fprintf(&b, "%s/%s (%s:%s): a new copy with setup directory %s, enrolled with Steward %s once applied\n", p.Machine, p.Director, p.SSHAlias, p.CopyDir, p.SetupDir, p.Steward)
	default:
		fmt.Fprintf(&b, "%s/%s (%s:%s): %s, session %s, enrolled with Steward %s once applied\n", p.Machine, p.Director, p.SSHAlias, p.CopyDir, p.Probe.Mode, p.Session(), p.Steward)
	}
	if p.RosterDiff != "" {
		fmt.Fprintf(&b, "roster.toml:\n")
		for line := range strings.Lines(p.RosterDiff) {
			b.WriteString("    " + line)
		}
	}
	for _, f := range p.Files {
		fmt.Fprintf(&b, "file %s -> %s (sha256 %s)\n", f.Name, f.Target, f.SHA256)
	}
	for _, sf := range p.StepFiles {
		if sf.Builtin {
			fmt.Fprintf(&b, "step file %s: built-in, sha256 %s\n", sf.Name, sf.SHA256)
			continue
		}
		fmt.Fprintf(&b, "step file %s: written by the Steward, sha256 %s, runs only under that digest; in full:\n", sf.Name, sf.SHA256)
		data, _ := os.ReadFile(filepath.Join(p.PlanDir, filepath.FromSlash(sf.File)))
		for line := range strings.Lines(string(data)) {
			b.WriteString("    | " + line)
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	if pre := p.Preflight; pre != nil {
		fmt.Fprintf(&b, "step file %s: built-in, sha256 %s, checked before the plan acts and before each clone; any gap refuses it\n", pre.Name, pre.SHA256)
	}
	for _, r := range p.Doctor {
		fmt.Fprintf(&b, "doctor %s: %d gap(s)\n", r.Steps, len(r.Gaps))
	}
	b.WriteString("steps:\n")
	for i, st := range p.Steps {
		fmt.Fprintf(&b, "  %d. %s: %s\n", i+1, st.ID, st.Expect)
		switch st.Kind {
		case steward.StepKindRemote, steward.StepKindFix:
			fmt.Fprintf(&b, "     ssh -o BatchMode=yes -- %s %s\n", p.SSHAlias, shellQuote(st.Argv))
		case steward.StepKindCopy:
			fmt.Fprintf(&b, "     %s\n", shellQuote(st.Argv))
		case steward.StepKindWizard:
			fmt.Fprintf(&b, "     the Guildmaster runs on %s: %s\n", p.Machine, shellQuote(st.Argv))
		}
	}
	fmt.Fprintf(&b, "\nplan: %s\n", p.PlanDir)
	fmt.Fprintf(&b, "this plan needs the Guildmaster's approval of its exact digest: guildhall steward approval request --plan %s\n", shellQuote([]string{p.PlanDir}))
	fmt.Fprintf(&b, "apply with: guildhall steward %s --config %s --plan %s --apply --approve %s\n", p.Kind, shellQuote([]string{cfgPath}), shellQuote([]string{p.PlanDir}), p.Digest)
	return b.String()
}

// setupApprovalRequest opens, once, the approval decision of the setup plan in planDir on the reserved task
// steward/setup, bound to its digest, and prints it with the command the Guildmaster answers it with.
func (a *App) setupApprovalRequest(copyDir, storePath, planDir string) int {
	plan, err := steward.ReadSetupPlan(planDir)
	if err != nil {
		return a.fail(fmt.Errorf("steward approval request: %w", err))
	}
	if err := ensureStewardRecords(storePath); err != nil {
		return a.fail(fmt.Errorf("steward approval request: %w", err))
	}
	backup := "backs its store up with backup now"
	if !plan.Probe.Backup {
		backup = "exports its store on the machine, as it has no [backup] table"
	}
	id := setupDecisionID(plan.Digest)
	question := fmt.Sprintf("Approve %s plan %s of Director %s on %s at %s? It pauses the Director's session, waits for running attempts, %s, publishes a managed.toml with no managed key, enrols the copy with Steward %s, reloads and resumes it, and commits its roster entry. Plan: %s",
		plan.Kind, plan.Digest, plan.Director, plan.Machine, plan.CopyDir, backup, plan.Steward, plan.PlanDir)
	if plan.Kind == steward.SetupKindProvision {
		var fixes, wizards, written []string
		for _, st := range plan.Steps {
			switch st.Kind {
			case steward.StepKindFix:
				fixes = append(fixes, st.ID)
			case steward.StepKindWizard:
				wizards = append(wizards, st.ID)
			}
		}
		for _, sf := range plan.StepFiles {
			if !sf.Builtin {
				written = append(written, sf.Name+" (sha256 "+sf.SHA256+")")
			}
		}
		question = fmt.Sprintf("Approve %s plan %s of new Director %s on %s at %s? It fixes the doctor's gaps %s; waits for you to run the wizard on the machine for %s; runs the Steward-written step files %s; writes config.toml and a managed.toml with no managed key, makes the store, clones %s, adopts the release, enrols the copy with Steward %s, reloads it, starts its Herdr tab and commits its roster entry. Plan: %s",
			plan.Kind, plan.Digest, plan.Director, plan.Machine, plan.CopyDir, orNone(fixes), orNone(wizards), orNone(written),
			strings.Join(plan.Roster.Repositories, ", "), plan.Steward, plan.PlanDir)
	}
	if bs := plan.Bootstrap; bs != nil {
		question = fmt.Sprintf("Approve bootstrap plan %s of bare machine %s? It copies %s guildhall %s (%s, sha256 %s) to %s and the launcher to %s, checks each with %s before it first runs, and installs it with release install --from --sha256; nothing else. Plan: %s",
			plan.Digest, plan.Machine, bootstrapSource(plan), bs.Version, bs.Platform, plan.Files[0].SHA256, bs.Guildhall, bs.Launcher, steward.HashCommand(plan.Roster.OS, "")[0], plan.PlanDir)
	}
	d := store.Decision{Repository: stewardRepository, TaskID: stewardSetupTask, ID: id, Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: plan.Digest}, Options: []string{stewardApprove, stewardDecline},
		Question: truncateUTF8(question, store.MaxTextBytes)}
	out, err := withStore(storePath, false, func(s *store.Store) (store.Decision, error) {
		_, err := s.OpenDecision("steward-setup-approval:"+plan.Digest, d)
		if err != nil && !errors.Is(err, store.ErrExists) {
			return store.Decision{}, err
		}
		return findDecision(s, stewardRepository, stewardSetupTask, id)
	})
	if err != nil {
		return a.fail(fmt.Errorf("steward approval request: %w", err))
	}
	abs, _ := filepath.Abs(copyDir)
	return a.printJSON(struct {
		Decision store.Decision `json:"decision"`
		Answer   string         `json:"answer"`
	}{out, fmt.Sprintf("guildhall decide answer --copy %s --repo %s --task %s --decision %s --answer-file -", shellQuote([]string{abs}), stewardRepository, stewardSetupTask, id)})
}
