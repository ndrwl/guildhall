//go:build unix

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/secrets"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/steward/stewardtest"
	"github.com/ndrwl/guildhall/internal/store"
)

// pushedSecret and pushedPassword are the values steward secrets set is given, which no commit, plan, receipt or
// output may hold in clear.
const (
	pushedSecret   = "sk-test-PUSHED-VALUE"
	pushedPassword = "db-PUSHED-password"
)

const (
	pushMachinePub  = "age1machine0000000000000000000000000000000000000000000000000"
	pushRecoveryPub = "age1recovery000000000000000000000000000000000000000000000000"
)

// pushSops is a fake sops that round-trips: --encrypt writes each value of the flat JSON on its standard input
// base64-encoded as ENC[...], with the age recipients it was given in sops metadata, and --decrypt reverses it when
// the key file's "# public key:" line is one of them. Values hold no '"', ',', '{' or '}'.
const pushSops = `case "$1" in
--encrypt)
  [ "$2 $4 $5 $6 $7 $8" = "--age --input-type json --output-type yaml /dev/stdin" ] || exit 64
  { tr -d '{}\n' | tr ',' '\n'; echo; } | while IFS= read -r kv; do
    [ -n "$kv" ] || continue
    name=${kv%%\":*}; name=${name#\"}
    val=${kv#*\":\"}; val=${val%\"}
    printf '%s: ENC[AES256_GCM,data:%s,type:str]\n' "$name" "$(printf %s "$val" | base64 | tr -d '\n')"
  done
  echo 'sops:'
  echo '    age:'
  for r in $(echo "$3" | tr ',' ' '); do
    echo "        - recipient: $r"
    echo '          enc: |'
    echo '            -----BEGIN AGE ENCRYPTED FILE-----'
  done
  ;;
--decrypt)
  [ "$2 $3" = "--output-type json" ] && [ -f "$SOPS_AGE_KEY_FILE" ] || exit 64
  pub=$(sed -n 's/^# public key: //p' "$SOPS_AGE_KEY_FILE")
  grep -q "recipient: $pub\$" "$4" || { echo 'Failed to get the data key required to decrypt the SOPS file.' >&2; exit 128; }
  printf '{'
  grep -E '^[A-Za-z_][A-Za-z0-9_]*: ENC\[' "$4" | {
    sep=''
    while IFS= read -r line; do
      name=${line%%:*}; data=${line#*data:}; data=${data%%,type*}
      printf '%s"%s":"%s"' "$sep" "$name" "$(printf %s "$data" | base64 -d)"
      sep=','
    done
  }
  printf '}\n'
  ;;
*) exit 64 ;;
esac`

// pushSopsRefuseNew, put before pushSops, makes a file a push dropped beside the secrets file fail to decrypt, as one
// encrypted to another key does, while PUSH_SOPS_REFUSE_NEW is set.
const pushSopsRefuseNew = `if [ -n "$PUSH_SOPS_REFUSE_NEW" ] && [ "$1" = --decrypt ]; then
  case "$4" in *.new-*) echo 'Failed to get the data key required to decrypt the SOPS file.' >&2; exit 128 ;; esac
fi
`

// pushSetup is a Steward copy whose private config is a clone of a bare remote, with the recovery key in this
// machine's secrets directory, and one Director machine reached through a Fleet whose secrets check runs this
// binary's App over the machine's own secrets directory, holding the file the Fleet was given.
type pushSetup struct {
	steward, private, remote, cfg, machineDir string
	fleet                                     *stewardtest.Fleet
	// copier, when set, is the Copier in the Fleet's place.
	copier steward.Copier
}

