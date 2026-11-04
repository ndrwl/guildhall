package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/admit"
	"github.com/ndrwl/guildhall/internal/secrets"
)

// secretsTimeout bounds one run of sops or age-keygen.
const secretsTimeout = 30 * time.Second

// secretsCmd runs `guildhall secrets init` (docs/secrets.md#secrets-init).
func (a *App) secretsCmd(args []string) int {
	if len(args) != 1 || args[0] != "init" {
		return a.usageError("secrets: the one command is init, which takes no arguments")
	}
	p, err := secrets.DefaultPaths()
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretsTimeout)
	defer cancel()
	pub, created, err := secrets.Init(ctx, p, nil)
	if err != nil {
		return a.fail(err)
	}
	if created {
		_, _ = fmt.Fprintf(a.Stdout, "key: created %s, readable by its owner only\n", p.Key)
	} else {
		_, _ = fmt.Fprintf(a.Stdout, "key: kept %s, which already exists\n", p.Key)
	}
	_, _ = fmt.Fprintf(a.Stdout, "public key: %s\n", pub)
	_, _ = fmt.Fprintf(a.Stdout, "secrets file: %s, encrypted to the public key; director start and steward start decrypt it in memory\n", p.File)
	return 0
}

// secretsPaths are this machine's key and secrets file, refused when either is inside copyDir.
func (a *App) secretsPaths(copyDir string) (secrets.Paths, error) {
	p, err := secrets.DefaultPaths()
	if err != nil {
		return p, err
	}
	return p, p.Outside(copyDir)
}

// sessionSecrets is the start preflight's secrets check (docs/secrets.md#at-start): it decrypts this machine's
// secrets file in memory and prints one line naming how many names it holds, or that there are none. A key or file
// inside the copy, or a file that fails to decrypt, refuses the start, naming no value.
func (a *App) sessionSecrets(command, copyDir string) (secrets.Values, error) {
	p, err := a.secretsPaths(copyDir)
	if err != nil {
		return secrets.Values{}, a.refuseRoleCheck(command, "secrets", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretsTimeout)
	defer cancel()
	v, err := secrets.Load(ctx, p, nil)
	switch {
	case err != nil:
		return v, a.refuseRoleCheck(command, "secrets", err)
	case v.Len() == 0:
		a.preflightLine("secrets", "none injected (no machine key or no secrets file at %s)", p.File)
	default:
		a.preflightLine("secrets", "%d injected (%s), withheld from model-driven processes but a profile's inherit_env", v.Len(), strings.Join(v.Names(), ", "))
	}
	return v, nil
}

// injectSecrets puts v into the supervisor's own environment, so the gh and git it runs itself can use them, and
// lists their names, with any inherited list's, in secrets.NamesEnv, so every model-driven process's environment
// drops them (secrets.Strip).
func injectSecrets(v secrets.Values, getenv func(string) string, setenv func(string, string) error) error {
	if v.Len() == 0 {
		return nil
	}
	for _, n := range v.Names() {
		s, _ := v.Get(n)
		if err := setenv(n, s); err != nil {
			return fmt.Errorf("inject secret %s: %w", n, err)
		}
	}
	return setenv(secrets.NamesEnv, secrets.JoinNames(secrets.SplitNames(getenv(secrets.NamesEnv)), v.Names()))
}

// inheritedSecrets is the environment an owned Artisan of profile in copyDir inherits from this machine's secrets:
// the names its inherit_env lists that the secrets file holds, then secrets.RedactEnv naming them for the runtime
// wrapper to redact. A profile listing none decrypts nothing and inherits nothing.
func (a *App) inheritedSecrets(copyDir, profile string) ([]string, error) {
	if copyDir == "" || profile == "" {
		return nil, nil
	}
	cfg, _, err := admit.Admit(filepath.Join(copyDir, "config.toml"))
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", profile, err)
	}
	names := cfg.Profiles[profile].InheritEnv
	if len(names) == 0 {
		return nil, nil
	}
	p, err := a.secretsPaths(copyDir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretsTimeout)
	defer cancel()
	v, err := secrets.Load(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	picked := v.Pick(names)
	if picked.Len() == 0 {
		return nil, nil
	}
	return append(picked.Env(), secrets.RedactEnv+"="+secrets.JoinNames(picked.Names())), nil
}

// withInherited is cmd with env added to its environment after the adapter's own entries.
func withInherited(cmd adapter.Command, env []string) adapter.Command {
	if len(env) > 0 {
		cmd.Env = append(slices.Clone(cmd.Env), env...)
	}
	return cmd
}

// setenv is os.Setenv, the supervisor's own environment.
var setenv = os.Setenv
