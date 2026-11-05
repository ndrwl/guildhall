package steward_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/steward/stewardtest"
	"github.com/ndrwl/guildhall/internal/store"
)

// The roster collection tests use: a Linux machine with two copies and a Windows one with one.
const (
	toolsDir   = "/home/guild/directors/tools"
	docsDir    = "/home/guild/directors/docs"
	gamesDir   = "C:/Users/guild/directors/games"
	gamesBin   = "C:/Users/guild/bin/guildhall.exe"
	collectSrc = `schema_version = 1

[[machine]]
label = "workstation"
ssh_alias = "guild-ws"
os = "linux"
roles = ["steward", "director"]

[[machine.director]]
name = "tools"
copy_dir = "/home/guild/directors/tools"
repositories = ["cli"]

[[machine.director]]
name = "docs"
copy_dir = "/home/guild/directors/docs"
repositories = ["handbook"]

[[machine]]
label = "laptop"
ssh_alias = "guild-laptop"
os = "windows"
roles = ["director"]
guildhall_path = "C:/Users/guild/bin/guildhall.exe"

[[machine.director]]
name = "games"
copy_dir = "C:/Users/guild/directors/games"
repositories = ["engine"]
`
)

// fakeDirector is a Director copy's queue as the fixed remote commands see it: its pending lists, and
// `director collected`, which reads the receipt the Steward copied to it.
type fakeDirector struct {
	mu        sync.Mutex
	origin    string
	lessons   []store.Lesson
	incidents []store.Incident
	// failMark makes director collected fail, as a crash between commit and mark does.
	failMark bool
}

func newFakeDirector(origin string) *fakeDirector { return &fakeDirector{origin: origin} }