func newPushSetup(t *testing.T) *pushSetup {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, global, "")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	bin := t.TempDir()
	for name, body := range map[string]string{"sops": pushSopsRefuseNew + pushSops, "age-keygen": `[ "$1" = -y ] && sed -n 's/^# public key: //p' "$2"`} {
		writeFile(t, filepath.Join(bin, name), "#!/bin/sh\n"+body+"\n")
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ps := &pushSetup{steward: t.TempDir(), remote: t.TempDir(), machineDir: secrets.Dir(t.TempDir())}
	stewardSecrets := t.TempDir()
	t.Setenv(secrets.DirEnv, stewardSecrets)
	writeFile(t, filepath.Join(stewardSecrets, secrets.RecoveryKeyFile), "# public key: "+pushRecoveryPub+"\nAGE-SECRET-KEY-1RECOVERY\n")
	if err := os.MkdirAll(ps.machineDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ps.machineDir, secrets.KeyFile), "# public key: "+pushMachinePub+"\nAGE-SECRET-KEY-1MACHINE\n")

	roster := "schema_version = 1\nrecovery_recipient = \"" + pushRecoveryPub + "\"\n\n[[machine]]\nlabel = \"workstation\"\nssh_alias = \"guild-ws\"\nos = \"linux\"\nroles = [\"director\"]\nage_recipient = \"" + pushMachinePub + "\"\n\n[[machine.director]]\nname = \"alpha\"\ncopy_dir = \"/home/guild/directors/alpha\"\nrepositories = [\"app\"]\n"
	ps.private = filepath.Join(ps.steward, "private")
	collectGit(t, ps.remote, "init", "-q", "--bare", "-b", "main")
	collectGit(t, ps.steward, "clone", "-q", ps.remote, "private")
	collectGit(t, ps.private, "checkout", "-q", "-b", "main")
	writeFile(t, filepath.Join(ps.private, "roster.toml"), roster)
	collectGit(t, ps.private, "add", ".")
	collectGit(t, ps.private, "commit", "-q", "-m", "private config")
	collectGit(t, ps.private, "push", "-q", "-u", "origin", "main")
	ps.cfg = filepath.Join(ps.steward, "config.toml")
	writeFile(t, ps.cfg, stewardSessionConfig(ps.private, ""))
	s, err := store.Open(filepath.Join(ps.steward, stewardStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	ps.fleet = stewardtest.NewFleet()
	ps.fleet.Handle("guild-ws", func(argv []string) ([]byte, bool, error) {
		if argv[1] != "secrets" {
			return nil, false, nil
		}
		// The machine's secrets file is what the Fleet holds at its place in the account's home, and so is a file a
		// push dropped beside it for secrets install; what install leaves there goes back to the Fleet.
		file := filepath.Join(ps.machineDir, secrets.File)
		live := steward.Remote{Alias: "guild-ws", Path: steward.RemoteSecretsFile}
		if data, ok := ps.fleet.Read(live); ok {
			writeFile(t, file, string(data))
		}
		install := argv[2] == "install"
		if install {
			sum := argv[4]
			if data, ok := ps.fleet.Read(steward.Remote{Alias: "guild-ws", Path: secrets.NewFile(steward.RemoteSecretsFile, sum)}); ok {
				writeFile(t, secrets.NewFile(file, sum), string(data))
			}
		}
		t.Setenv(secrets.DirEnv, ps.machineDir)
		defer t.Setenv(secrets.DirEnv, stewardSecrets)
		app, stdout, stderr := newTestApp(nil, nil)
		code := app.Run(argv[1:])
		if install {
			if data, err := os.ReadFile(file); err == nil {
				ps.fleet.Write(live, data)
			}
		}
		if code != 0 {
			// As ssh reports a command that exits non-zero: its standard error, or else its standard output.
			msg := stderr.Bytes()
			if len(msg) == 0 {
				msg = stdout.Bytes()
			}
			return nil, true, fmt.Errorf("ssh: exit status %d: %s", code, bytes.TrimSpace(msg))
		}
		return stdout.Bytes(), true, nil
	})
	return ps
}

func (ps *pushSetup) run(t *testing.T, stdin string, args ...string) (int, string) {
	t.Helper()
	return ps.runIn(t, false, stdin, args...)
}

// runIn runs args as run does, inside a Guildhall session when inSession is set.
func (ps *pushSetup) runIn(t *testing.T, inSession bool, stdin string, args ...string) (int, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	if inSession {
		app.Getenv = func(name string) string {
			if name == session.NumberEnv {
				return "1"
			}
			return ""
		}
	}
	app.Copier, app.Runner = ps.fleet, ps.fleet
	if ps.copier != nil {
		app.Copier = ps.copier
	}
	app.LookPath = exec.LookPath
	app.Output = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, path, args...).Output()
	}
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(args)
	out := stdout.String() + stderr.String()
	if strings.Contains(out, pushedSecret) || strings.Contains(out, pushedPassword) {
		t.Fatalf("output holds a value:\n%s", out)
	}
	return code, out
}

