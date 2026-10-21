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
	"github.com/ndrwl/guildhall/internal/strictjson"
)

const toolsPath = "/home/guild/directors/tools/config.toml"
const docsPath = "/home/guild/directors/docs/config.toml"
const gamesPath = "C:/Users/guild/directors/games/config.toml"
const gamesCopy = "schema_version = 1\r\nmode = \"director\"\r\nname = \"games\"\r\n[local]\r\nnotes_dir = 'D:/notes'\r\n[[repository]]\r\nid = \"engine\"\r\npath = 'C:/src/engine'\r\nremote = \"git@github.com:example/engine.git\"\r\nowner_machine = \"laptop\"\r\n"

var stamp = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func managedPath(p string) string { return strings.TrimSuffix(p, "config.toml") + "managed.toml" }
func fleet(t *testing.T) (*stub, *Rollout) {
	t.Helper()
	s := newStub(t)
	private := t.TempDir()
	for name, content := range map[string]string{"roster.toml": rosterFixture, "managed.toml": managedFixture} {
		if err := os.WriteFile(filepath.Join(private, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldManaged := map[string]config.ManagedSetting{
		"standards_revision":  {Value: "standards-1"},
		"review_instructions": {Value: "Cite the requirement.", Overridable: true},
	}
	const docsLocal = `schema_version = 1
mode = "director"
name = "docs"
[local]
review_instructions = "Cite the requirement and check Windows paths."
notes_dir = "notes"
[[repository]]
id = "handbook"
path = '/srv/src/handbook'
remote = "git@github.com:example/handbook.git"
owner_machine = "workstation"
`
	for _, c := range []struct {
		alias, path, local string
		managed            map[string]config.ManagedSetting
	}{{"guild-ws", toolsPath, localCopyFixture, oldManaged}, {"guild-ws", docsPath, docsLocal, oldManaged}, {"guild-laptop", gamesPath, gamesCopy, nil}} {
		managed, err := config.EncodeManaged(c.managed)
		if err != nil {
			t.Fatal(err)
		}
		s.put(c.alias, c.path, c.local)
		s.put(c.alias, managedPath(c.path), string(managed))
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
	edited := s.get("guild-ws", toolsPath) + "\n# concurrent editor change\n"
	r.Transport = editingCopier{r.Transport, func(src Remote) {
		if src.Alias == "guild-laptop" {
			s.put("guild-ws", toolsPath, edited)
		}
	}}
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

// editingCopier runs edit before each fetch, as a concurrent editor would.
type editingCopier struct {
	Copier
	edit func(src Remote)
}

func (c editingCopier) Fetch(ctx context.Context, src Remote, dst string) error {
	c.edit(src)
	return c.Copier.Fetch(ctx, src, dst)
}

func TestRolloutRefusals(t *testing.T) {
	for _, scenario := range []string{"machines", "duplicate", "approval", "stale", "preview", "inline", "lock"} {
		t.Run(scenario, func(t *testing.T) {
			s, r := fleet(t)
			if scenario == "inline" {
				s.put("guild-ws", toolsPath, localCopyFixture+"\n[managed.standards_revision]\nvalue = 'standards-1'\n")
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

func TestReadPlanIsStrict(t *testing.T) {
	const valid = `{"schema":"guildhall.rollout-plan/1","digest":"d","roster_digest":"r","managed_digest":"m","machines":["laptop"],"stage_dir":"s","copies":[{"machine":"laptop","director":"docs","ssh_alias":"laptop","target":"t","status":"changed"}]}`
	for name, tc := range map[string]struct {
		data  string
		newer bool
	}{
		"valid":                {data: valid},
		"duplicate key":        {data: strings.Replace(valid, `"digest":"d",`, `"digest":"d","digest":"e",`, 1)},
		"unknown field":        {data: strings.Replace(valid, `"digest":"d",`, `"digest":"d","extra":1,`, 1)},
		"unknown copy field":   {data: strings.Replace(valid, `"machine":"laptop",`, `"machine":"laptop","extra":1,`, 1)},
		"trailing data":        {data: valid + `{}`},
		"unknown schema":       {data: strings.Replace(valid, "rollout-plan/1", "rollout-receipt/1", 1)},
		"newer schema":         {data: strings.Replace(valid, "rollout-plan/1", "rollout-plan/2", 1), newer: true},
		"newer schema, extras": {data: strings.Replace(strings.Replace(valid, "rollout-plan/1", "rollout-plan/2", 1), `"digest":"d",`, `"digest":"d","extra":1,`, 1), newer: true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			p, err := ReadPlan(dir)
			if name == "valid" {
				if err != nil || p.Copies[0].Director != "docs" {
					t.Fatalf("ReadPlan = %+v, %v", p, err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted")
			}
			if got := errors.Is(err, strictjson.ErrNewerSchema); got != tc.newer {
				t.Fatalf("errors.Is(ErrNewerSchema) = %v, err = %v", got, err)
			}
		})
	}
}
