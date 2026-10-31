package steward

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
)

// TestRolloutReloadsEachWrittenCopy covers apply running config reload, over ssh, on each copy it wrote and only
// those, with the planned command recorded as the receipt's last step and its output kept.
func TestRolloutReloadsEachWrittenCopy(t *testing.T) {
	s, r := fleet(t)
	const authorized = "managed revision authorized by reload"
	s.answer("guild-ws", authorized+"\n", "")
	p := dryRun(t, r, "workstation", "laptop")
	wantRun := map[string][][]string{
		"guild-ws":     {ReloadCommand(DefaultGuildhall, "/home/guild/directors/tools"), ReloadCommand(DefaultGuildhall, "/home/guild/directors/docs")},
		"guild-laptop": {ReloadCommand(DefaultGuildhall, "C:/Users/guild/directors/games")},
	}
	for _, c := range p.Copies {
		last := c.Commands[len(c.Commands)-1]
		want := sshCommand(c.SSHAlias, ReloadCommand(DefaultGuildhall, strings.TrimSuffix(c.Target, "/managed.toml")))
		if !slices.Equal(last, want) {
			t.Errorf("%s planned last command %q, want %q", c.Director, last, want)
		}
	}
	if got := s.ran("guild-ws"); len(got) != 0 {
		t.Fatalf("dry run ran %q", got)
	}
	receipt, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...)
	if err != nil {
		t.Fatal(err)
	}
	for alias, want := range wantRun {
		if got := s.ran(alias); !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("%s ran %q, want %q", alias, got, want)
		}
	}
	for i, c := range receipt.Copies {
		if c.Status != StatusApplied || len(c.Steps) != 4 {
			t.Fatalf("receipt copy %+v", c)
		}
		step := c.Steps[3]
		if !slices.Equal(step.Command, p.Copies[i].Commands[3]) || step.Error != "" {
			t.Errorf("%s reload step %+v, want the planned %q", c.Director, step, p.Copies[i].Commands[3])
		}
		want := ""
		if c.Machine == "workstation" {
			want = authorized
		}
		if step.Output != want {
			t.Errorf("%s reload output %q, want %q", c.Director, step.Output, want)
		}
	}
	fresh := dryRun(t, r, p.Machines...)
	if _, err := r.Apply(context.Background(), fresh.StageDir, "", fresh.Machines...); err != nil {
		t.Fatal(err)
	}
	if got := s.ran("guild-laptop"); len(got) != 1 {
		t.Errorf("an unchanged apply ran %q, want no new reload", got)
	}
}

// TestRolloutReportsAFailedReloadPerCopy covers an old guildhall that does not know config reload: that copy is
// reload_failed with the remote diagnostic in its receipt, its published file stays, and the other copies apply.
func TestRolloutReportsAFailedReloadPerCopy(t *testing.T) {
	s, r := fleet(t)
	const old = `guildhall: unknown command "config"`
	s.answer("guild-laptop", "", old)
	p := dryRun(t, r, "workstation", "laptop")
	receipt, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Apply: %v, want ErrIncomplete", err)
	}
	for _, c := range receipt.Copies[:2] {
		if c.Status != StatusApplied {
			t.Errorf("%s: %+v", c.Director, c)
		}
	}
	games := receipt.Copies[2]
	if games.Status != StatusReloadFailed || !strings.Contains(games.Error, old) || games.VerifiedSHA256 != games.AfterSHA256 {
		t.Fatalf("games receipt %+v", games)
	}
	if last := games.Steps[len(games.Steps)-1]; !strings.Contains(last.Error, old) || last.Command[0] != "ssh" {
		t.Errorf("games reload step %+v", last)
	}
	data, err := os.ReadFile(receipt.File)
	if err != nil || !strings.Contains(string(data), `"status": "reload_failed"`) || !strings.Contains(string(data), `unknown command \"config\"`) {
		t.Errorf("receipt file does not read as reload_failed with the diagnostic: %v\n%s", err, data)
	}
	if got := digest.Of([]byte(s.get("guild-laptop", managedPath(gamesPath)))).Hex(); got != games.AfterSHA256 {
		t.Error("a failed reload undid the published managed file")
	}
}

// TestPlanDigestCoversTheReloadCommand pins that the planned reload, guildhall_path included, is part of what an
// approval of the plan's digest approves, and that apply runs the roster's guildhall_path.
func TestPlanDigestCoversTheReloadCommand(t *testing.T) {
	s, r := fleet(t)
	const program = "C:/Users/guild/bin/guildhall.exe"
	p := dryRun(t, r, "laptop")
	tampered := *p
	tampered.Copies = slices.Clone(p.Copies)
	c := &tampered.Copies[0]
	c.Commands = slices.Clone(c.Commands)
	c.Commands[len(c.Commands)-1] = sshCommand(c.SSHAlias, ReloadCommand(program, "C:/Users/guild/directors/games"))
	if tampered.digest() == p.Digest {
		t.Fatal("the plan digest does not cover the reload command")
	}

	roster := strings.Replace(rosterFixture, `os = "windows"`, "os = \"windows\"\nguildhall_path = \""+program+"\"", 1)
	if err := os.WriteFile(filepath.Join(r.privateDir, "roster.toml"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	withPath, err := LoadRollout(r.privateDir, s.transport(), s.runner())
	if err != nil {
		t.Fatal(err)
	}
	q := dryRun(t, withPath, "laptop")
	argv := ReloadCommand(program, "C:/Users/guild/directors/games")
	if got := q.Copies[0].Commands[len(q.Copies[0].Commands)-1]; !slices.Equal(got, sshCommand("guild-laptop", argv)) {
		t.Errorf("planned reload %q, want %q", got, sshCommand("guild-laptop", argv))
	}
	if _, err := withPath.Apply(context.Background(), q.StageDir, q.Digest, q.Machines...); err != nil {
		t.Fatal(err)
	}
	if got := s.ran("guild-laptop"); len(got) != 1 || !slices.Equal(got[0], argv) {
		t.Errorf("laptop ran %q, want %q", got, argv)
	}
}
