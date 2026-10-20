package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/steward/stewardtest"
)

const (
	cliRoster = `schema_version = 1

[[machine]]
label = "workstation"
ssh_alias = "guild-ws"
os = "linux"
roles = ["director"]

[[machine.director]]
name = "tools"
copy_dir = "/home/guild/tools"
repositories = ["cli"]
`
	cliCopy = `schema_version = 1
mode = "director"
name = "tools"

[local]
notes_dir = "notes"

[[repository]]
id = "cli"
path = '/srv/src/cli'
remote = "git@github.com:example/cli.git"
owner_machine = "workstation"
`
)

var (
	cliConfig  = steward.Remote{Alias: "guild-ws", Path: "/home/guild/tools/config.toml"}
	cliManaged = steward.Remote{Alias: "guild-ws", Path: "/home/guild/tools/managed.toml"}
)

// stewardApp is an App whose rollouts reach an in-memory fleet holding one Director copy.
func stewardApp(t *testing.T, managed string) (app *App, stdout, stderr *bytes.Buffer, stewardConfig string, fleet *stewardtest.Fleet) {
	t.Helper()
	stewardConfig = writeStewardConfig(t, managed)
	fleet = stewardtest.NewFleet()
	fleet.Write(cliConfig, []byte(cliCopy))
	managedCopy, err := config.EncodeManaged(nil)
	if err != nil {
		t.Fatal(err)
	}
	fleet.Write(cliManaged, managedCopy)
	app, stdout, stderr = newTestApp(nil, nil)
	app.Copier = fleet
	return app, stdout, stderr, stewardConfig, fleet
}

