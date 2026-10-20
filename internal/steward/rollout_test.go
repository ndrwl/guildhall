package steward

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
)

const toolsPath = "/home/guild/directors/tools/config.toml"
const docsPath = "/home/guild/directors/docs/config.toml"
const gamesPath = "C:/Users/guild/directors/games/config.toml"
const gamesCopy = "schema_version = 1\r\nmode = \"director\"\r\nname = \"games\"\r\n[local]\r\nnotes_dir = 'D:/notes'\r\n[[repository]]\r\nid = \"engine\"\r\npath = 'C:/src/engine'\r\nremote = \"git@github.com:example/engine.git\"\r\nowner_machine = \"laptop\"\r\n"

var stamp = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func managedPath(p string) string { return strings.TrimSuffix(p, "config.toml") + "managed.toml" }
func splitCopy(t *testing.T, data string) (string, string) {
	t.Helper()
	cfg, err := config.Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	m, err := config.EncodeManaged(cfg.Managed)
	if err != nil {
		t.Fatal(err)
	}
	return string(splice([]byte(data), nil)), string(m)
}
func fleet(t *testing.T) (*stub, *Rollout) {
	t.Helper()
	s := newStub(t)
	private := t.TempDir()
	for name, content := range map[string]string{"roster.toml": rosterFixture, "managed.toml": managedFixture} {
		if err := os.WriteFile(filepath.Join(private, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	docs := strings.ReplaceAll(strings.ReplaceAll(copyFixture, `name = "tools"`, `name = "docs"`), `id = "cli"`, `id = "handbook"`)
	docs = strings.Split(docs, "\n[[repository]]\nid = \"site\"")[0] + "\n"
	for _, c := range []struct{ alias, path, data string }{{"guild-ws", toolsPath, copyFixture}, {"guild-ws", docsPath, docs}, {"guild-laptop", gamesPath, gamesCopy}} {
		local, managed := splitCopy(t, c.data)
		s.put(c.alias, c.path, local)
		s.put(c.alias, managedPath(c.path), managed)
	}
	r, err := LoadRollout(private, s.transport())
	if err != nil {
		t.Fatal(err)
	}
	r.Now = func() time.Time { return stamp }
	return s, r
}
func dryRun(t *testing.T, r *Rollout, machines ...string) *Plan {
	t.Helper()
	p, err := r.Plan(context.Background(), machines, filepath.Join(t.TempDir(), "plan"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePlan(p); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestRolloutPreservesConcurrentLocalEditAndRevisions(t *testing.T) {
	s, r := fleet(t)
	original := s.get("guild-ws", managedPath(toolsPath))
	p := dryRun(t, r, "workstation", "laptop")
	for _, c := range p.Copies {
		if c.Status != StatusChange {
			t.Fatalf("plan: %+v", c)
		}
	}
	for _, call := range s.calls() {
		if !strings.Contains(call, "-- guild-") {
			t.Fatal("dry run wrote", call)
		}
	}
	output := r.Transport.Output
	edited := s.get("guild-ws", toolsPath) + "\n# concurrent editor change\n"
	r.Transport.Output = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "guild-laptop:") {
			s.put("guild-ws", toolsPath, edited)
		}
		return output(ctx, path, args...)
	}
	receipt, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range receipt.Copies {
		if c.Status != StatusApplied || c.VerifiedSHA256 != c.AfterSHA256 {
			t.Fatalf("receipt: %+v", c)
		}
	}
	if got := s.get("guild-ws", toolsPath); got != edited {
		t.Fatal("local edit overwritten")
	}
	if got := s.get("guild-ws", receipt.Copies[0].Revision); got != original {
		t.Fatal("old revision lost")
	}
	if got := s.get("guild-laptop", gamesPath); got != gamesCopy {
		t.Fatal("CRLF local file changed")
	}
	if _, err := config.ParseCombined([]byte(edited), []byte(s.get("guild-ws", managedPath(toolsPath)))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(receipt.File); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("stale: %v", err)
	}
	fresh := dryRun(t, r, p.Machines...)
	for _, c := range fresh.Copies {
		if c.Status != StatusUnchanged {
			t.Fatalf("fresh: %+v", c)
		}
	}
	if _, err := r.Apply(context.Background(), fresh.StageDir, "", fresh.Machines...); err != nil {
		t.Fatal(err)
	}
}
func TestRolloutRefusals(t *testing.T) {
	for _, scenario := range []string{"machines", "duplicate", "approval", "stale", "preview", "inline", "lock"} {
		t.Run(scenario, func(t *testing.T) {
			s, r := fleet(t)
			if scenario == "inline" {
				s.put("guild-ws", toolsPath, copyFixture)
			}
			p := dryRun(t, r, "workstation")
			machines := p.Machines
			approve := p.Digest
			switch scenario {
			case "machines":
				machines = []string{"laptop"}
			case "duplicate":
				machines = []string{"workstation", "workstation"}
			case "approval":
				approve = "sha256:wrong"
			case "stale":
				s.put("guild-ws", toolsPath, s.get("guild-ws", toolsPath)+"\n# changed\n")
			case "preview":
				if err := os.WriteFile(filepath.Join(p.Copies[0].stage(p.StageDir), "managed.toml"), []byte("bad"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "inline":
				if !strings.Contains(p.Copies[0].Problem, "move") {
					t.Fatal(p.Copies[0].Problem)
				}
			case "lock":
				if err := os.WriteFile(filepath.Join(r.privateDir, ".rollout.lock"), []byte("live writer"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s.calls()
			if _, err := r.Apply(context.Background(), p.StageDir, approve, machines...); err == nil {
				t.Fatal("accepted")
			}
			calls := s.calls()
			for _, call := range calls {
				if !strings.Contains(call, "-- guild-") {
					t.Fatal("refused apply wrote", call)
				}
			}
			if (scenario == "machines" || scenario == "duplicate" || scenario == "lock") && len(calls) != 0 {
				t.Fatal("transport before refusal")
			}
			if scenario == "lock" {
				b, err := os.ReadFile(filepath.Join(r.privateDir, ".rollout.lock"))
				if err != nil || string(b) != "live writer" {
					t.Fatal("removed live lock")
				}
			}
		})
	}
}
func TestRolloutTransferFailures(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "corruption"}[corrupt], func(t *testing.T) {
			s, r := fleet(t)
			p := dryRun(t, r, "workstation", "laptop")
			if corrupt {
				t.Setenv(stubCorruptEnv, "verify.toml")
			} else {
				t.Setenv(stubFailEnv, "managed.toml guild-laptop:")
			}
			receipt, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...)
			if !errors.Is(err, ErrIncomplete) {
				t.Fatal(err)
			}
			if corrupt {
				if receipt.Copies[0].Status != StatusVerifyFailed {
					t.Fatal(receipt)
				}
			} else {
				if receipt.Copies[0].Status != StatusApplied || receipt.Copies[2].Status != StatusFailed {
					t.Fatal(receipt)
				}
				if s.get("guild-laptop", gamesPath) != gamesCopy {
					t.Fatal("local config changed")
				}
			}
		})
	}
}
func TestPlanRefusesUnknownOrRepeatedMachines(t *testing.T) {
	_, r := fleet(t)
	for _, machines := range [][]string{nil, {"unknown"}, {"laptop", "laptop"}} {
		if _, err := r.Plan(context.Background(), machines, t.TempDir()); err == nil {
			t.Fatal("accepted", machines)
		}
	}
}
