//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// LiveSSHEnv set to 1 runs TestLiveSSH; CI's "Live SSH" step sets it on ubuntu (docs/decisions/ci-live-ssh.md).
const LiveSSHEnv = "GUILDHALL_LIVE_SSH"

// liveSSHD is the absolute path the throwaway sshd runs from: sshd re-executes itself and refuses a relative path.
const liveSSHD = "/usr/sbin/sshd"

// liveAlias is the roster's ssh_alias, the Host alias the throwaway ssh_config defines; liveHostKeyAlias is the name
// known_hosts pins the throwaway host key under.
const (
	liveAlias        = "guild-ws"
	liveHostKeyAlias = "guildhall-live-sshd"
)

// restrictedPath is the roster's restricted path form that copy_dir and guildhall_path must match.
var restrictedPath = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)

// liveSSH is one TestLiveSSH run: a throwaway sshd on loopback, the ssh and scp wrappers that log their argv and
// run the real tools with the throwaway ssh_config, the built binary on both ends, a Director copy, a Steward copy and
// its private config checkout cloned from a bare remote.
type liveSSH struct {
	t                                  *testing.T
	root, bin, path, wrapLog, sshCfg   string
	ssh                                string
	director, steward, private, remote string
	env                                []string
	logged                             int
}

// TestLiveSSH drives the Steward's takeover, collect and rollout through real ssh, scp and an unprivileged sshd on
// 127.0.0.1, with the built binary as the remote guildhall (H118 of d246). It runs only with GUILDHALL_LIVE_SSH=1,
// and then fails rather than skips when a tool is missing; it never reads or writes ~/.ssh, the Guildhall home or
// the secrets directory.
func TestLiveSSH(t *testing.T) {
	if os.Getenv(LiveSSHEnv) != "1" {
		t.Skipf("set %s=1 to run the Steward over real OpenSSH on localhost (docs/decisions/ci-live-ssh.md)", LiveSSHEnv)
	}
	l := newLiveSSH(t)

	cfg := filepath.Join(l.steward, "config.toml")
	planDir := filepath.Join(l.steward, "rollouts", "takeover-alpha")
	var plan string
	t.Run("takeover dry run", func(t *testing.T) {
		dry := []string{"steward", "takeover", "--config", cfg, "--machine", "workstation", "--director", "alpha", "--copy", l.director, "--plan", planDir, "--dry-run"}
		out := l.run(t, false, "", dry...)
		m := regexp.MustCompile(`^takeover plan (sha256:[0-9a-f]{64})\n`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("dry run printed:\n%s", out)
		}
		plan = m[1]
		if _, err := os.Stat(filepath.Join(l.director, "managed.toml")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the dry run wrote managed.toml: %v", err)
		}
		l.wantCalls(t, "ssh", "scp")
		again := l.run(t, false, "", dry...)
		if !strings.HasPrefix(again, "takeover plan "+plan+"\n") {
			t.Errorf("second dry run gave another plan:\n%s", again)
		}
		l.wantCalls(t, "ssh", "scp")
	})
	if plan == "" {
		t.FailNow()
	}

	t.Run("takeover approval", func(t *testing.T) {
		out := l.run(t, true, "", "steward", "approval", "request", "--plan", planDir, "--copy", l.steward)
		var req struct {
			Decision struct {
				Repository string `json:"repository"`
				TaskID     string `json:"task_id"`
				ID         string `json:"id"`
			} `json:"decision"`
		}
		if err := json.Unmarshal([]byte(out), &req); err != nil || req.Decision.ID == "" {
			t.Fatalf("approval request printed %s: %v", out, err)
		}
		l.run(t, false, "approve\n", "decide", "answer", "--copy", l.steward, "--repo", req.Decision.Repository, "--task", req.Decision.TaskID,
			"--decision", req.Decision.ID, "--answer-file", "-")
	})

	t.Run("takeover apply", func(t *testing.T) {
		out := l.run(t, true, "", "steward", "takeover", "--config", cfg, "--plan", planDir, "--apply", "--approve", plan)
		if !strings.HasSuffix(out, "\ndone\n") {
			t.Fatalf("apply printed:\n%s", out)
		}
		l.wantCalls(t, "ssh", "scp")
		mode := l.mode(t)
		if mode.Mode != "managed" || mode.Steward != "steward" || !mode.Authorized {
			t.Errorf("director mode after the takeover: %+v", mode)
		}
		roster := l.git(t, l.private, "show", "HEAD:roster.toml")
		if !strings.Contains(roster, "name = \"alpha\"\ncopy_dir = \""+l.director+"\"") {
			t.Errorf("committed roster:\n%s", roster)
		}
		if head, main := l.git(t, l.private, "rev-parse", "HEAD"), l.git(t, l.remote, "rev-parse", "main"); head != main {
			t.Errorf("the roster commit %s is not pushed; the remote's main is %s", head, main)
		}
	})
	if t.Failed() {
		t.FailNow()
	}

	t.Run("collect", func(t *testing.T) {
		lesson := "# Rebase before review\n\n## Evidence\n\nd12 failed twice.\n\n## Limits\n\nLong-lived tasks only.\n"
		l.run(t, false, lesson, "lesson", "propose", "--id", "rebase", "-", "--copy", l.director)
		out := l.run(t, false, "", "steward", "collect", "--copy", l.steward)
		var round struct {
			Commit string `json:"commit"`
		}
		if err := json.Unmarshal([]byte(out), &round); err != nil || round.Commit == "" {
			t.Fatalf("collect printed %s: %v", out, err)
		}
		l.wantCalls(t, "ssh", "scp")
		if main := l.git(t, l.remote, "rev-parse", "main"); round.Commit != main {
			t.Errorf("round commit %s is not the remote's main %s", round.Commit, main)
		}
		files := l.git(t, l.private, "ls-tree", "-r", "--name-only", round.Commit, "lessons/workstation/")
		if !strings.Contains(files, "lessons/workstation/") || !strings.Contains(files, "rebase") {
			t.Errorf("the round commit holds no lesson under lessons/workstation/: %q", files)
		}
		if pending := l.run(t, false, "", "lesson", "list", "--json", "--pending", "--copy", l.director); strings.Contains(pending, `"id"`) {
			t.Errorf("lessons still pending after the collect: %s", pending)
		}
	})

	t.Run("rollout", func(t *testing.T) {
		before := l.mode(t)
		l.writeFile(t, filepath.Join(l.private, "managed.toml"), "schema_version = 1\n\n[managed.standards_revision]\nvalue = \"standards-2\"\n")
		l.git(t, l.private, "commit", "-q", "-am", "standards-2")
		l.git(t, l.private, "push", "-q")
		rolloutPlan := filepath.Join(l.steward, "rollouts", "standards-2")
		args := []string{"steward", "rollout", "--config", cfg, "--machine", "workstation", "--plan", rolloutPlan}
		out := l.run(t, false, "", append(args, "--dry-run")...)
		if want := "workstation/alpha (" + liveAlias + ":" + l.director + "/managed.toml): change"; !strings.Contains(out, want) {
			t.Fatalf("dry run lacks %q:\n%s", want, out)
		}
		l.wantCalls(t, "scp")
		out = l.run(t, false, "", append(args, "--apply")...)
		if !strings.Contains(out, "workstation/alpha: applied") {
			t.Fatalf("apply printed:\n%s", out)
		}
		l.wantCalls(t, "ssh", "scp")
		receipts, _ := filepath.Glob(filepath.Join(rolloutPlan, "receipt-*.json"))
		if len(receipts) != 1 {
			t.Fatalf("receipts %q, want one", receipts)
		}
		var receipt struct {
			Copies []struct {
				Director, Status string
			} `json:"copies"`
		}
		b, err := os.ReadFile(receipts[0])
		if err != nil || json.Unmarshal(b, &receipt) != nil || len(receipt.Copies) != 1 || receipt.Copies[0].Director != "alpha" || receipt.Copies[0].Status != "applied" {
			t.Errorf("receipt %s (%v):\n%s", receipts[0], err, b)
		}
		published, err := os.ReadFile(filepath.Join(l.director, "managed.toml"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(published)
		after := l.mode(t)
		if want := "sha256:" + hex.EncodeToString(sum[:]); after.ManagedDigest != want || after.ManagedDigest == before.ManagedDigest || !after.Authorized || after.Mode != "managed" {
			t.Errorf("director mode after the rollout: %+v, want %s authorized (before %s)", after, want, before.ManagedDigest)
		}
	})
}

// newLiveSSH builds the run's tools, identity, sshd and copies, or fails the test: with the gate set, nothing is
// skipped.
func newLiveSSH(t *testing.T) *liveSSH {
	t.Helper()
	tools := map[string]string{}
	for _, name := range []string{"ssh", "scp", "ssh-keygen", "git"} {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("%s=1 needs %s: %v", LiveSSHEnv, name, err)
		}
		tools[name] = p
	}
	if _, err := os.Stat(liveSSHD); err != nil {
		t.Fatalf("%s=1 needs %s: %v", LiveSSHEnv, liveSSHD, err)
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	path := sessionPath(t)
	l := &liveSSH{t: t, root: t.TempDir(), path: path, ssh: tools["ssh"],
		bin: filepath.Join(strings.SplitN(path, string(os.PathListSeparator), 2)[0], "guildhall")}
	for _, p := range []string{l.root, l.bin} {
		if !restrictedPath.MatchString(p) {
			t.Fatalf("%s is outside the roster's restricted path characters; set TMPDIR to a plain path", p)
		}
	}

	// The remote end's and every local command's Guildhall home, secrets directory and git config.
	home, secretsDir, gitDir := filepath.Join(l.root, "home"), filepath.Join(l.root, "secrets"), filepath.Join(l.root, "git")
	for _, d := range []string{home, secretsDir, gitDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	gitConfig := filepath.Join(gitDir, "config")
	l.writeFile(t, gitConfig, "[user]\n\tname = Live SSH\n\temail = live-ssh@example.com\n[commit]\n\tgpgsign = false\n[init]\n\tdefaultBranch = main\n")
	remoteEnv := []string{"GUILDHALL_HOME=" + home, "GUILDHALL_SECRETS_DIR=" + secretsDir, "GIT_CONFIG_GLOBAL=" + gitConfig, "GIT_CONFIG_NOSYSTEM=1"}

	// The throwaway identity: a client key, a host key pinned under a fixed alias, and the two configs.
	keys := filepath.Join(l.root, "ssh")
	if err := os.Mkdir(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	clientKey, hostKey := filepath.Join(keys, "client_ed25519"), filepath.Join(keys, "host_ed25519")
	for _, k := range []string{clientKey, hostKey} {
		if out, err := exec.Command(tools["ssh-keygen"], "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", k).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen %s: %v\n%s", k, err, out)
		}
	}
	clientPub, hostPub := l.readFile(t, clientKey+".pub"), l.readFile(t, hostKey+".pub")
	authorized, knownHosts := filepath.Join(keys, "authorized_keys"), filepath.Join(keys, "known_hosts")
	l.writeFile(t, authorized, clientPub)
	l.writeFile(t, knownHosts, liveHostKeyAlias+" "+hostPub)
	sshdCfg := filepath.Join(keys, "sshd_config")
	l.sshCfg = filepath.Join(keys, "ssh_config")
	port := l.startSSHD(t, sshdCfg, func(port int) (string, string) {
		sshd := strings.Join([]string{
			"Port " + strconv.Itoa(port),
			"ListenAddress 127.0.0.1",
			"HostKey " + hostKey,
			"AuthorizedKeysFile " + authorized,
			"PidFile " + filepath.Join(keys, "sshd.pid"),
			"UsePAM no",
			"StrictModes no",
			"PasswordAuthentication no",
			"KbdInteractiveAuthentication no",
			"Subsystem sftp internal-sftp",
			// Remote sessions get no environment of the test's; these keep them off the runner's real home.
			"SetEnv " + strings.Join(remoteEnv, " "),
		}, "\n") + "\n"
		client := strings.Join([]string{
			"Host " + liveAlias,
			"  HostName 127.0.0.1",
			"  Port " + strconv.Itoa(port),
			"  User " + me.Username,
			"  IdentityFile " + clientKey,
			"  IdentitiesOnly yes",
			"  IdentityAgent none",
			"  UserKnownHostsFile " + knownHosts,
			"  HostKeyAlias " + liveHostKeyAlias,
			"  StrictHostKeyChecking yes",
		}, "\n") + "\n"
		return sshd, client
	})
	t.Logf("sshd on 127.0.0.1:%d", port)

	// ssh and scp go first on PATH: each logs its argv and runs the real tool with the throwaway config, so
	// Guildhall's own arguments reach OpenSSH unchanged.
	wrappers := filepath.Join(l.root, "wrap")
	if err := os.Mkdir(wrappers, 0o700); err != nil {
		t.Fatal(err)
	}
	l.wrapLog = filepath.Join(l.root, "wrap.log")
	for _, name := range []string{"ssh", "scp"} {
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"%s $*\" >> '%s'\nexec '%s' -F '%s' \"$@\"\n", name, l.wrapLog, tools[name], l.sshCfg)
		if err := os.WriteFile(filepath.Join(wrappers, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	l.env = append(os.Environ(), append(remoteEnv, "PATH="+wrappers+string(os.PathListSeparator)+path, "GUILDHALL_ATTEMPT=")...)

	// The Director copy: the run fixture's config with no local standards_revision, which the rollout's managed key
	// would conflict with, pointed at a real repository, its store and its approved workflow.
	l.director = filepath.Join(l.root, "alpha")
	if err := os.CopyFS(l.director, os.DirFS(filepath.Join("..", "..", "internal", "cli", "testdata", "run"))); err != nil {
		t.Fatal(err)
	}
	repository, remote, _ := gittest.Repository(t)
	b := l.readFile(t, filepath.Join(l.director, "config.toml"))
	for old, repl := range map[string]string{
		`path = "/srv/app"`:                         "path = '" + repository + "'",
		`remote = "git@github.com:example/app.git"`: "remote = '" + remote + "'",
		"standards_revision = \"standards-1\"\n":    "",
	} {
		if !strings.Contains(b, old) {
			t.Fatalf("the run fixture's config lacks %q", old)
		}
		b = strings.Replace(b, old, repl, 1)
	}
	l.writeFile(t, filepath.Join(l.director, "config.toml"), b)
	if err := os.MkdirAll(filepath.Join(l.director, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	l.run(t, false, "", "store", "init", "--copy", l.director)
	l.run(t, false, "Approved for the live SSH test.\n", "workflow", "approve", "--copy", l.director, "--repo", "app", "--words-file", "-")

	// The Steward copy, and its private config checkout cloned from a bare remote, whose roster lists the machine,
	// reached as liveAlias and running the built binary, but not the copy.
	l.steward, l.remote = filepath.Join(l.root, "steward"), filepath.Join(l.root, "private.git")
	l.private = filepath.Join(l.steward, "private")
	if err := os.Mkdir(l.steward, 0o700); err != nil {
		t.Fatal(err)
	}
	l.git(t, l.root, "init", "-q", "--bare", "-b", "main", l.remote)
	l.git(t, l.steward, "clone", "-q", l.remote, "private")
	l.git(t, l.private, "checkout", "-q", "-B", "main")
	l.writeFile(t, filepath.Join(l.private, "roster.toml"), fmt.Sprintf("schema_version = 1\n\n[[machine]]\nlabel = \"workstation\"\nssh_alias = %q\nos = \"linux\"\nroles = [\"steward\", \"director\"]\nguildhall_path = %q\n", liveAlias, l.bin))
	l.writeFile(t, filepath.Join(l.private, "managed.toml"), "schema_version = 1\n")
	l.git(t, l.private, "add", ".")
	l.git(t, l.private, "commit", "-q", "-m", "private config")
	l.git(t, l.private, "push", "-q", "-u", "origin", "main")
	l.writeFile(t, filepath.Join(l.steward, "config.toml"), "schema_version = 1\nmode = \"steward\"\n\n[private_config]\nlocation = '"+l.private+"'\n\n[steward]\nname = \"steward\"\nadapter = \"fake\"\n")
	l.run(t, false, "", "store", "init", filepath.Join(l.steward, "steward.store"))
	l.wantCalls(t)
	return l
}

// startSSHD writes the configs render gives for a free port, checks sshd's with sshd -t, and starts sshd -D in its own
// process group until a TCP dial and an ssh through the throwaway config both succeed; another process taking the
// port first makes it try a new one. Cleanup kills the group. It returns the port.
func (l *liveSSH) startSSHD(t *testing.T, sshdCfg string, render func(port int) (sshd, client string)) int {
	t.Helper()
	for try := 1; ; try++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
		sshd, client := render(port)
		l.writeFile(t, sshdCfg, sshd)
		l.writeFile(t, l.sshCfg, client)
		if out, err := exec.Command(liveSSHD, "-t", "-f", sshdCfg).CombinedOutput(); err != nil {
			t.Fatalf("sshd -t -f %s: %v\n%s\n%s", sshdCfg, err, out, sshd)
		}
		logPath := filepath.Join(filepath.Dir(sshdCfg), fmt.Sprintf("sshd-%d.log", try))
		logFile, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(liveSSHD, "-D", "-e", "-f", sshdCfg)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			_ = logFile.Close()
			t.Fatalf("start sshd: %v", err)
		}
		exited := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			_ = logFile.Close()
			close(exited)
		}()
		t.Cleanup(func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-exited
			if t.Failed() {
				t.Logf("sshd log %s:\n%s", logPath, l.readFile(t, logPath))
			}
		})
		var gone bool
		testenv.WaitFor(t, "sshd to accept a login on 127.0.0.1:"+strconv.Itoa(port), func() bool {
			select {
			case <-exited:
				gone = true
				return true
			default:
			}
			c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				return false
			}
			_ = c.Close()
			return exec.Command(l.ssh, "-F", l.sshCfg, "-o", "BatchMode=yes", "--", liveAlias, "true").Run() == nil
		})
		if !gone {
			return port
		}
		if log := l.readFile(t, logPath); try >= 3 || !strings.Contains(log, "Address already in use") {
			t.Fatalf("sshd exited before it accepted a login:\n%s", log)
		}
	}
}

// run runs the built binary locally, outside any copy, in a session when inSession is set, and fails t unless it
// exits 0; it returns stdout.
func (l *liveSSH) run(t *testing.T, inSession bool, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(l.bin, args...)
	cmd.Dir = t.TempDir()
	session := ""
	if inSession {
		session = "1"
	}
	cmd.Env = append(append([]string(nil), l.env...), "GUILDHALL_SESSION="+session)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("guildhall %s: %v\nstdout %s\nstderr %s\nssh and scp ran:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String(), strings.Join(l.calls(), "\n"))
	}
	return stdout.String()
}

type liveMode struct {
	Mode          string `json:"mode"`
	Steward       string `json:"steward"`
	ManagedDigest string `json:"managed_digest"`
	Authorized    bool   `json:"authorized"`
}

// mode is director mode --json of the Director copy.
func (l *liveSSH) mode(t *testing.T) liveMode {
	t.Helper()
	out := l.run(t, false, "", "director", "mode", "--json", "--copy", l.director)
	var m liveMode
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("director mode --json printed %s: %v", out, err)
	}
	return m
}

// calls are the wrapper log's lines: one "<tool> <argv>" per ssh or scp Guildhall ran.
func (l *liveSSH) calls() []string {
	b, err := os.ReadFile(l.wrapLog)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// wantCalls checks the ssh and scp runs logged since the last check: exactly the tools named ran, each at least once,
// and each with Guildhall's own argument list, so a regression back to a stub or an added option cannot pass.
func (l *liveSSH) wantCalls(t *testing.T, tools ...string) {
	t.Helper()
	all := l.calls()
	calls := all[min(l.logged, len(all)):]
	l.logged = len(all)
	seen := map[string]bool{}
	sshPrefix := "ssh -o BatchMode=yes -- " + liveAlias + " " + l.bin + " "
	scpPrefix := "scp -q -B -o BatchMode=yes -- "
	for _, c := range calls {
		switch {
		case strings.HasPrefix(c, sshPrefix):
			seen["ssh"] = true
		case strings.HasPrefix(c, scpPrefix) && strings.Contains(c, liveAlias+":"+l.director+"/"):
			seen["scp"] = true
		default:
			t.Errorf("unexpected ssh or scp run: %s", c)
		}
	}
	for _, tool := range tools {
		if !seen[tool] {
			t.Errorf("no real %s ran; logged:\n%s", tool, strings.Join(calls, "\n"))
		}
		delete(seen, tool)
	}
	for tool := range seen {
		t.Errorf("%s ran where none was expected; logged:\n%s", tool, strings.Join(calls, "\n"))
	}
	t.Logf("real ssh and scp ran %d times", len(calls))
}

// git runs git in dir with the run's environment, failing t on an error, and returns its trimmed output.
func (l *liveSSH) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = l.env
	if cmd.Env == nil {
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+filepath.Join(l.root, "git", "config"), "GIT_CONFIG_NOSYSTEM=1")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (l *liveSSH) readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (l *liveSSH) writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