// writeStewardConfig writes a private config directory with cliRoster and managed, and a Steward config naming it.
func writeStewardConfig(t *testing.T, managed string) string {
	t.Helper()
	private := t.TempDir()
	for name, content := range map[string]string{"roster.toml": cliRoster, "managed.toml": managed} {
		if err := os.WriteFile(filepath.Join(private, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stewardConfig := filepath.Join(t.TempDir(), "steward.toml")
	if err := os.WriteFile(stewardConfig, []byte("schema_version = 1\nmode = \"steward\"\n[private_config]\nlocation = '"+private+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return stewardConfig
}

func TestStewardRolloutDryRunThenApply(t *testing.T) {
	app, stdout, stderr, cfg, fleet := stewardApp(t, "schema_version = 1\n[managed.standards_revision]\nvalue = \"standards-2\"\n")
	plan := filepath.Join(t.TempDir(), "plan")
	args := []string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", plan}

	if code := app.Run(append(args, "--dry-run")); code != 0 {
		t.Fatalf("dry run exit %d; stderr: %s", code, stderr)
	}
	out := stdout.String()
	for _, want := range []string{
		"rollout plan sha256:",
		"dry run: nothing was changed",
		"workstation/tools (guild-ws:/home/guild/tools/managed.toml): change",
		`added standards_revision = "standards-2"`,
		"+[managed.standards_revision]",
		"scp -q -B -o BatchMode=yes -- " + filepath.Join(plan, "files", "workstation", "tools", "managed.toml") + " " + cliManaged.String(),
		"--apply",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out)
		}
	}
	for _, call := range fleet.Calls() {
		if !strings.HasPrefix(call, "fetch ") {
			t.Errorf("dry run made %s", call)
		}
	}

	stdout.Reset()
	if code := app.Run(append(args, "--apply")); code != 0 {
		t.Fatalf("apply exit %d; stderr: %s", code, stderr)
	}
	if out := stdout.String(); !strings.Contains(out, "workstation/tools: applied") || !strings.Contains(out, "receipt: "+plan) {
		t.Errorf("apply output:\n%s", out)
	}
	local, _ := fleet.Read(cliConfig)
	if string(local) != cliCopy {
		t.Fatal("local config changed")
	}
	published, _ := fleet.Read(cliManaged)
	combined, err := config.ParseCombined(local, published)
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := combined.Setting("standards_revision"); value != "standards-2" {
		t.Fatal(value)
	}
}

func TestStewardApplyRefusesDifferentMachinesBeforeTransport(t *testing.T) {
	app, _, stderr, cfg, fleet := stewardApp(t, "schema_version = 1\n")
	plan := filepath.Join(t.TempDir(), "plan")
	if code := app.Run([]string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", plan, "--dry-run"}); code != 0 {
		t.Fatal(stderr.String())
	}
	fleet.Calls()
	if code := app.Run([]string{"steward", "rollout", "--config", cfg, "--machine", "laptop", "--plan", plan, "--apply"}); code != 1 || !strings.Contains(stderr.String(), "requested machines differ") {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if calls := fleet.Calls(); len(calls) != 0 {
		t.Fatalf("refused apply made %q", calls)
	}
}

func TestStewardRolloutNeedsApprovalForPolicyChanges(t *testing.T) {
	app, stdout, stderr, cfg, _ := stewardApp(t, "schema_version = 1\n[managed.review_rule]\nvalue = \"two reviewers\"\npolicy = true\n")
	plan := filepath.Join(t.TempDir(), "plan")
	args := []string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", plan}
	if code := app.Run(append(args, "--dry-run")); code != 0 {
		t.Fatalf("dry run exit %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout.String(), "needs Guildmaster approval: the key is marked policy") {
		t.Errorf("dry run does not flag the policy change:\n%s", stdout)
	}
	digest := regexp.MustCompile(`rollout plan (sha256:[0-9a-f]{64})`).FindStringSubmatch(stdout.String())
	if digest == nil {
		t.Fatalf("no digest in:\n%s", stdout)
	}
	stderr.Reset()
	if code := app.Run(append(args, "--apply")); code != 1 || !strings.Contains(stderr.String(), "--approve "+digest[1]) {
		t.Fatalf("unapproved apply exit %d; stderr: %s", code, stderr)
	}
	if code := app.Run(append(args, "--apply", "--approve", digest[1])); code != 0 {
		t.Fatalf("approved apply exit %d; stderr: %s", code, stderr)
	}
}

func TestStewardRolloutUsageErrors(t *testing.T) {
	app, _, _, cfg, _ := stewardApp(t, "schema_version = 1\n")
	plan := filepath.Join(t.TempDir(), "plan")
	base := []string{"steward", "rollout", "--config", cfg, "--plan", plan, "--machine", "workstation"}
	cases := map[string][]string{
		"no subcommand":       {"steward"},
		"unknown subcommand":  {"steward", "install"},
		"neither mode":        base,
		"both modes":          append(append([]string{}, base...), "--dry-run", "--apply"),
		"approve on dry run":  append(append([]string{}, base...), "--dry-run", "--approve", "sha256:x"),
		"missing config":      {"steward", "rollout", "--plan", plan, "--machine", "workstation", "--dry-run"},
		"missing plan":        {"steward", "rollout", "--config", cfg, "--machine", "workstation", "--dry-run"},
		"missing machine":     {"steward", "rollout", "--config", cfg, "--plan", plan, "--dry-run"},
		"positional argument": append(append([]string{}, base...), "--dry-run", "extra"),
		"unknown flag":        append(append([]string{}, base...), "--dry-run", "--force"),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if code := app.Run(args); code != 2 {
				t.Fatalf("exit %d, want 2", code)
			}
		})
	}
}

func TestStewardRolloutRefusals(t *testing.T) {
	app, _, stderr, cfg, _ := stewardApp(t, "schema_version = 1\n")
	director := filepath.Join(t.TempDir(), "director.toml")
	if err := os.WriteFile(director, []byte(strings.Replace(cliCopy, "/srv/src/cli", filepath.ToSlash(t.TempDir()), 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteConfig := filepath.Join(t.TempDir(), "steward.toml")
	if err := os.WriteFile(remoteConfig, []byte("schema_version = 1\nmode = \"steward\"\n[private_config]\nlocation = \"git@github.com:example/private.git\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(t.TempDir(), "plan")
	cases := map[string]struct {
		args []string
		want string
	}{
		"director config":    {[]string{"--config", director, "--machine", "workstation"}, "is not a steward config"},
		"remote private ref": {[]string{"--config", remoteConfig, "--machine", "workstation"}, "must be a local directory"},
		"unenrolled machine": {[]string{"--config", cfg, "--machine", "stranger"}, `machine "stranger" is not enrolled`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			stderr.Reset()
			args := append([]string{"steward", "rollout", "--plan", plan, "--dry-run"}, c.args...)
			if code := app.Run(args); code != 1 || !strings.Contains(stderr.String(), c.want) {
				t.Fatalf("exit %d; stderr: %s", code, stderr)
			}
		})
	}
	app.Copier = nil
	app.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	stderr.Reset()
	if code := app.Run([]string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", plan, "--dry-run"}); code != 1 || !strings.Contains(stderr.String(), "scp") {
		t.Fatalf("missing scp: exit %d; stderr: %s", code, stderr)
	}
}

func TestStewardDryRunReportsUnreachableCopy(t *testing.T) {
	app, stdout, stderr, cfg, fleet := stewardApp(t, "schema_version = 1\n")
	fleet.Refuse("guild-ws")
	plan := filepath.Join(t.TempDir(), "plan")
	if code := app.Run([]string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", plan, "--dry-run"}); code != 1 {
		t.Fatalf("dry run exit %d, want 1; stderr: %s", code, stderr)
	}
	for _, want := range []string{"workstation/tools (guild-ws:/home/guild/tools/managed.toml): refused", "problem: fleet: guild-ws: connection refused"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("dry run output lacks %q:\n%s", want, stdout)
		}
	}
}

// scpStubEnv makes the test binary act as scp, appending its arguments to the named file and refusing the copy.
const scpStubEnv = "GUILDHALL_TEST_SCP_LOG"

func TestMain(m *testing.M) {
	if log := os.Getenv(scpStubEnv); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "stub scp:", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "stub scp: connection refused")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// TestStewardRolloutRunsSCPFromPath checks the real wiring: without a Copier, New's App
// finds scp on PATH and runs it, in a separate process, with the planned arguments.
func TestStewardRolloutRunsSCPFromPath(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	name := "scp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	copyFile(t, self, filepath.Join(bin, name))
	log := filepath.Join(t.TempDir(), "scp.log")
	t.Setenv("PATH", bin)
	t.Setenv(scpStubEnv, log)

	app := New()
	var stdout, stderr bytes.Buffer
	app.Stdout, app.Stderr = &stdout, &stderr
	cfg := writeStewardConfig(t, "schema_version = 1\n")
	if code := app.Run([]string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", filepath.Join(t.TempDir(), "plan"), "--dry-run"}); code != 1 {
		t.Fatalf("dry run exit %d, want 1; stderr: %s", code, &stderr)
	}
	if want := "problem: scp: exit status 1: stub scp: connection refused"; !strings.Contains(stdout.String(), want) {
		t.Errorf("dry run output lacks %q:\n%s", want, &stdout)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "-q -B -o BatchMode=yes -- " + cliConfig.String() + " "; !strings.HasPrefix(string(data), want) || strings.Count(string(data), "\n") != 1 {
		t.Errorf("scp ran with %q, want one run starting %q", data, want)
	}
}
