package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/steward/stewardtest"
	"github.com/ndrwl/guildhall/internal/store"
)

// collectGit runs git in dir with a fixed identity, failing the test on an error.
func collectGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// collectSetup is a Director copy with a real store, reached through a Fleet that runs the fixed commands on it with
// this binary's App, and a Steward copy whose private config is a clone of a bare remote enrolling it.
type collectSetup struct {
	director, steward, private, remote string
	fleet                              *stewardtest.Fleet
	// receive, when set, answers messages receive first, as a Handler does; collectFails, when set, fails the copy's
	// pending lesson list with it, as an unreachable machine does.
	receive      stewardtest.Handler
	collectFails string
	// status, when set, answers director status first, as a Handler does.
	status stewardtest.Handler
}

func newCollectSetup(t *testing.T) *collectSetup {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, global, "")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	cs := &collectSetup{steward: t.TempDir(), remote: t.TempDir()}
	// The roster's copy_dir admits no space, which newCopy's directory has, so the copy is reached through a link.
	cs.director = filepath.Join(t.TempDir(), "alpha")
	if err := os.Symlink(newCopy(t), cs.director); err != nil {
		t.Skipf("no symbolic link to the copy: %v", err)
	}
	copyDir := filepath.ToSlash(cs.director)
	osName := "linux"
	if runtime.GOOS == "windows" {
		osName = "windows"
	}
	roster := fmt.Sprintf("schema_version = 1\n\n[[machine]]\nlabel = \"workstation\"\nssh_alias = \"guild-ws\"\nos = %q\nroles = [\"director\"]\n\n[[machine.director]]\nname = \"alpha\"\ncopy_dir = %q\nrepositories = [\"app\"]\n", osName, copyDir)
	if _, err := steward.ParseRoster([]byte(roster)); err != nil {
		t.Skipf("the temporary copy %s is outside the roster's restricted path characters: %v", copyDir, err)
	}
	cs.private = filepath.Join(cs.steward, "private")
	collectGit(t, cs.remote, "init", "-q", "--bare", "-b", "main")
	collectGit(t, cs.steward, "clone", "-q", cs.remote, "private")
	collectGit(t, cs.private, "checkout", "-q", "-b", "main")
	writeFile(t, filepath.Join(cs.private, "roster.toml"), roster)
	writeFile(t, filepath.Join(cs.private, "managed.toml"), "schema_version = 1\n")
	collectGit(t, cs.private, "add", ".")
	collectGit(t, cs.private, "commit", "-q", "-m", "private config")
	collectGit(t, cs.private, "push", "-q", "-u", "origin", "main")
	writeFile(t, filepath.Join(cs.steward, "config.toml"), stewardSessionConfig(cs.private, "[steward]\nname = \"steward\"\nadapter = \"fake\"\n"))
	s, err := store.Open(filepath.Join(cs.steward, stewardStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cs.fleet = stewardtest.NewFleet()
	cs.fleet.Handle("guild-ws", func(argv []string) ([]byte, bool, error) {
		if argv[1] == "director" && argv[2] == "collected" {
			data, ok := cs.fleet.Read(steward.Remote{Alias: "guild-ws", Path: copyDir + "/" + steward.CollectReceiptPath})
			if !ok {
				return nil, true, fmt.Errorf("no receipt was copied")
			}
			writeFile(t, filepath.Join(cs.director, filepath.FromSlash(steward.CollectReceiptPath)), string(data))
		}
		if argv[1] == "messages" && argv[2] == "receive" {
			if cs.receive != nil {
				if out, handled, err := cs.receive(argv); handled {
					return out, true, err
				}
			}
			data, ok := cs.fleet.Read(steward.Remote{Alias: "guild-ws", Path: copyDir + "/" + steward.MessageDropPath})
			if !ok {
				return nil, true, fmt.Errorf("no message was copied")
			}
			writeFile(t, filepath.Join(cs.director, filepath.FromSlash(steward.MessageDropPath)), string(data))
		}
		if cs.status != nil && argv[1] == "director" && argv[2] == "status" {
			if out, handled, err := cs.status(argv); handled {
				return out, true, err
			}
		}
		if cs.collectFails != "" && argv[1] == "lesson" {
			return nil, true, fmt.Errorf("%s", cs.collectFails)
		}
		app, stdout, stderr := newTestApp(nil, nil)
		if code := app.Run(argv[1:]); code != 0 {
			return nil, true, fmt.Errorf("exit %d: %s", code, stderr)
		}
		return stdout.Bytes(), true, nil
	})
	return cs
}

// app is an App whose collection reaches the Director copy through the Fleet and runs git from PATH.
func (cs *collectSetup) app() (*App, *bytes.Buffer, *bytes.Buffer) {
	app, stdout, stderr := newTestApp(nil, nil)
	app.Copier, app.Runner = cs.fleet, cs.fleet
	app.LookPath = exec.LookPath
	app.Output = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, path, args...).Output()
	}
	return app, stdout, stderr
}

