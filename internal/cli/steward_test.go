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
task_prefix = "c"
path = '/srv/src/cli'
remote = "git@github.com:example/cli.git"
owner_machine = "workstation"
`
)

var (
	cliConfig  = steward.Remote{Alias: "guild-ws", Path: "/home/guild/tools/config.toml"}
	cliManaged = steward.Remote{Alias: "guild-ws", Path: "/home/guild/tools/managed.toml"}
)

// stewardApp is an App whose rollouts reach an in-memory fleet holding one Director copy, and whose faked git finds
// the Steward config's private config directory a Git work tree.
func stewardApp(t *testing.T, managed string) (app *App, stdout, stderr *bytes.Buffer, stewardConfig string, fleet *stewardtest.Fleet) {
	t.Helper()
	stewardConfig, private := writeStewardConfig(t, managed)
	fleet = stewardtest.NewFleet()
	fleet.Write(cliConfig, []byte(cliCopy))
	managedCopy, err := config.EncodeManaged(nil)
	if err != nil {
		t.Fatal(err)
	}
	fleet.Write(cliManaged, managedCopy)
	app, stdout, stderr = newTestApp(privateWorkTree(private))
	app.Copier, app.Runner = fleet, fleet
	return app, stdout, stderr, stewardConfig, fleet
}

// privateWorkTree is newTestApp's tools for a git on PATH that answers private is a Git work tree.
func privateWorkTree(private string) (map[string]string, map[string]result) {
	return map[string]string{"git": "git"}, map[string]result{"git -C " + private + " rev-parse --is-inside-work-tree": {out: "true\n"}}
}

// writeStewardConfig writes a private config directory with cliRoster and managed, and a Steward copy's config.toml
// naming it under a [steward] name, as the session admits it. It returns the config's path and the private directory.
func writeStewardConfig(t *testing.T, managed string) (stewardConfig, private string) {
	t.Helper()
	private = t.TempDir()
	for name, content := range map[string]string{"roster.toml": cliRoster, "managed.toml": managed} {
		if err := os.WriteFile(filepath.Join(private, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stewardConfig = filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(stewardConfig, []byte(stewardSessionConfig(private, "[steward]\nname = \"steward\"\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	return stewardConfig, private
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
		"ssh -o BatchMode=yes -- guild-ws guildhall config reload --copy /home/guild/tools\n",
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
	if ran := fleet.Ran("guild-ws"); len(ran) != 1 || strings.Join(ran[0], " ") != "guildhall config reload --copy /home/guild/tools" {
		t.Errorf("apply ran %q on the copy, want one config reload", ran)
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

// TestStewardApplyReportsAFailedReload: a copy whose guildhall does not know config reload is reported per copy,
// with the remote diagnostic, and apply exits 1 without undoing the published file.
func TestStewardApplyReportsAFailedReload(t *testing.T) {
	app, stdout, stderr, cfg, fleet := stewardApp(t, "schema_version = 1\n[managed.standards_revision]\nvalue = \"standards-2\"\n")
	const old = `guildhall: unknown command "config"`
	fleet.Answer("guild-ws", nil, old)
	plan := filepath.Join(t.TempDir(), "plan")
	args := []string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", plan}
	if code := app.Run(append(args, "--dry-run")); code != 0 {
		t.Fatalf("dry run exit %d; stderr: %s", code, stderr)
	}
	stdout.Reset()
	if code := app.Run(append(args, "--apply")); code != 1 || !strings.Contains(stderr.String(), "did not complete") {
		t.Fatalf("apply exit %d, want 1; stderr: %s", code, stderr)
	}
	if out := stdout.String(); !strings.Contains(out, "workstation/tools: reload_failed: ") || !strings.Contains(out, old) {
		t.Errorf("apply output does not report the failed reload:\n%s", out)
	}
	published, _ := fleet.Read(cliManaged)
	if !strings.Contains(string(published), "standards-2") {
		t.Error("a failed reload undid the published managed file")
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
	remoteConfig := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, remoteConfig, "schema_version = 1\nmode = \"steward\"\n[private_config]\nlocation = \"git@github.com:example/private.git\"\n[steward]\nname = \"steward\"\n")
	unnamed := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, unnamed, stewardSessionConfig(t.TempDir(), ""))
	inline := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, inline, stewardSessionConfig(t.TempDir(), "[steward]\nname = \"steward\"\n\n[managed]\n"))
	managedCopy, _ := writeStewardConfig(t, "schema_version = 1\n")
	writeFile(t, filepath.Join(filepath.Dir(managedCopy), "managed.toml"), "schema_version = 1\n")
	plan := filepath.Join(t.TempDir(), "plan")
	cases := map[string]struct {
		args []string
		want string
	}{
		"director config":    {[]string{"--config", director, "--machine", "workstation"}, "is a director config, not the steward's"},
		"no steward name":    {[]string{"--config", unnamed, "--machine", "workstation"}, "has no [steward] name"},
		"inline managed":     {[]string{"--config", inline, "--machine", "workstation"}, "inline [managed] settings: a Steward copy is never managed"},
		"managed copy":       {[]string{"--config", managedCopy, "--machine", "workstation"}, "a Steward copy is never managed"},
		"remote private ref": {[]string{"--config", remoteConfig, "--machine", "workstation"}, "must be an absolute local directory"},
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
	app.Copier, app.Runner = nil, nil
	app.LookPath = func(name string) (string, error) {
		if name == "git" {
			return name, nil
		}
		return "", exec.ErrNotFound
	}
	stderr.Reset()
	if code := app.Run([]string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", plan, "--dry-run"}); code != 1 || !strings.Contains(stderr.String(), "scp is needed for rollouts") {
		t.Fatalf("missing scp: exit %d; stderr: %s", code, stderr)
	}
	app.LookPath = func(name string) (string, error) {
		if name == "scp" || name == "git" {
			return name, nil
		}
		return "", exec.ErrNotFound
	}
	stderr.Reset()
	if code := app.Run([]string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", plan, "--dry-run"}); code != 1 || !strings.Contains(stderr.String(), "ssh is needed for rollouts") {
		t.Fatalf("missing ssh: exit %d; stderr: %s", code, stderr)
	}
}

// TestStewardRolloutRefusesAPrivateDirThatIsNotAWorkTree: rollout checks the private config checkout as the session
// does, so a plain directory that is not a Git work tree is refused before any copy is fetched.
func TestStewardRolloutRefusesAPrivateDirThatIsNotAWorkTree(t *testing.T) {
	cfg, private := writeStewardConfig(t, "schema_version = 1\n")
	paths, _ := privateWorkTree(private)
	app, _, stderr := newTestApp(paths, map[string]result{"git -C " + private + " rev-parse --is-inside-work-tree": {out: "false\n"}})
	fleet := stewardtest.NewFleet()
	app.Copier, app.Runner = fleet, fleet
	args := []string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", filepath.Join(t.TempDir(), "plan"), "--dry-run"}
	if code := app.Run(args); code != 1 || !strings.Contains(stderr.String(), "is not a Git work tree") {
		t.Fatalf("exit %d; stderr: %s", code, stderr)
	}
	if calls := fleet.Calls(); len(calls) != 0 {
		t.Errorf("a refused rollout reached the fleet: %q", calls)
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

func runStewardSCPStub() {
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
}

// TestStewardRolloutRunsSCPFromPath checks the real wiring: without a Copier, New's App
// finds scp on PATH and runs it, in a separate process, with the planned arguments.
func TestStewardRolloutRunsSCPFromPath(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is needed for the private config checkout: %v", err)
	}
	bin := t.TempDir()
	for _, name := range []string{"scp", "ssh"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		copyFile(t, self, filepath.Join(bin, name))
	}
	cfg, private := writeStewardConfig(t, "schema_version = 1\n")
	if out, err := exec.Command(git, "init", "-q", private).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	log := filepath.Join(t.TempDir(), "scp.log")
	// The real git checks the private config checkout. Its own directory goes on PATH after the stubs, so the stub
	// scp and ssh still come first; a symbolic link to git would not run on Windows, where Git for Windows finds its
	// installation from the executable's path.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+filepath.Dir(git))
	t.Setenv(scpStubEnv, log)

	app := New()
	var stdout, stderr bytes.Buffer
	app.Stdout, app.Stderr = &stdout, &stderr
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