func (d *fakeDirector) lesson(id, title string) store.Lesson {
	body := "# " + title + "\n\n## Evidence\n\nSeen twice.\n\n## Limits\n\nNone.\n"
	l := store.Lesson{ID: id, Origin: d.origin, Title: title, Body: body, Digest: string(digest.Of([]byte(body))), State: store.LessonProposed,
		Receipt: uint64(len(d.lessons) + 1), Time: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lessons = append(d.lessons, l)
	return l
}

func (d *fakeDirector) incident(id, msg string) store.Incident {
	r := store.IncidentReport{Schema: "guildhall.incident/1", ID: id, Repository: "cli", Task: "t-1", Step: "build", Attempt: "1",
		Release: "v1.2.3", Workflow: "default", ObservedState: "stuck", ObservedAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
		Error: msg, Evidence: []store.Evidence{}, Tried: "retry", Done: "parked", Risk: "low"}
	b, err := json.Marshal(r)
	if err != nil {
		panic(err)
	}
	inc := store.Incident{Report: r, Origin: d.origin, Digest: string(digest.Of(b)), State: store.IncidentOpen}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.incidents = append(d.incidents, inc)
	return inc
}

func (d *fakeDirector) pending() (lessons, incidents []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, l := range d.lessons {
		if l.Collected == nil {
			lessons = append(lessons, l.ID)
		}
	}
	for _, inc := range d.incidents {
		if inc.Collected == nil {
			incidents = append(incidents, inc.Report.ID)
		}
	}
	return lessons, incidents
}

// serve answers the copy's commands on f at alias, for the copy in dir.
func (d *fakeDirector) serve(f *stewardtest.Fleet, alias, dir string, others ...func([]string) ([]byte, bool, error)) func([]string) ([]byte, bool, error) {
	return func(argv []string) ([]byte, bool, error) {
		if argv[len(argv)-1] != dir {
			for _, o := range others {
				if out, ok, err := o(argv); ok {
					return out, ok, err
				}
			}
			return nil, false, nil
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		switch strings.Join(argv[1:len(argv)-1], " ") {
		case "lesson list --json --pending --copy":
			out := map[string]any{"origin": d.origin, "lessons": []store.Lesson{}, "truncated": false}
			var ls []store.Lesson
			for _, l := range d.lessons {
				if l.Collected == nil {
					ls = append(ls, l)
				}
			}
			if ls != nil {
				out["lessons"] = ls
			}
			b, err := json.Marshal(out)
			return b, true, err
		case "incident list --json --pending --copy":
			out := map[string]any{"origin": d.origin, "incidents": []store.Incident{}, "truncated": false}
			var is []store.Incident
			for _, inc := range d.incidents {
				if inc.Collected == nil {
					is = append(is, inc)
				}
			}
			if is != nil {
				out["incidents"] = is
			}
			b, err := json.Marshal(out)
			return b, true, err
		case "director collected --copy":
			if d.failMark {
				return nil, true, errors.New("guildhall: director collected: the store is locked")
			}
			file := steward.Remote{Alias: alias, Path: dir + "/" + steward.CollectReceiptPath}
			data, ok := f.Read(file)
			if !ok {
				return nil, true, errors.New("no receipt")
			}
			r, err := steward.ParseCollectReceipt(data)
			if err != nil {
				return nil, true, err
			}
			report := steward.CollectedReport{Schema: steward.CollectedReportSchema}
			for _, it := range r.Items {
				m := steward.MarkedResult{Kind: it.Kind, Origin: it.Origin, ID: it.ID, Status: steward.MarkMissing}
				at := &store.Collection{Commit: it.Commit}
				for i := range d.lessons {
					if l := &d.lessons[i]; it.Kind == store.QueueLesson && l.ID == it.ID {
						m.Status = d.markOne(l.Digest, it.Digest, &l.Collected, at)
					}
				}
				for i := range d.incidents {
					if inc := &d.incidents[i]; it.Kind == store.QueueIncident && inc.Report.ID == it.ID {
						m.Status = d.markOne(inc.Digest, it.Digest, &inc.Collected, at)
					}
				}
				report.Items = append(report.Items, m)
			}
			b, err := json.Marshal(report)
			return b, true, err
		}
		return nil, false, nil
	}
}

func (d *fakeDirector) markOne(have, want string, collected **store.Collection, at *store.Collection) string {
	switch {
	case have != want:
		return steward.MarkMismatch
	case *collected != nil:
		return steward.MarkAlready
	}
	*collected = at
	return steward.MarkCollected
}

// collectFixture is a private config checkout tracking a bare remote, the roster in it, and the three fake copies.
type collectFixture struct {
	t                 *testing.T
	checkout, remote  string
	global            string
	tools, docs, game *fakeDirector
}

func newCollectFixture(t *testing.T) *collectFixture {
	t.Helper()
	fx := &collectFixture{t: t, checkout: t.TempDir(), remote: t.TempDir(), global: filepath.Join(t.TempDir(), "gitconfig")}
	if err := os.WriteFile(fx.global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", fx.global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	fx.git(fx.remote, "init", "-q", "--bare", "-b", "main")
	fx.git(fx.checkout, "clone", "-q", fx.remote, ".")
	fx.git(fx.checkout, "checkout", "-q", "-b", "main")
	for name, data := range map[string]string{"roster.toml": collectSrc, "managed.toml": "schema_version = 1\n"} {
		if err := os.WriteFile(filepath.Join(fx.checkout, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fx.git(fx.checkout, "add", ".")
	fx.git(fx.checkout, "commit", "-q", "-m", "private config")
	fx.git(fx.checkout, "push", "-q", "-u", "origin", "main")
	fx.tools, fx.docs, fx.game = newFakeDirector("1111111111111111"), newFakeDirector("2222222222222222"), newFakeDirector("3333333333333333")
	return fx
}

func (fx *collectFixture) git(dir string, args ...string) string {
	fx.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fx.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// collector is a Collector over a fresh Fleet on which the three copies answer; refused aliases are unreachable.
func (fx *collectFixture) collector(refused ...string) (*steward.Collector, *stewardtest.Fleet) {
	fx.t.Helper()
	roster, err := steward.ParseRoster([]byte(collectSrc))
	if err != nil {
		fx.t.Fatal(err)
	}
	f := stewardtest.NewFleet()
	f.Handle("guild-ws", fx.tools.serve(f, "guild-ws", toolsDir, fx.docs.serve(f, "guild-ws", docsDir)))
	f.Handle("guild-laptop", fx.game.serve(f, "guild-laptop", gamesDir))
	for _, alias := range refused {
		f.Refuse(alias)
	}
	return &steward.Collector{Roster: roster, Private: git.WorkTree{Dir: fx.checkout}, Transport: f, Runner: f,
		Now: func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) }}, f
}

func (fx *collectFixture) head() string { return fx.git(fx.checkout, "rev-parse", "HEAD") }

// TestCollect: one round commits each machine's new items, and only them, as one commit per machine, pushes it,
// marks them collected on each copy through the receipt and `director collected`, and leaves the Guildmaster's
// uncommitted roster and managed edits alone; a second round changes nothing.
func TestCollect(t *testing.T) {
	ctx := context.Background()
	fx := newCollectFixture(t)
	fx.tools.lesson("l-merge", "Merge after green checks")
	fx.tools.incident("i-stuck", "rebase stuck\nsecond line")
	fx.docs.lesson("l-docs", "Docs need examples")
	fx.game.incident("i-win", "path too long")
	if err := os.WriteFile(filepath.Join(fx.checkout, "roster.toml"), []byte(collectSrc+"# Guildmaster edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.git(fx.checkout, "add", "roster.toml")
	if err := os.WriteFile(filepath.Join(fx.checkout, "managed.toml"), []byte("schema_version = 1\n# unstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	start := fx.head()
	c, f := fx.collector()
	res, err := c.Collect(ctx, nil, nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := fx.git(fx.checkout, "rev-list", "--count", start+"..HEAD"); got != "2" {
		t.Errorf("round made %s commits, want one per machine", got)
	}
	if got := fx.git(fx.checkout, "log", "--format=%s", start+"..HEAD"); got != "Collect 1 incident from laptop\nCollect 2 lessons and 1 incident from workstation" {
		t.Errorf("commit messages = %q", got)
	}
	want := []string{
		"incidents/laptop/3333333333333333-i-win.json",
		"incidents/workstation/1111111111111111-i-stuck.json",
		"lessons/workstation/1111111111111111-l-merge.md",
		"lessons/workstation/2222222222222222-l-docs.md",
	}
	if got := strings.Split(fx.git(fx.checkout, "diff", "--name-only", start, "HEAD"), "\n"); !slices.Equal(got, want) {
		t.Errorf("round committed %q, want only %q", got, want)
	}
	if staged, unstaged := fx.git(fx.checkout, "diff", "--cached", "--name-only"), fx.git(fx.checkout, "diff", "--name-only"); staged != "roster.toml" || unstaged != "managed.toml" {
		t.Errorf("staged %q, unstaged %q; want the Guildmaster's edits kept", staged, unstaged)
	}
	if got := fx.git(fx.remote, "rev-parse", "main"); got != fx.head() || res.Commit != got {
		t.Errorf("remote main %s, HEAD %s, result commit %s; want all equal", got, fx.head(), res.Commit)
	}
	for _, d := range []*fakeDirector{fx.tools, fx.docs, fx.game} {
		if l, i := d.pending(); len(l)+len(i) != 0 {
			t.Errorf("copy %s still has %v and %v pending", d.origin, l, i)
		}
	}
	for _, it := range res.Items() {
		if !it.New || !it.Marked || it.Problem != "" {
			t.Errorf("item %+v, want new and marked", it)
		}
	}
	lesson, err := os.ReadFile(filepath.Join(fx.checkout, want[2]))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(lesson), "---\nschema: \"guildhall.collected-lesson/1\"\nmachine: \"workstation\"\ndirector: \"tools\"\n") ||
		!strings.Contains(string(lesson), "\ntriage: \"pending\"\n---\n# Merge after green checks\n") {
		t.Errorf("collected lesson =\n%s", lesson)
	}
	if got, err := steward.ParseCollected(want[0], must(os.ReadFile(filepath.Join(fx.checkout, want[0])))); err != nil ||
		got.Kind != store.QueueIncident || got.Machine != "laptop" || got.Director != "games" || got.ID != "i-win" || got.Triage != steward.TriagePending || got.Title != "path too long" {
		t.Errorf("ParseCollected = %+v, %v", got, err)
	}
	// The Windows copy got its receipt at its C:/ path and ran the command with its guildhall_path.
	if ran := f.Ran("guild-laptop"); len(ran) != 3 || !slices.Equal(ran[2], steward.CollectedCommand(gamesBin, gamesDir)) {
		t.Errorf("laptop ran %q", ran)
	}
	if calls := f.Calls(); !slices.Contains(calls, "put guild-laptop:"+gamesDir+"/state/steward/collect-receipt.json") {
		t.Errorf("calls %q, want the receipt put to the Windows copy", calls)
	}

	head := fx.head()
	again, err := c.Collect(ctx, nil, nil)
	if err != nil || fx.head() != head || again.Commit != "" || len(again.Items()) != 0 {
		t.Errorf("second round = %+v, %v; HEAD %s, want nothing changed at %s", again, err, fx.head(), head)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// TestCollectResendsOnlyTheMark: a crash between the commit and the mark, here a failing director collected, leaves
// the items committed and pending; the next round writes nothing again and only sends the mark.
func TestCollectResendsOnlyTheMark(t *testing.T) {
	ctx := context.Background()
	fx := newCollectFixture(t)
	fx.tools.lesson("l-one", "One")
	fx.tools.failMark = true
	c, _ := fx.collector()
	res, err := c.Collect(ctx, []string{"workstation"}, nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Copies[0].Error == "" || !strings.Contains(res.Copies[0].Error, "store is locked") {
		t.Errorf("copy = %+v, want the failed mark reported", res.Copies[0])
	}
	if l, _ := fx.tools.pending(); len(l) != 1 {
		t.Fatalf("pending after a failed mark = %v, want the lesson still pending", l)
	}
	head := fx.head()
	fx.tools.failMark = false
	res, err = c.Collect(ctx, []string{"workstation"}, nil)
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if fx.head() != head {
		t.Errorf("second round committed %s, want nothing new on %s", fx.head(), head)
	}
	items := res.Items()
	if len(items) != 1 || items[0].New || !items[0].Marked {
		t.Errorf("items = %+v, want the lesson only marked", items)
	}
	if l, _ := fx.tools.pending(); len(l) != 0 {
		t.Errorf("pending = %v, want none", l)
	}
}

// TestCollectOfflineMachine: an unreachable machine is reported, the others still finish, and the next round
// collects it.
func TestCollectOfflineMachine(t *testing.T) {
	ctx := context.Background()
	fx := newCollectFixture(t)
	fx.tools.lesson("l-one", "One")
	fx.game.incident("i-one", "boom")
	c, _ := fx.collector("guild-laptop")
	res, err := c.Collect(ctx, nil, nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var games steward.CopyCollection
	for _, cc := range res.Copies {
		if cc.Director == "games" {
			games = cc
		}
	}
	if games.Error == "" || !strings.Contains(games.Error, "connection refused") {
		t.Errorf("games = %+v, want it reported unreachable", games)
	}
	if l, _ := fx.tools.pending(); len(l) != 0 {
		t.Errorf("tools still pending %v", l)
	}
	if _, i := fx.game.pending(); len(i) != 1 {
		t.Fatalf("games pending %v, want its incident", i)
	}
	c, _ = fx.collector()
	if _, err := c.Collect(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, i := fx.game.pending(); len(i) != 0 {
		t.Errorf("games pending %v after it came back", i)
	}
	if _, err := os.Stat(filepath.Join(fx.checkout, "incidents/laptop/3333333333333333-i-one.json")); err != nil {
		t.Error(err)
	}
}

// TestCollectBusyLock: a held .rollout.lock stops the round with ErrBusy before it writes, commits or marks.
func TestCollectBusyLock(t *testing.T) {
	fx := newCollectFixture(t)
	fx.tools.lesson("l-one", "One")
	holdLock(t, fx.checkout)
	head := fx.head()
	c, f := fx.collector()
	if _, err := c.Collect(context.Background(), nil, nil); !errors.Is(err, steward.ErrBusy) {
		t.Fatalf("Collect = %v, want ErrBusy", err)
	}
	if fx.head() != head {
		t.Error("a busy round committed")
	}
	if l, _ := fx.tools.pending(); len(l) != 1 {
		t.Errorf("a busy round marked %v", l)
	}
	for _, call := range f.Calls() {
		if strings.HasPrefix(call, "put ") || strings.Contains(call, "director collected") {
			t.Errorf("a busy round ran %q", call)
		}
	}
	if !lockHeld(t, fx.checkout) {
		t.Error("the held lock was released")
	}
}

// TestCollectRemoteMovedAhead: a push refused because the remote moved ahead is replayed onto it once and the items
// are marked at the replayed commit.
func TestCollectRemoteMovedAhead(t *testing.T) {
	fx := newCollectFixture(t)
	other := t.TempDir()
	fx.git(other, "clone", "-q", fx.remote, ".")
	if err := os.WriteFile(filepath.Join(other, "managed.toml"), []byte("schema_version = 1\n# v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.git(other, "commit", "-q", "-am", "Guildmaster edit")
	fx.git(other, "push", "-q", "origin", "main")
	moved := fx.git(other, "rev-parse", "HEAD")
	fx.tools.lesson("l-one", "One")
	c, _ := fx.collector()
	res, err := c.Collect(context.Background(), []string{"workstation"}, nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := fx.git(fx.remote, "rev-parse", "main^"); got != moved {
		t.Errorf("collection commit's parent %s, want the moved remote %s", got, moved)
	}
	if res.Commit != fx.git(fx.remote, "rev-parse", "main") {
		t.Errorf("result commit %s is not the remote's", res.Commit)
	}
	if fx.tools.lessons[0].Collected == nil || fx.tools.lessons[0].Collected.Commit != res.Commit {
		t.Errorf("lesson collected = %+v, want marked at %s", fx.tools.lessons[0].Collected, res.Commit)
	}
}

// TestCollectRecordsTriageAnswers: an answer is written into its item's file once, in its own commit.
func TestCollectRecordsTriageAnswers(t *testing.T) {
	ctx := context.Background()
	fx := newCollectFixture(t)
	fx.tools.lesson("l-one", "One")
	fx.tools.incident("i-one", "boom")
	c, _ := fx.collector()
	if _, err := c.Collect(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	w := git.WorkTree{Dir: fx.checkout}
	pending, err := w.Grep(ctx, "HEAD", steward.TriagePendingPatterns, "lessons", "incidents")
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending files = %q, %v", pending, err)
	}
	answers := []steward.TriageAnswer{
		{Kind: store.QueueLesson, Path: "lessons/workstation/1111111111111111-l-one.md", Decision: "steward/lessons/x", State: "adopted", Outcome: "drafted", Note: "yes\nplease"},
		{Kind: store.QueueIncident, Path: "incidents/workstation/1111111111111111-i-one.json", Decision: "steward/incidents/y", State: "accepted", Outcome: "the Guildmaster to create a task"},
	}
	head := fx.head()
	res, err := c.Collect(ctx, nil, answers)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Answers) != 2 || fx.head() == head || fx.git(fx.checkout, "log", "-1", "--format=%s") != "Record 2 triage answers" || fx.git(fx.remote, "rev-parse", "main") != fx.head() {
		t.Errorf("answers %q, HEAD %q", res.Answers, fx.git(fx.checkout, "log", "-1", "--format=%s"))
	}
	if pending, _ := w.Grep(ctx, "HEAD", steward.TriagePendingPatterns, "lessons", "incidents"); len(pending) != 0 {
		t.Errorf("still pending %q", pending)
	}
	lesson := string(must(os.ReadFile(filepath.Join(fx.checkout, answers[0].Path))))
	if !strings.Contains(lesson, "triage: \"adopted\"\ntriage_decision: \"steward/lessons/x\"\ntriage_outcome: \"drafted\"\ntriage_note: \"yes\\nplease\"\n---\n# One\n") {
		t.Errorf("lesson after triage =\n%s", lesson)
	}
	got, err := steward.ParseCollected(answers[1].Path, must(os.ReadFile(filepath.Join(fx.checkout, answers[1].Path))))
	if err != nil || got.Triage != "accepted" {
		t.Errorf("incident triage = %+v, %v", got, err)
	}
	head = fx.head()
	if _, err := c.Collect(ctx, nil, answers); err != nil || fx.head() != head {
		t.Errorf("recording the answers again committed %s, %v", fx.head(), err)
	}
}

// TestCollectionCommandsAllowList: the three collection commands are on the allow-list in the fixed shape, the
// Windows program and copy forms among them, and their variations are refused.
func TestCollectionCommandsAllowList(t *testing.T) {
	for _, argv := range [][]string{
		steward.PendingLessonsCommand(steward.DefaultGuildhall, toolsDir),
		steward.PendingIncidentsCommand(steward.DefaultGuildhall, toolsDir),
		steward.CollectedCommand(steward.DefaultGuildhall, toolsDir),
		steward.PendingLessonsCommand(gamesBin, gamesDir),
		steward.PendingIncidentsCommand(gamesBin, gamesDir),
		steward.CollectedCommand(gamesBin, gamesDir),
	} {
		if err := steward.CheckCommand(argv); err != nil {
			t.Errorf("CheckCommand(%q) = %v, want allowed", argv, err)
		}
	}
	for _, argv := range [][]string{
		{"guildhall", "lesson", "list", "--json", "--copy", toolsDir},
		{"guildhall", "lesson", "list", "--pending", "--json", "--copy", toolsDir},
		{"guildhall", "lesson", "list", "--json", "--pending", "--copy", toolsDir, "--extra"},
		{"guildhall", "lesson", "propose", "--copy", toolsDir},
		{"guildhall", "incident", "resolve", "--copy", toolsDir},
		{"guildhall", "director", "collected", "--copy", "directors/tools"},
		{"guildhall", "director", "collected", "--copy", toolsDir + ";id"},
		{"guildhall", "director", "collected", "--receipt", "sha256:00", "--copy", toolsDir},
		{"guildhall", "director", "collected", "--copy", `C:\Users\guild\directors\games`},
		{"guildhall", "incident", "list", "--json", "--pending", "--copy", "C:/Users/../games"},
	} {
		if err := steward.CheckCommand(argv); !errors.Is(err, steward.ErrNotAllowed) {
			t.Errorf("CheckCommand(%q) = %v, want ErrNotAllowed", argv, err)
		}
	}
}

// TestParseCollectReceipt: a receipt is strict JSON of the fixed schema with valid fields.
func TestParseCollectReceipt(t *testing.T) {
	good := fmt.Sprintf(`{"schema":%q,"items":[{"kind":"lesson","origin":"1111111111111111","id":"l-one","digest":"sha256:%s","commit":"%s"}]}`,
		steward.CollectReceiptSchema, strings.Repeat("a", 64), strings.Repeat("b", 40))
	if r, err := steward.ParseCollectReceipt([]byte(good)); err != nil || len(r.Items) != 1 {
		t.Fatalf("ParseCollectReceipt = %+v, %v", r, err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"items"`, `"extra":1,"items"`, 1),
		strings.Replace(good, "collect-receipt/1", "collect-receipt/2", 1),
		strings.Replace(good, `"lesson"`, `"note"`, 1),
		strings.Replace(good, "1111111111111111", "../../etc", 1),
		strings.Replace(good, `"l-one"`, `"L One"`, 1),
		strings.Replace(good, strings.Repeat("b", 40), "main", 1),
	} {
		if _, err := steward.ParseCollectReceipt([]byte(bad)); err == nil {
			t.Errorf("ParseCollectReceipt(%s) succeeded", bad)
		}
	}
}