func (cs *collectSetup) collect(t *testing.T) (int, collectRound, string) {
	t.Helper()
	app, stdout, stderr := cs.app()
	code := app.Run([]string{"steward", "collect", "--copy", cs.steward})
	var round collectRound
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &round); err != nil {
			t.Fatalf("collect printed %q: %v", stdout, err)
		}
	}
	return code, round, stderr.String()
}

// TestStewardCollect: steward collect brings a copy's filed lesson and incident into the private config as one
// commit, pushes it, marks them collected on the copy and opens one triage decision each; running it again changes
// nothing, and an answer the Guildmaster gives outside a session is written back into the item's file.
func TestStewardCollect(t *testing.T) {
	cs := newCollectSetup(t)
	if code, _, stderr := queueCmd(t, cliLesson, "lesson", "propose", "--id", "rebase", "-", "--copy", cs.director); code != 0 {
		t.Fatalf("propose: %s", stderr)
	}
	if code, _, stderr := queueCmd(t, cliIncident("i-stuck", "rebase stuck"), "incident", "file", "-", "--copy", cs.director); code != 0 {
		t.Fatalf("file: %s", stderr)
	}
	start := collectGit(t, cs.private, "rev-parse", "HEAD")
	code, round, stderr := cs.collect(t)
	if code != 0 {
		t.Fatalf("collect: exit %d, %+v, %s", code, round, stderr)
	}
	if got := collectGit(t, cs.private, "rev-list", "--count", start+"..HEAD"); got != "1" {
		t.Errorf("collect made %s commits, want 1", got)
	}
	if round.Commit != collectGit(t, cs.remote, "rev-parse", "main") {
		t.Errorf("round commit %s is not the remote's", round.Commit)
	}
	if len(round.Opened) != 2 || !strings.HasPrefix(round.Opened[0], "steward/incidents/incident-") || !strings.HasPrefix(round.Opened[1], "steward/lessons/lesson-") {
		t.Errorf("opened %q, want one incident and one lesson decision", round.Opened)
	}
	for _, args := range [][]string{{"lesson", "list", "--json", "--pending"}, {"incident", "list", "--json", "--pending"}} {
		code, out, _ := queueCmd(t, "", append(args, "--copy", cs.director)...)
		if code != 0 || strings.Contains(out, `"id"`) {
			t.Errorf("%s: exit %d, %s; want nothing pending", args[0], code, out)
		}
	}
	if code, out, _ := queueCmd(t, "", "lesson", "list", "--copy", cs.director); code != 0 || out != "rebase collected Rebase before review\n" {
		t.Errorf("lesson list = %q", out)
	}
	storePath := filepath.Join(cs.steward, stewardStoreFile)
	decisions, err := triageDecisions(storePath)
	if err != nil || len(decisions) != 2 {
		t.Fatalf("triage decisions = %v, %v", decisions, err)
	}
	var lessonID string
	for id, d := range decisions {
		if !d.Open() || d.Kind != store.DecisionApproval || d.Subject == nil {
			t.Errorf("decision %s = %+v, want an open approval bound to the item", id, d)
		}
		if d.TaskID == stewardLessonsTask {
			lessonID = id
			if strings.Join(d.Options, ",") != "adopt,decline" || !strings.Contains(d.Question, `"Rebase before review"`) {
				t.Errorf("lesson decision = %+v", d)
			}
		} else if strings.Join(d.Options, ",") != "accept,defer,decline" {
			t.Errorf("incident decision = %+v", d)
		}
	}

	head := collectGit(t, cs.private, "rev-parse", "HEAD")
	if code, again, stderr := cs.collect(t); code != 0 || len(again.Opened) != 0 || again.Commit != "" || collectGit(t, cs.private, "rev-parse", "HEAD") != head {
		t.Errorf("second collect: exit %d, %+v, %s; want nothing changed", code, again, stderr)
	}

	// Only the Guildmaster answers triage, outside any session.
	inSession, _, errOut := newTestApp(nil, nil)
	inSession.Getenv = func(k string) string { return map[string]string{session.NumberEnv: "1"}[k] }
	inSession.Stdin = strings.NewReader("adopt\n")
	if code := inSession.Run([]string{"decide", "answer", "--copy", cs.steward, "--repo", stewardRepository, "--task", stewardLessonsTask, "--decision", lessonID, "--answer-file", "-"}); code != 1 || !strings.Contains(errOut.String(), "Guildmaster's triage") {
		t.Errorf("answer in a session: exit %d, %s; want it refused", code, errOut)
	}
	if code, _, stderr := queueCmd(t, "adopt\nWorth it.\n", "decide", "answer", "--copy", cs.steward, "--repo", stewardRepository, "--task", stewardLessonsTask, "--decision", lessonID, "--answer-file", "-"); code != 0 {
		t.Fatalf("answer: %s", stderr)
	}
	code, round, stderr = cs.collect(t)
	if code != 0 || len(round.Answers) != 1 || !strings.HasPrefix(round.Answers[0], "lessons/workstation/") {
		t.Fatalf("collect after the answer: exit %d, %+v, %s", code, round, stderr)
	}
	data, err := os.ReadFile(filepath.Join(cs.private, filepath.FromSlash(round.Answers[0])))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "triage: \"adopted\"\ntriage_decision: \"steward/lessons/"+lessonID+"\"\n") || !strings.Contains(string(data), `triage_note: "Worth it."`) {
		t.Errorf("lesson file after the answer:\n%s", data)
	}
	if got := collectGit(t, cs.remote, "log", "-1", "--format=%s", "main"); got != "Record 1 triage answer" {
		t.Errorf("remote's last commit %q", got)
	}
}

