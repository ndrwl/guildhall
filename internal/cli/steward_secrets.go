package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/secrets"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
)

// stewardSecretsTask is the Steward store's reserved task of secrets plan approvals, one decision per plan
// (docs/secrets.md#the-steward-pushes-them); only the Guildmaster answers them.
const stewardSecretsTask = "secrets"

// secretsPushTimeout bounds one secrets set, plan or apply, its sops, ssh and scp runs, commit and push together.
const secretsPushTimeout = 30 * time.Minute

// maxSecretValue bounds a value read for steward secrets set.
const maxSecretValue = 64 << 10

const stewardSecretsUsage = `Usage:
  guildhall steward secrets set --config <steward.toml> <machine> <NAME>
  guildhall steward secrets plan --config <steward.toml> --machine <label> [--machine <label>...] --plan <dir>
  guildhall steward secrets apply --config <steward.toml> --machine <label> [--machine <label>...] --plan <dir> --approve <plan digest>

set reads NAME's value from the terminal, without echo, or from standard input, never from the command line,
encrypts it at once to the machine's age key and the recovery key, and commits and pushes the ciphertext to the
private config. plan writes a new --plan directory naming, for each machine, the names and digests its committed
ciphertext would push, and changes nothing; apply copies it to each machine once the Guildmaster approved the plan's
exact digest, reads it back and runs secrets check there.
`

// secretsDecisionID is the id of the approval decision of secrets plan digest d: "secrets-" and its first 12 hex digits.
func secretsDecisionID(d string) string {
	hex := strings.TrimPrefix(d, "sha256:")
	return "secrets-" + hex[:min(12, len(hex))]
}