// noValueIn fails the test when any file below dir holds a pushed value in clear.
func noValueIn(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(pushedSecret)) || bytes.Contains(data, []byte(pushedPassword)) {
			t.Errorf("%s holds a value in clear", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestStewardSecretsPush: secrets set refuses a value on the command line, encrypts one read from standard input to
// the machine's and recovery keys, commits and pushes only ciphertext and the generated .sops.yaml, and adds a second
// name by decrypting with the recovery key; plan names and digests the push and changes nothing, apply needs the
// plan's digest and then copies, reads back and checks the file on the machine, and a second plan is unchanged. No
// commit, plan, receipt or output holds a value.
func TestStewardSecretsPush(t *testing.T) {
	ps := newPushSetup(t)
	if code, out := ps.run(t, "", "steward", "secrets", "set", "--config", ps.cfg, "workstation", "API_KEY", pushedSecret); code != 2 || !strings.Contains(out, "never given on the command line") {
		t.Fatalf("a value in argv: exit %d, want 2:\n%s", code, out)
	}
	if code, out := ps.run(t, "", "steward", "secrets", "set", "--config", ps.cfg, "workstation", "API_KEY"); code != 1 || !strings.Contains(out, "empty") {
		t.Fatalf("no value: exit %d, want 1:\n%s", code, out)
	}
	if code, out := ps.run(t, pushedSecret+"\n", "steward", "secrets", "set", "--config", ps.cfg, "workstation", "API_KEY"); code != 0 || !strings.Contains(out, "holds API_KEY") {
		t.Fatalf("set API_KEY: exit %d:\n%s", code, out)
	}
	if code, out := ps.run(t, pushedPassword, "steward", "secrets", "set", "--config", ps.cfg, "workstation", "DB_PASS"); code != 0 || !strings.Contains(out, "holds API_KEY, DB_PASS") {
		t.Fatalf("set DB_PASS: exit %d:\n%s", code, out)
	}
	if got := collectGit(t, ps.remote, "log", "--format=%s", "main"); got != "Set secret DB_PASS for workstation\nSet secret API_KEY for workstation\nprivate config" {
		t.Fatalf("remote log:\n%s", got)
	}
	if got := collectGit(t, ps.private, "show", "--format=", "--name-only", "HEAD"); got != ".sops.yaml\nsecrets/workstation.sops.yaml" && got != "secrets/workstation.sops.yaml" {
		t.Fatalf("the commit changed:\n%s", got)
	}
	if got := collectGit(t, ps.private, "show", "HEAD:.sops.yaml"); !strings.Contains(got, "age: "+pushMachinePub+","+pushRecoveryPub) {
		t.Fatalf(".sops.yaml:\n%s", got)
	}
	noValueIn(t, ps.private)
	noValueIn(t, ps.remote)

	planDir := filepath.Join(ps.steward, "rollouts", "secrets-1")
	code, out := ps.run(t, "", "steward", "secrets", "plan", "--config", ps.cfg, "--machine", "workstation", "--plan", planDir)
	if code != 0 || !strings.Contains(out, "workstation (guild-ws:.config/guildhall/secrets.sops.yaml): change") || !strings.Contains(out, "names: API_KEY, DB_PASS") {
		t.Fatalf("plan: exit %d:\n%s", code, out)
	}
	if _, ok := ps.fleet.Read(steward.Remote{Alias: "guild-ws", Path: steward.RemoteSecretsFile}); ok {
		t.Fatal("the plan wrote the machine's secrets file")
	}
	plan, err := steward.ReadSecretsPlan(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := ps.run(t, "", "steward", "secrets", "apply", "--config", ps.cfg, "--machine", "workstation", "--plan", planDir); code != 2 || !strings.Contains(out, "--approve") {
		t.Fatalf("apply without --approve: exit %d, want 2:\n%s", code, out)
	}
	if code, out := ps.run(t, "", "steward", "secrets", "apply", "--config", ps.cfg, "--machine", "workstation", "--plan", planDir, "--approve", "sha256:0000"); code != 1 || !strings.Contains(out, plan.Digest) {
		t.Fatalf("apply with another digest: exit %d, want 1 naming the plan's digest:\n%s", code, out)
	}
	// Inside a session, --approve stands only on the Guildmaster's answer to the plan's decision on steward/secrets,
	// which a session cannot record.
	apply := []string{"steward", "secrets", "apply", "--config", ps.cfg, "--machine", "workstation", "--plan", planDir, "--approve", plan.Digest}
	if code, out := ps.runIn(t, true, "", apply...); code != 1 || !strings.Contains(out, "steward/secrets/"+secretsDecisionID(plan.Digest)) {
		t.Fatalf("apply in a session before the answer: exit %d:\n%s", code, out)
	}
	code, out = ps.run(t, "", "steward", "approval", "request", "--plan", planDir, "--copy", ps.steward)
	if code != 0 || !strings.Contains(out, secretsDecisionID(plan.Digest)) || !strings.Contains(out, "--task secrets") {
		t.Fatalf("approval request: exit %d:\n%s", code, out)
	}
	answer := []string{"decide", "answer", "--copy", ps.steward, "--repo", stewardRepository, "--task", stewardSecretsTask, "--decision", secretsDecisionID(plan.Digest), "--answer-file", "-"}
	if code, out := ps.runIn(t, true, "approve\n", answer...); code != 1 || !strings.Contains(out, "approves a secrets or retire plan") {
		t.Fatalf("answer in a session: exit %d, want it refused:\n%s", code, out)
	}
	if code, out := ps.run(t, "approve\n", answer...); code != 0 {
		t.Fatalf("answer: exit %d:\n%s", code, out)
	}
	code, out = ps.runIn(t, true, "", apply...)
	if code != 0 || !strings.Contains(out, "workstation: applied (API_KEY, DB_PASS)") {
		t.Fatalf("apply: exit %d:\n%s", code, out)
	}
	pushed, _ := ps.fleet.Read(steward.Remote{Alias: "guild-ws", Path: steward.RemoteSecretsFile})
	if committed := collectGit(t, ps.private, "show", "HEAD:secrets/workstation.sops.yaml"); strings.TrimSpace(string(pushed)) != committed {
		t.Fatalf("the machine holds\n%s\nnot the committed\n%s", pushed, committed)
	}
	receipts, _ := filepath.Glob(filepath.Join(planDir, "receipt-*.json"))
	if len(receipts) != 1 {
		t.Fatalf("receipts %q", receipts)
	}
	var receipt steward.SecretsReceipt
	if data, err := os.ReadFile(receipts[0]); err != nil || json.Unmarshal(data, &receipt) != nil {
		t.Fatalf("receipt: %v", err)
	}
	// Four steps: the put beside the file, its read-back, secrets install and secrets check.
	m := receipt.Machines[0]
	if m.Status != steward.StatusApplied || m.VerifiedSHA256 != m.AfterSHA256 || len(m.Steps) != 4 ||
		!strings.Contains(strings.Join(m.Steps[0].Command, " "), "guild-ws:.config/guildhall/secrets.sops.yaml.new-"+m.AfterSHA256) ||
		!strings.Contains(m.Steps[2].Output, `"installed":true`) || !strings.Contains(m.Steps[3].Output, `"decrypts":true`) {
		t.Fatalf("receipt %+v", m)
	}
	// A first push keeps no previous file and leaves no dropped one.
	if left, _ := filepath.Glob(filepath.Join(ps.machineDir, secrets.File+".*")); len(left) != 0 {
		t.Fatalf("the machine holds %q beside its secrets file", left)
	}
	noValueIn(t, planDir)

	again := filepath.Join(ps.steward, "rollouts", "secrets-2")
	if code, out := ps.run(t, "", "steward", "secrets", "plan", "--config", ps.cfg, "--machine", "workstation", "--plan", again); code != 0 || !strings.Contains(out, ": unchanged") {
		t.Fatalf("second plan: exit %d, want unchanged:\n%s", code, out)
	}
}

// pushed sets name on the workstation, then plans and applies its push outside a session, and returns the plan.
func (ps *pushSetup) pushed(t *testing.T, name, value, planDir string) (*steward.SecretsPlan, int, string) {
	t.Helper()
	if code, out := ps.run(t, value, "steward", "secrets", "set", "--config", ps.cfg, "workstation", name); code != 0 {
		t.Fatalf("set %s: exit %d:\n%s", name, code, out)
	}
	if code, out := ps.run(t, "", "steward", "secrets", "plan", "--config", ps.cfg, "--machine", "workstation", "--plan", planDir); code != 0 {
		t.Fatalf("plan: exit %d:\n%s", code, out)
	}
	plan, err := steward.ReadSecretsPlan(planDir)
	if err != nil {
		t.Fatal(err)
	}
	code, out := ps.run(t, "", "steward", "secrets", "apply", "--config", ps.cfg, "--machine", "workstation", "--plan", planDir, "--approve", plan.Digest)
	return plan, code, out
}

// receiptOf is the one receipt in planDir.
func receiptOf(t *testing.T, planDir string) steward.SecretsReceipt {
	t.Helper()
	receipts, _ := filepath.Glob(filepath.Join(planDir, "receipt-*.json"))
	if len(receipts) != 1 {
		t.Fatalf("receipts %q", receipts)
	}
	var receipt steward.SecretsReceipt
	if data, err := os.ReadFile(receipts[0]); err != nil || json.Unmarshal(data, &receipt) != nil {
		t.Fatalf("receipt: %v", err)
	}
	return receipt
}

// halfPut is a Copier whose Put writes half the file to the machine and fails, as a cut-short scp does.
type halfPut struct{ *stewardtest.Fleet }

func (h halfPut) Put(_ context.Context, src string, dst steward.Remote) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	h.Write(dst, data[:len(data)/2])
	return errors.New("scp: connection lost")
}

// TestStewardSecretsPushCutShortKeepsTheMachineFile: a put cut short halfway leaves the machine's secrets file
// byte-identical to what it held, and the receipt names the failed put.
func TestStewardSecretsPushCutShortKeepsTheMachineFile(t *testing.T) {
	ps := newPushSetup(t)
	if _, code, out := ps.pushed(t, "API_KEY", pushedSecret, filepath.Join(ps.steward, "rollouts", "first")); code != 0 {
		t.Fatalf("first push: exit %d:\n%s", code, out)
	}
	live := steward.Remote{Alias: "guild-ws", Path: steward.RemoteSecretsFile}
	before, _ := ps.fleet.Read(live)
	ps.copier = halfPut{ps.fleet}
	planDir := filepath.Join(ps.steward, "rollouts", "cut")
	plan, code, out := ps.pushed(t, "DB_PASS", pushedPassword, planDir)
	if code != 1 || !strings.Contains(out, "workstation: failed") {
		t.Fatalf("cut-short push: exit %d, want 1 naming the failure:\n%s", code, out)
	}
	if after, _ := ps.fleet.Read(live); !bytes.Equal(after, before) {
		t.Fatalf("the machine's secrets file changed:\n%s\nwas\n%s", after, before)
	}
	if dropped, ok := ps.fleet.Read(steward.Remote{Alias: "guild-ws", Path: secrets.NewFile(steward.RemoteSecretsFile, plan.Pushes[0].AfterSHA256)}); !ok || len(dropped) == 0 {
		t.Fatal("the cut-short put did not land beside the secrets file")
	}
	m := receiptOf(t, planDir).Machines[0]
	if m.Status != steward.StatusFailed || len(m.Steps) != 1 || !strings.Contains(m.Steps[0].Error, "connection lost") {
		t.Fatalf("receipt %+v", m)
	}
	noValueIn(t, planDir)
}

// TestStewardSecretsPushThatDoesNotDecryptKeepsTheMachineFile: a pushed file the machine's key cannot decrypt is
// refused by secrets install before it is renamed, so the machine's secrets file is byte-identical, the dropped file
// is gone, and the receipt names the install step.
func TestStewardSecretsPushThatDoesNotDecryptKeepsTheMachineFile(t *testing.T) {
	ps := newPushSetup(t)
	if _, code, out := ps.pushed(t, "API_KEY", pushedSecret, filepath.Join(ps.steward, "rollouts", "first")); code != 0 {
		t.Fatalf("first push: exit %d:\n%s", code, out)
	}
	file := filepath.Join(ps.machineDir, secrets.File)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PUSH_SOPS_REFUSE_NEW", "1")
	planDir := filepath.Join(ps.steward, "rollouts", "undecryptable")
	_, code, out := ps.pushed(t, "DB_PASS", pushedPassword, planDir)
	if code != 1 || !strings.Contains(out, "workstation: "+steward.StatusInstallFailed) {
		t.Fatalf("undecryptable push: exit %d, want 1 naming install_failed:\n%s", code, out)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(after, before) {
		t.Fatalf("the machine's secrets file changed:\n%s\nwas\n%s", after, before)
	}
	if after, _ := ps.fleet.Read(steward.Remote{Alias: "guild-ws", Path: steward.RemoteSecretsFile}); !bytes.Equal(after, before) {
		t.Fatal("the machine's secrets file, as the Fleet holds it, changed")
	}
	// The first push kept no .prev, and the refused install writes none and removes the file it refused.
	if left, _ := filepath.Glob(filepath.Join(ps.machineDir, secrets.File+".*")); len(left) != 0 {
		t.Fatalf("beside the secrets file: %q, want nothing", left)
	}
	m := receiptOf(t, planDir).Machines[0]
	if m.Status != steward.StatusInstallFailed || len(m.Steps) != 3 || !slices.Contains(m.Steps[2].Command, "install") ||
		!strings.Contains(m.Steps[2].Error, `"step":"decrypt"`) || !strings.Contains(m.Error, "does not decrypt") {
		t.Fatalf("receipt %+v", m)
	}
	noValueIn(t, planDir)
}

// TestStewardSecretsPushKeepsThePrevious: a second push installs the new ciphertext and keeps the one it replaced as
// .prev, with no dropped file left.
func TestStewardSecretsPushKeepsThePrevious(t *testing.T) {
	ps := newPushSetup(t)
	if _, code, out := ps.pushed(t, "API_KEY", pushedSecret, filepath.Join(ps.steward, "rollouts", "first")); code != 0 {
		t.Fatalf("first push: exit %d:\n%s", code, out)
	}
	file := filepath.Join(ps.machineDir, secrets.File)
	first, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	planDir := filepath.Join(ps.steward, "rollouts", "second")
	if _, code, out := ps.pushed(t, "DB_PASS", pushedPassword, planDir); code != 0 || !strings.Contains(out, "workstation: applied (API_KEY, DB_PASS)") {
		t.Fatalf("second push: exit %d:\n%s", code, out)
	}
	if prev, _ := os.ReadFile(file + secrets.PrevSuffix); !bytes.Equal(prev, first) {
		t.Fatalf(".prev holds\n%s\nnot the first push's\n%s", prev, first)
	}
	if committed := collectGit(t, ps.private, "show", "HEAD:secrets/workstation.sops.yaml"); func() string { b, _ := os.ReadFile(file); return strings.TrimSpace(string(b)) }() != committed {
		t.Fatal("the machine's secrets file is not the committed one")
	}
	if left, _ := filepath.Glob(filepath.Join(ps.machineDir, secrets.File+secrets.NewInfix+"*")); len(left) != 0 {
		t.Fatalf("dropped files left: %q", left)
	}
}

// TestStewardSecretsPlanRefusesAnOlderGuildhall: a machine whose secrets check report has no installs field, as a
// guildhall that predates secrets install prints, is refused at plan time.
func TestStewardSecretsPlanRefusesAnOlderGuildhall(t *testing.T) {
	ps := newPushSetup(t)
	if code, out := ps.run(t, pushedSecret, "steward", "secrets", "set", "--config", ps.cfg, "workstation", "API_KEY"); code != 0 {
		t.Fatalf("set: exit %d:\n%s", code, out)
	}
	ps.fleet.Handle("guild-ws", func([]string) ([]byte, bool, error) {
		r := secrets.Report{Key: "/home/guild/.config/guildhall/age.key", KeyExists: true, Recipient: pushMachinePub, File: "/home/guild/" + steward.RemoteSecretsFile, Names: []string{}}
		data, err := json.Marshal(r)
		// Drop the field an older guildhall does not print.
		return bytes.Replace(data, []byte(`,"installs":false`), nil, 1), true, err
	})
	planDir := filepath.Join(ps.steward, "rollouts", "older")
	if code, out := ps.run(t, "", "steward", "secrets", "plan", "--config", ps.cfg, "--machine", "workstation", "--plan", planDir); code != 1 || !strings.Contains(out, "upgrade guildhall on the machine") {
		t.Fatalf("an older guildhall: exit %d, want 1:\n%s", code, out)
	}
}

// TestStewardSecretsPlanRefuses: a machine whose key is not the roster's age_recipient, or with nothing committed, is
// refused and the plan cannot be applied.
func TestStewardSecretsPlanRefuses(t *testing.T) {
	ps := newPushSetup(t)
	planDir := filepath.Join(ps.steward, "rollouts", "none")
	if code, out := ps.run(t, "", "steward", "secrets", "plan", "--config", ps.cfg, "--machine", "workstation", "--plan", planDir); code != 1 || !strings.Contains(out, "steward secrets set") {
		t.Fatalf("nothing committed: exit %d, want 1:\n%s", code, out)
	}
	if code, out := ps.run(t, pushedSecret, "steward", "secrets", "set", "--config", ps.cfg, "workstation", "API_KEY"); code != 0 {
		t.Fatalf("set: exit %d:\n%s", code, out)
	}
	writeFile(t, filepath.Join(ps.machineDir, secrets.KeyFile), "# public key: age1another\nAGE-SECRET-KEY-1OTHER\n")
	planDir = filepath.Join(ps.steward, "rollouts", "rekeyed")
	code, out := ps.run(t, "", "steward", "secrets", "plan", "--config", ps.cfg, "--machine", "workstation", "--plan", planDir)
	if code != 1 || !strings.Contains(out, "not the roster's age_recipient") {
		t.Fatalf("another key: exit %d, want 1:\n%s", code, out)
	}
	plan, err := steward.ReadSecretsPlan(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := ps.run(t, "", "steward", "secrets", "apply", "--config", ps.cfg, "--machine", "workstation", "--plan", planDir, "--approve", plan.Digest); code != 1 || !strings.Contains(out, "refused") {
		t.Fatalf("apply of a refused plan: exit %d, want 1:\n%s", code, out)
	}
	if _, ok := ps.fleet.Read(steward.Remote{Alias: "guild-ws", Path: steward.RemoteSecretsFile}); ok {
		t.Fatal("a refused plan wrote the machine's secrets file")
	}
}
