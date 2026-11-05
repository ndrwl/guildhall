package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
)

const stewardRetireUsage = `Usage:
  guildhall steward machine retire --config <steward.toml> --plan <new dir> <machine>
  guildhall steward machine retire --config <steward.toml> --plan <dir> --approve <plan digest> <machine>
  guildhall steward machine retire --config <steward.toml> --status <machine>
  guildhall steward machine retire --config <steward.toml> --rotated <NAME> <machine>

Without --approve, retire writes a plan to a new --plan directory and changes nothing: the machine's age_recipient
and secrets file to remove, every name in that file with the machines that must get a new value, and the
credentials to revoke at their providers. With --approve, once the Guildmaster approved the plan's exact digest, it
removes the recipient and the file and records the retirement in the private config, open until each listed value
is rotated: a name another machine holds once guildhall steward secrets set gives it a new value there, and a name
no other machine holds once the Guildmaster marks it with --rotated, which is refused inside a Guildhall session.
--status prints that record.
`

// retireDecisionID is the id of the approval decision of retire plan digest d: "retire-" and its first 12 hex digits.
func retireDecisionID(d string) string {
	return "retire-" + strings.TrimPrefix(secretsDecisionID(d), "secrets-")
}

// stewardMachineCmd runs `guildhall steward machine retire`.
func (a *App) stewardMachineCmd(args []string) int {
	usage := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: steward machine: %v\n\n%s", err, stewardRetireUsage)
		return 2
	}
	if len(args) == 0 || args[0] != "retire" {
		return usage(errors.New("the command is retire"))
	}
	fs := flag.NewFlagSet("steward machine retire", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", "", "")
	planDir := fs.String("plan", "", "")
	approve := fs.String("approve", "", "")
	status := fs.Bool("status", false, "")
	rotated := fs.String("rotated", "", "")
	if err := parseInterspersed(fs, args[1:]); err != nil {
		return usage(err)
	}
	switch {
	case *cfgPath == "":
		return usage(errors.New("--config is required"))
	case fs.NArg() != 1:
		return usage(errors.New("name the one machine to retire"))
	case *status && *rotated != "":
		return usage(errors.New("--status and --rotated are separate commands"))
	case (*status || *rotated != "") && (*planDir != "" || *approve != ""):
		return usage(errors.New("--status and --rotated take no --plan or --approve"))
	case !*status && *rotated == "" && *planDir == "":
		return usage(errors.New("--plan is required"))
	}
	machine := fs.Arg(0)
	// Only the Guildmaster, who rotated the value at its provider, can say so: an agent in a session never marks it.
	if *rotated != "" && a.inSession() {
		return a.fail(fmt.Errorf("steward machine retire: --rotated records the Guildmaster's word that %s was rotated at its provider, so it is refused inside a Guildhall session; ask the Guildmaster to run it in their own terminal", *rotated))
	}
	s, err := a.loadSecrets(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretsPushTimeout)
	defer cancel()
	if *rotated != "" {
		r, err := s.MarkRotated(ctx, machine, *rotated)
		if r != nil {
			if code := a.write(fmt.Sprintf("marked %s rotated for the retirement of %s in commit %s\n", *rotated, machine, r.Commit) + renderRetirement(r)); code != 0 {
				return code
			}
		}
		if err != nil {
			return a.fail(fmt.Errorf("steward machine retire: %w", err))
		}
		return 0
	}
	if *status {
		r, ok, err := s.Retirement(ctx, machine)
		switch {
		case err != nil:
			return a.fail(err)
		case !ok:
			return a.fail(fmt.Errorf("machine %q has no retirement in the private config", machine))
		}
		return a.write(renderRetirement(r))
	}
	if *approve == "" {
		p, err := s.RetirePlan(ctx, machine, *planDir)
		if err != nil {
			return a.fail(err)
		}
		if err := steward.WriteRetirePlan(p); err != nil {
			return a.fail(err)
		}
		return a.write(renderRetirePlan(p, *cfgPath))
	}
	// An agent in a session never approves a retirement itself: --approve stands only on the Guildmaster's recorded
	// answer to the plan's approval decision.
	if a.inSession() {
		storePath := filepath.Join(filepath.Dir(*cfgPath), stewardStoreFile)
		ok, err := stewardDecisionApproved(storePath, stewardSecretsTask, retireDecisionID(*approve), *approve)
		if err != nil {
			return a.fail(fmt.Errorf("steward machine retire: %w", err))
		}
		if !ok {
			return a.fail(fmt.Errorf("steward machine retire: inside a Guildhall session --approve %s needs the Guildmaster's answer %q to decision %s/%s/%s in %s; open it with guildhall steward approval request --plan %s and ask the Guildmaster to answer it",
				*approve, stewardApprove, stewardRepository, stewardSecretsTask, retireDecisionID(*approve), storePath, shellQuote([]string{*planDir})))
		}
	}
	r, err := s.Retire(ctx, *planDir, *approve, machine)
	if errors.Is(err, steward.ErrApprovalRequired) {
		if saved, rerr := steward.ReadRetirePlan(*planDir); rerr == nil {
			err = fmt.Errorf("%w; once the Guildmaster approves this exact plan, add --approve %s", err, saved.Digest)
		}
	}
	if r != nil {
		out := fmt.Sprintf("retired %s in commit %s\n", machine, r.Commit)
		for _, o := range r.Closed {
			out += fmt.Sprintf("closed %s's rotations on %s, now retired too\n", o, machine)
		}
		if code := a.write(out + renderRetirement(r)); code != 0 {
			return code
		}
	}
	if err != nil {
		return a.fail(fmt.Errorf("steward machine retire: %w", err))
	}
	return 0
}