// stewardSecretsCmd runs `guildhall steward secrets set|plan|apply`.
func (a *App) stewardSecretsCmd(args []string) int {
	usage := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: steward secrets: %v\n\n%s", err, stewardSecretsUsage)
		return 2
	}
	if len(args) == 0 || (args[0] != "set" && args[0] != "plan" && args[0] != "apply") {
		return usage(errors.New("the commands are set, plan and apply"))
	}
	sub := args[0]
	fs := flag.NewFlagSet("steward secrets "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var machines labels
	cfgPath := fs.String("config", "", "")
	planDir, approve := new(string), new(string)
	if sub != "set" {
		fs.StringVar(planDir, "plan", "", "")
		fs.Var(&machines, "machine", "")
	}
	if sub == "apply" {
		fs.StringVar(approve, "approve", "", "")
	}
	if err := parseInterspersed(fs, args[1:]); err != nil {
		return usage(err)
	}
	switch {
	case *cfgPath == "":
		return usage(errors.New("--config is required"))
	case sub == "set" && fs.NArg() > 2:
		return usage(errors.New("a value is never given on the command line, where it would show in the process list and shell history; type it when asked, or pipe it on standard input"))
	case sub == "set" && fs.NArg() != 2:
		return usage(errors.New("set takes the machine and the NAME"))
	case sub != "set" && fs.NArg() > 0:
		return usage(fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	case sub != "set" && (*planDir == "" || len(machines) == 0):
		return usage(errors.New("--plan and at least one --machine are required"))
	case sub == "apply" && *approve == "":
		return usage(errors.New("every secrets push needs --approve with the plan's digest, once the Guildmaster approved it"))
	}
	s, err := a.loadSecrets(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretsPushTimeout)
	defer cancel()
	switch sub {
	case "set":
		return a.stewardSecretsSet(ctx, s, fs.Arg(0), fs.Arg(1))
	case "plan":
		p, err := s.Plan(ctx, machines, *planDir)
		if err != nil {
			return a.fail(err)
		}
		if err := steward.WriteSecretsPlan(p); err != nil {
			return a.fail(err)
		}
		if _, err := io.WriteString(a.Stdout, renderSecretsPlan(p, *cfgPath)); err != nil {
			return 1
		}
		for _, ms := range p.Pushes {
			if ms.Status == steward.StatusRefused {
				return 1
			}
		}
		return 0
	}
	// An agent in a session, the Steward's among them, never approves a push itself: --approve stands only on the
	// Guildmaster's recorded answer to the plan's approval decision.
	if a.inSession() {
		storePath := filepath.Join(filepath.Dir(*cfgPath), stewardStoreFile)
		ok, err := stewardDecisionApproved(storePath, stewardSecretsTask, secretsDecisionID(*approve), *approve)
		if err != nil {
			return a.fail(fmt.Errorf("steward secrets apply: %w", err))
		}
		if !ok {
			return a.fail(fmt.Errorf("steward secrets apply: inside a Guildhall session --approve %s needs the Guildmaster's answer %q to decision %s/%s/%s in %s; open it with guildhall steward approval request --plan %s and ask the Guildmaster to answer it",
				*approve, stewardApprove, stewardRepository, stewardSecretsTask, secretsDecisionID(*approve), storePath, shellQuote([]string{*planDir})))
		}
	}
	receipt, err := s.Apply(ctx, *planDir, *approve, machines...)
	if errors.Is(err, steward.ErrApprovalRequired) {
		if saved, rerr := steward.ReadSecretsPlan(*planDir); rerr == nil {
			err = fmt.Errorf("%w; once the Guildmaster approves this exact plan, add --approve %s", err, saved.Digest)
		}
	}
	if receipt != nil {
		var b strings.Builder
		for _, m := range receipt.Machines {
			fmt.Fprintf(&b, "%s: %s (%s)", m.Machine, m.Status, strings.Join(m.Names, ", "))
			if m.Error != "" {
				fmt.Fprintf(&b, ": %s", m.Error)
			}
			b.WriteByte('\n')
		}
		if receipt.File != "" {
			fmt.Fprintf(&b, "receipt: %s\n", receipt.File)
		}
		if _, werr := io.WriteString(a.Stdout, b.String()); werr != nil {
			return 1
		}
	}
	if err != nil {
		return a.fail(err)
	}
	return 0
}

// stewardSecretsSet reads the value and sets it.
func (a *App) stewardSecretsSet(ctx context.Context, s *steward.Secrets, machine, name string) int {
	if !secrets.ValidName(name) {
		return a.fail(fmt.Errorf("steward secrets set: %q is not an environment variable name", name))
	}
	if _, ok := s.Roster.Machine(machine); !ok {
		return a.fail(fmt.Errorf("steward secrets set: machine %q is not in the roster", machine))
	}
	value, err := a.readSecretValue(machine, name)
	if err != nil {
		return a.fail(fmt.Errorf("steward secrets set: %w", err))
	}
	p, err := secrets.DefaultPaths()
	if err != nil {
		return a.fail(err)
	}
	res, err := s.Set(ctx, machine, name, value, p.RecoveryKey())
	if res != nil {
		_, _ = fmt.Fprintf(a.Stdout, "%s: %s set; %s holds %s (sha256 %s)\ncommit: %s\n", machine, name, res.File, strings.Join(res.Names, ", "), res.SHA256, res.Commit)
	}
	if err != nil {
		return a.fail(fmt.Errorf("steward secrets set: %w", err))
	}
	_, _ = fmt.Fprintf(a.Stdout, "push it to the machine with: guildhall steward secrets plan --config <steward.toml> --machine %s --plan <new dir>\n", machine)
	return 0
}

// readSecretValue reads one value from standard input: typed at a terminal after a prompt on standard error, with
// echo off, up to the end of the line, or else piped, all of it but one final line ending.
func (a *App) readSecretValue(machine, name string) (string, error) {
	in := a.Stdin
	if in == nil {
		return "", errors.New("no standard input to read the value from")
	}
	if f, ok := in.(*os.File); ok {
		terminal, restore, err := noEcho(f)
		if err != nil {
			return "", err
		}
		if terminal {
			defer restore()
			_, _ = fmt.Fprintf(a.Stderr, "value of %s for %s (not shown): ", name, machine)
			line, err := bufio.NewReader(io.LimitReader(f, maxSecretValue+1)).ReadString('\n')
			_, _ = fmt.Fprintln(a.Stderr)
			if err != nil && !errors.Is(err, io.EOF) {
				return "", err
			}
			return checkedValue(strings.TrimRight(line, "\r\n"))
		}
	}
	data, err := io.ReadAll(io.LimitReader(in, maxSecretValue+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxSecretValue {
		return "", fmt.Errorf("the value is longer than %d bytes", maxSecretValue)
	}
	s := string(data)
	if t, ok := strings.CutSuffix(s, "\n"); ok {
		s = strings.TrimSuffix(t, "\r")
	}
	return checkedValue(s)
}

func checkedValue(s string) (string, error) {
	if err := secrets.CheckValue(s); err != nil {
		return "", err
	}
	return s, nil
}

// loadSecrets is the secrets push over the Steward copy's private config checkout.
func (a *App) loadSecrets(cfgPath string) (*steward.Secrets, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cfgPath, err)
	}
	if cfg.Mode != config.ModeSteward {
		return nil, fmt.Errorf("%s is not a steward config", cfgPath)
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
			return nil, fmt.Errorf("scp is needed to push secrets files: %w", err)
		}
		copier = steward.Transport{Path: scp, Timeout: scpTimeout}
	}
	if runner == nil {
		ssh, err := a.LookPath("ssh")
		if err != nil {
			return nil, fmt.Errorf("ssh is needed to run secrets check on each machine: %w", err)
		}
		runner = steward.SSH{Path: ssh, Timeout: sshTimeout}
	}
	s, err := steward.LoadSecrets(git.WorkTree{Program: git.Program{Path: gitPath, Timeout: scpTimeout}, Dir: private}, copier, runner)
	if err != nil {
		return nil, err
	}
	s.Now = a.now
	return s, nil
}

