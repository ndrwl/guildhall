package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
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
	case !*dryRun && (*machine != "" || *director != "" || *copyDir != "" || *alias != ""):
		err = errors.New("--machine, --director, --copy and --ssh-alias belong to --dry-run; --apply and --abandon read them from the plan")
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
		p, err := s.PlanTakeover(ctx, steward.TakeoverInput{Machine: *machine, Director: *director, CopyDir: *copyDir, SSHAlias: *alias, PlanDir: *planDir})
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
		return a.setupOutcome(out, err)
	}
	// An agent in a session, the Steward's among them, never approves a setup plan itself: its --approve stands only
	// on the Guildmaster's recorded answer to the plan's approval decision.
	if *approve != "" && a.inSession() {
		storePath := filepath.Join(filepath.Dir(*cfgPath), stewardStoreFile)
		ok, err := stewardDecisionApproved(storePath, stewardSetupTask, setupDecisionID(*approve), *approve)
		if err != nil {
			return a.fail(fmt.Errorf("steward takeover: %w", err))
		}
		if !ok {
			return a.fail(fmt.Errorf("steward takeover: inside a Guildhall session --approve %s needs the Guildmaster's answer %q to decision %s/%s/%s in %s; open it with guildhall steward approval request --plan %s and ask the Guildmaster to answer it",
				*approve, stewardApprove, stewardRepository, stewardSetupTask, setupDecisionID(*approve), storePath, shellQuote([]string{*planDir})))
		}
	}
	out, err := s.ApplySetup(ctx, *planDir, *approve)
	if errors.Is(err, steward.ErrNotApproved) {
		if saved, rerr := steward.ReadSetupPlan(*planDir); rerr == nil {
			err = fmt.Errorf("%w; open its approval decision with guildhall steward approval request --plan %s, and once the Guildmaster approves, add --approve %s",
				err, shellQuote([]string{*planDir}), saved.Digest)
		}
	}
	return a.setupOutcome(out, err)
}

// setupOutcome prints each step's record and the outcome, and returns 0 only when the plan is done.
func (a *App) setupOutcome(out *steward.Outcome, err error) int {
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
		return a.fail(fmt.Errorf("steward takeover: %w", err))
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
		runner = steward.SSH{Path: ssh, Timeout: sshTimeout}
	}
	return &steward.Setup{Private: git.WorkTree{Program: git.Program{Path: gitPath, Timeout: scpTimeout}, Dir: private},
		Transport: copier, Runner: runner, Steward: cfg.Steward.Name, Now: a.now}, nil
}

// renderSetupPlan describes a dry-run takeover plan: the copy, the roster change and every step with its command.
func renderSetupPlan(p *steward.SetupPlan, cfgPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s plan %s\ndry run: nothing was changed\n\n", p.Kind, p.Digest)
	fmt.Fprintf(&b, "%s/%s (%s:%s): %s, session %s, enrolled with Steward %s once applied\n", p.Machine, p.Director, p.SSHAlias, p.CopyDir, p.Probe.Mode, p.Session(), p.Steward)
	fmt.Fprintf(&b, "roster.toml:\n")
	for line := range strings.Lines(p.RosterDiff) {
		b.WriteString("    " + line)
	}
	for _, f := range p.Files {
		fmt.Fprintf(&b, "file %s -> %s (sha256 %s)\n", f.Name, f.Target, f.SHA256)
	}
	b.WriteString("steps:\n")
	for i, st := range p.Steps {
		fmt.Fprintf(&b, "  %d. %s: %s\n", i+1, st.ID, st.Expect)
		switch st.Kind {
		case steward.StepKindRemote:
			fmt.Fprintf(&b, "     ssh -o BatchMode=yes -- %s %s\n", p.SSHAlias, shellQuote(st.Argv))
		case steward.StepKindCopy:
			fmt.Fprintf(&b, "     %s\n", shellQuote(st.Argv))
		}
	}
	fmt.Fprintf(&b, "\nplan: %s\n", p.PlanDir)
	fmt.Fprintf(&b, "this plan needs the Guildmaster's approval of its exact digest: guildhall steward approval request --plan %s\n", shellQuote([]string{p.PlanDir}))
	fmt.Fprintf(&b, "apply with: guildhall steward takeover --config %s --plan %s --apply --approve %s\n", shellQuote([]string{cfgPath}), shellQuote([]string{p.PlanDir}), p.Digest)
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
	d := store.Decision{Repository: stewardRepository, TaskID: stewardSetupTask, ID: id, Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: plan.Digest}, Options: []string{stewardApprove, stewardDecline},
		Question: truncateUTF8(fmt.Sprintf("Approve %s plan %s of Director %s on %s at %s? It pauses the Director's session, waits for running attempts, %s, publishes a managed.toml with no managed key, enrols the copy with Steward %s, reloads and resumes it, and commits its roster entry. Plan: %s",
			plan.Kind, plan.Digest, plan.Director, plan.Machine, plan.CopyDir, backup, plan.Steward, plan.PlanDir), store.MaxTextBytes)}
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