func (a *App) write(s string) int {
	if _, err := io.WriteString(a.Stdout, s); err != nil {
		return 1
	}
	return 0
}

// renderRetirePlan describes a dry-run retirement: what it removes, what to rotate and what to revoke, never a value.
func renderRetirePlan(p *steward.RetirePlan, cfgPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "retire plan %s for %s (%s)\ndry run: nothing was changed; the plan holds names and digests, never a value\n\n", p.Digest, p.Machine, p.SSHAlias)
	if p.Recipient != "" {
		fmt.Fprintf(&b, "remove its age_recipient %s from roster.toml and .sops.yaml\n", p.Recipient)
	} else {
		b.WriteString("it has no age_recipient to remove\n")
	}
	if p.FileSHA256 != "" {
		fmt.Fprintf(&b, "remove %s (sha256 %s), holding %s\n", p.File, p.FileSHA256, strings.Join(p.Names, ", "))
	} else {
		fmt.Fprintf(&b, "it has no %s to remove\n", p.File)
	}
	writeRotations(&b, p.Machine, p.Names, p.Rotations, p.Revoke)
	fmt.Fprintf(&b, "\nplan: %s\n", p.StageDir)
	b.WriteString("every retirement needs Guildmaster approval of its exact digest\n")
	fmt.Fprintf(&b, "apply with: guildhall steward machine retire --config %s --plan %s --approve %s %s\n",
		shellQuote([]string{cfgPath}), shellQuote([]string{p.StageDir}), p.Digest, p.Machine)
	return b.String()
}

// writeRotations prints the manual steps: rotate each name the machine could read at its provider, then set it anew
// on each machine still holding it or, when none does, mark it rotated; and revoke the machine's own credentials.
func writeRotations(b *strings.Builder, machine string, names []string, rotations []steward.Rotation, revoke []string) {
	if len(names) > 0 {
		b.WriteString("\nrotate at its provider every value the machine could read, as it keeps its key and old ciphertext:\n")
		for _, n := range names {
			var on []string
			unheld := ""
			for _, x := range rotations {
				if x.Name != n {
					continue
				}
				state := "waiting"
				if x.ClosedAt != nil {
					state = x.ClosedBy + " " + x.ClosedAt.Format("2006-01-02")
				}
				if x.Machine == "" {
					unheld = state
					continue
				}
				on = append(on, fmt.Sprintf("%s (%s)", x.Machine, state))
			}
			switch {
			case len(on) > 0:
				fmt.Fprintf(b, "  - %s: rotate it, then set the new value with guildhall steward secrets set on %s\n", n, strings.Join(on, ", "))
			case unheld != "":
				fmt.Fprintf(b, "  - %s: no other machine holds it; rotate or revoke it, then the Guildmaster marks it with guildhall steward machine retire --rotated %s %s (%s)\n", n, n, machine, unheld)
			}
		}
	}
	b.WriteString("\nrevoke the machine's own credentials at their providers:\n")
	for _, r := range revoke {
		fmt.Fprintf(b, "  - %s\n", r)
	}
}

// renderRetirement describes a retirement record: its state, each rotation's and the revocation steps.
func renderRetirement(r *steward.RetireReceipt) string {
	var b strings.Builder
	fmt.Fprintf(&b, "retirement of %s (plan %s) on %s: %s", r.Machine, r.PlanDigest, r.RetiredAt.Format("2006-01-02"), r.State)
	if pending := r.Pending(); len(pending) > 0 {
		fmt.Fprintf(&b, ", %d of %d rotations waiting", len(pending), len(r.Rotations))
	}
	b.WriteByte('\n')
	writeRotations(&b, r.Machine, r.Names, r.Rotations, r.Revoke)
	return b.String()
}

// retireApprovalRequest opens, once, the approval decision of the retire plan in planDir on the reserved task
// steward/secrets, bound to its digest, and prints it with the command the Guildmaster answers it with.
func (a *App) retireApprovalRequest(copyDir, storePath, planDir string) int {
	plan, err := steward.ReadRetirePlan(planDir)
	if err != nil {
		return a.fail(fmt.Errorf("steward approval request: %w", err))
	}
	if err := ensureStewardRecords(storePath); err != nil {
		return a.fail(fmt.Errorf("steward approval request: %w", err))
	}
	names := "none"
	if len(plan.Names) > 0 {
		names = strings.Join(plan.Names, ", ")
	}
	id := retireDecisionID(plan.Digest)
	d := store.Decision{Repository: stewardRepository, TaskID: stewardSecretsTask, ID: id, Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: plan.Digest}, Options: []string{stewardApprove, stewardDecline},
		Question: truncateUTF8(fmt.Sprintf("Approve retire plan %s for machine %s? It removes its age_recipient and %s, and stays open until every value it could read (%s) is rotated: set anew on the machines that hold it, or marked rotated by the Guildmaster when none does. Plan: %s",
			plan.Digest, plan.Machine, plan.File, names, plan.StageDir), store.MaxTextBytes)}
	out, err := withStore(storePath, false, func(s *store.Store) (store.Decision, error) {
		_, err := s.OpenDecision("steward-retire-approval:"+plan.Digest, d)
		if err != nil && !errors.Is(err, store.ErrExists) {
			return store.Decision{}, err
		}
		return findDecision(s, stewardRepository, stewardSecretsTask, id)
	})
	if err != nil {
		return a.fail(fmt.Errorf("steward approval request: %w", err))
	}
	abs, _ := filepath.Abs(copyDir)
	return a.printJSON(struct {
		Decision store.Decision `json:"decision"`
		Answer   string         `json:"answer"`
	}{out, fmt.Sprintf("guildhall decide answer --copy %s --repo %s --task %s --decision %s --answer-file -", shellQuote([]string{abs}), stewardRepository, stewardSecretsTask, id)})
}