// renderSecretsPlan describes a dry-run secrets plan: each machine's names, digests and commands, never a value.
func renderSecretsPlan(p *steward.SecretsPlan, cfgPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "secrets plan %s\ndry run: nothing was changed; the plan holds names and digests, never a value\n", p.Digest)
	for _, ms := range p.Pushes {
		fmt.Fprintf(&b, "\n%s (%s:%s): %s\n", ms.Machine, ms.SSHAlias, ms.Target, ms.Status)
		if ms.Problem != "" {
			fmt.Fprintf(&b, "  problem: %s\n", ms.Problem)
		}
		if len(ms.Names) > 0 {
			fmt.Fprintf(&b, "  names: %s\n", strings.Join(ms.Names, ", "))
		}
		if ms.AfterSHA256 != "" {
			before := ms.BeforeSHA256
			if before == "" {
				before = "(none)"
			}
			fmt.Fprintf(&b, "  sha256: %s -> %s\n", before, ms.AfterSHA256)
		}
		if len(ms.Commands) > 0 {
			b.WriteString("  commands:\n")
			for _, argv := range ms.Commands {
				b.WriteString("    " + shellQuote(argv) + "\n")
			}
		}
	}
	fmt.Fprintf(&b, "\nplan: %s\n", p.StageDir)
	apply := fmt.Sprintf("guildhall steward secrets apply --config %s", shellQuote([]string{cfgPath}))
	for _, m := range p.Machines {
		apply += " --machine " + m
	}
	apply += " --plan " + shellQuote([]string{p.StageDir}) + " --approve " + p.Digest
	b.WriteString("every secrets push needs Guildmaster approval of its exact digest\n")
	fmt.Fprintf(&b, "apply with: %s\n", apply)
	return b.String()
}

// secretsApprovalRequest opens, once, the approval decision of the secrets plan in planDir on the reserved task
// steward/secrets, bound to its digest, and prints it with the command the Guildmaster answers it with.
func (a *App) secretsApprovalRequest(copyDir, storePath, planDir string) int {
	plan, err := steward.ReadSecretsPlan(planDir)
	if err != nil {
		return a.fail(fmt.Errorf("steward approval request: %w", err))
	}
	if err := ensureStewardRecords(storePath); err != nil {
		return a.fail(fmt.Errorf("steward approval request: %w", err))
	}
	var pushes []string
	for _, ms := range plan.Pushes {
		if ms.Status == steward.StatusChange {
			pushes = append(pushes, fmt.Sprintf("%s gets %s", ms.Machine, strings.Join(ms.Names, ", ")))
		}
	}
	if len(pushes) == 0 {
		pushes = append(pushes, "no machine changes")
	}
	id := secretsDecisionID(plan.Digest)
	d := store.Decision{Repository: stewardRepository, TaskID: stewardSecretsTask, ID: id, Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: plan.Digest}, Options: []string{stewardApprove, stewardDecline},
		Question: truncateUTF8(fmt.Sprintf("Approve secrets plan %s to %s? It pushes each machine's committed ciphertext: %s. Plan: %s",
			plan.Digest, strings.Join(plan.Machines, ", "), strings.Join(pushes, "; "), plan.StageDir), store.MaxTextBytes)}
	out, err := withStore(storePath, false, func(s *store.Store) (store.Decision, error) {
		_, err := s.OpenDecision("steward-secrets-approval:"+plan.Digest, d)
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