// TestStewardCollectBusy: a held .rollout.lock makes steward collect exit 1 saying busy, changing nothing.
func TestStewardCollectBusy(t *testing.T) {
	cs := newCollectSetup(t)
	if code, _, stderr := queueCmd(t, cliLesson, "lesson", "propose", "-", "--copy", cs.director); code != 0 {
		t.Fatal(stderr)
	}
	writeFile(t, filepath.Join(cs.private, ".rollout.lock"), "")
	head := collectGit(t, cs.private, "rev-parse", "HEAD")
	if code, _, stderr := cs.collect(t); code != 1 || !strings.Contains(stderr, "busy") {
		t.Errorf("collect: exit %d, %s; want busy", code, stderr)
	}
	if collectGit(t, cs.private, "rev-parse", "HEAD") != head {
		t.Error("a busy collect committed")
	}
	if code, out, _ := queueCmd(t, "", "lesson", "list", "--copy", cs.director); code != 0 || !strings.Contains(out, " proposed ") {
		t.Errorf("lesson list = %q, want it still proposed", out)
	}
}

// TestScheduledCollect: collect_every unset schedules nothing; a scheduled round that opens triage decisions sends
// the Steward's inbox one triage-opened message per kind, tagged director, and a round with nothing new sends none.
func TestScheduledCollect(t *testing.T) {
	cs := newCollectSetup(t)
	app, _, _ := cs.app()
	cfg, err := config.Load(filepath.Join(cs.steward, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(cs.steward, stewardStoreFile)
	if w, err := app.stewardCollection(cfg, storePath); err != nil || w != nil {
		t.Fatalf("collect_every unset: %v, %v; want no round", w, err)
	}
	cfg.Steward.CollectEvery = "1h"
	w, err := app.stewardCollection(cfg, storePath)
	if err != nil || w == nil {
		t.Fatalf("collect_every 1h: %v, %v", w, err)
	}
	messages := func() []store.Message {
		ms, err := withStore(storePath, true, func(s *store.Store) ([]store.Message, error) {
			ms, _, err := s.Inbox(store.Recipient{Role: store.RecipientSteward}, 0, store.MaxInboxLimit)
			return ms, err
		})
		if err != nil {
			t.Fatal(err)
		}
		return ms
	}
	w.round()
	if ms := messages(); len(ms) != 0 {
		t.Fatalf("a round with nothing new sent %+v", ms)
	}
	if code, _, stderr := queueCmd(t, cliIncident("i-one", "boom"), "incident", "file", "-", "--copy", cs.director); code != 0 {
		t.Fatal(stderr)
	}
	w.round()
	ms := messages()
	if len(ms) != 1 || ms[0].Sender != store.SenderDirector || ms[0].Type != store.MessageTriageOpened || ms[0].TaskID != stewardIncidentsTask ||
		!regexp.MustCompile(`^Collection opened 1 triage decision: steward/incidents/incident-[0-9a-f]{16}\.`).MatchString(ms[0].Text) {
		t.Fatalf("messages = %+v, want one triage-opened from director", ms)
	}
	w.round()
	if ms := messages(); len(ms) != 1 {
		t.Errorf("a round with nothing new sent more: %+v", ms)
	}
	begun, err := app.stewardSessionStart(startPlan{cfg: &config.Config{Mode: config.ModeSteward}}, cs.steward, storePath, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(begun.inFlight) != 1 || !strings.Contains(begun.inFlight[0], "triage steward/incidents/incident-") || begun.collects != nil {
		t.Errorf("startup in flight %q, collects %v; want the open triage decision and no schedule", begun.inFlight, begun.collects)
	}
}

// TestDirectorCollected: director collected marks the receipt's items whose origin and digest are the copy's,
// reports the others and leaves them pending, deletes the receipt, and reports a repeated item as already collected.
func TestDirectorCollected(t *testing.T) {
	dir := newCopy(t)
	if code, _, stderr := queueCmd(t, cliLesson, "lesson", "propose", "--id", "rebase", "-", "--copy", dir); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, stderr := queueCmd(t, cliIncident("i-one", "boom"), "incident", "file", "-", "--copy", dir); code != 0 {
		t.Fatal(stderr)
	}
	var lessons lessonListJSON
	if _, out, _ := queueCmd(t, "", "lesson", "list", "--json", "--pending", "--copy", dir); json.Unmarshal([]byte(out), &lessons) != nil {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(dir, "state", "steward")); err != nil {
		t.Errorf("the pending list did not make the receipt directory: %v", err)
	}
	origin, l := lessons.Origin, lessons.Lessons[0]
	commit := strings.Repeat("c", 40)
	receipt := steward.CollectReceipt{Schema: steward.CollectReceiptSchema, Items: []steward.ReceiptItem{
		{Kind: store.QueueLesson, Origin: origin, ID: "rebase", Digest: l.Digest, Commit: commit},
		{Kind: store.QueueIncident, Origin: origin, ID: "i-one", Digest: "sha256:" + strings.Repeat("0", 64), Commit: commit},
		{Kind: store.QueueIncident, Origin: origin, ID: "i-none", Digest: l.Digest, Commit: commit},
		{Kind: store.QueueLesson, Origin: "ffffffffffffffff", ID: "rebase", Digest: l.Digest, Commit: commit},
	}}
	file := filepath.Join(dir, filepath.FromSlash(steward.CollectReceiptPath))
	put := func() {
		data, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, file, string(data))
	}
	put()
	code, out, stderr := queueCmd(t, "", "director", "collected", "--copy", dir)
	if code != 0 {
		t.Fatalf("collected: exit %d, %s", code, stderr)
	}
	var report steward.CollectedReport
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Schema != steward.CollectedReportSchema || len(report.Items) != 4 {
		t.Fatalf("report %s: %v", out, err)
	}
	for i, want := range []string{steward.MarkCollected, steward.MarkMismatch, steward.MarkMissing, steward.MarkMismatch} {
		if report.Items[i].Status != want {
			t.Errorf("item %d = %+v, want %s", i, report.Items[i], want)
		}
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the receipt was not deleted: %v", err)
	}
	if _, out, _ := queueCmd(t, "", "incident", "list", "--copy", dir); !strings.HasPrefix(out, "i-one open ") {
		t.Errorf("incident list = %q, want the mismatched incident still open", out)
	}
	if _, out, _ := queueCmd(t, "", "lesson", "list", "--copy", dir); out != "rebase collected Rebase before review\n" {
		t.Errorf("lesson list = %q", out)
	}
	receipt.Items = receipt.Items[:1]
	put()
	if _, out, _ := queueCmd(t, "", "director", "collected", "--copy", dir); !strings.Contains(out, `"status":"already-collected"`) {
		t.Errorf("collected again = %s", out)
	}
	if code, _, stderr := queueCmd(t, "", "director", "collected", "--copy", dir); code != 1 || !strings.Contains(stderr, "no collection receipt") {
		t.Errorf("no receipt: exit %d, %s", code, stderr)
	}
	writeFile(t, file, `{"schema":"guildhall.collect-receipt/1","items":[],"extra":1}`)
	if code, _, _ := queueCmd(t, "", "director", "collected", "--copy", dir); code != 1 {
		t.Errorf("bad receipt: exit %d, want 1", code)
	}
}
