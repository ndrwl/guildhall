package launch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// execTabs is a TabHost with no shell: its one pane reports an idle sh, and
// typing a line starts the line's command directly, so pane launches run on
// every platform.
type execTabs struct {
	t        *testing.T
	failType bool

	mu      sync.Mutex
	spec    herdr.TabSpec
	sent    []string
	bridges []chan struct{}
}

func newExecTabs(t *testing.T) *execTabs {
	h := &execTabs{t: t}
	t.Cleanup(func() {
		h.mu.Lock()
		bridges := h.bridges
		h.mu.Unlock()
		for _, done := range bridges {
			testenv.WaitFor(t, "pane bridge exit", func() bool {
				select {
				case <-done:
					return true
				default:
					return false
				}
			})
		}
	})
	return h
}

func (h *execTabs) Workspaces(context.Context) ([]herdr.Workspace, error) { return nil, nil }

func (h *execTabs) CreateWorkspace(_ context.Context, label, _ string) (herdr.Workspace, error) {
	return herdr.Workspace{ID: "w2", Label: label}, nil
}

func (h *execTabs) CreateTab(_ context.Context, spec herdr.TabSpec) (adapter.TabRef, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spec = spec
	return adapter.TabRef{Session: "lab", WorkspaceID: spec.WorkspaceID, TabID: spec.WorkspaceID + ":t1", PaneID: spec.WorkspaceID + ":p1"}, nil
}

func (h *execTabs) AdoptTab(context.Context, adapter.TabRef) error { return nil }

func (h *execTabs) PaneProcessInfo(context.Context, adapter.TabRef) (herdr.ProcessInfo, error) {
	return herdr.ProcessInfo{ShellPID: 1, Foreground: []herdr.Process{{PID: 1, Name: "sh"}}}, nil
}

func (h *execTabs) RunInTab(_ context.Context, _ adapter.TabRef, line string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, line)
	if h.failType {
		return errors.New("lost pane response")
	}
	argv, err := unquoteSh(line)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), h.spec.Env...)
	cmd.Dir = h.spec.Cwd
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	h.bridges = append(h.bridges, done)
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return nil
}

func (h *execTabs) InterruptTab(context.Context, adapter.TabRef) error { return nil }

func (h *execTabs) Sent() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.sent...)
}

// unquoteSh splits a line herdr.ShellCommand quoted for a POSIX shell.
func unquoteSh(line string) ([]string, error) {
	var argv []string
	var cur strings.Builder
	quoted, word := false, false
	for i := 0; i < len(line); i++ {
		switch ch := line[i]; {
		case quoted && ch == '\'':
			quoted = false
		case quoted:
			cur.WriteByte(ch)
		case ch == '\'':
			quoted, word = true, true
		case ch == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			word = true
		case ch == ' ':
			if word {
				argv = append(argv, cur.String())
				cur.Reset()
				word = false
			}
		default:
			return nil, fmt.Errorf("unexpected %q in %q", ch, line)
		}
	}
	if quoted || !word {
		return nil, fmt.Errorf("unterminated line %q", line)
	}
	return append(argv, cur.String()), nil
}

var reportKey = artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}

// presentationReports returns the attempt's presentation-report events.
func presentationReports(t *testing.T, p *launch.Process) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(reportKey.Dir(p.StateDir), artisan.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	var reports []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		if e["event"] == "presentation-report" {
			reports = append(reports, e)
		}
	}
	return reports
}

// reportFixture is a pane launch of app/t7/build/1 whose report answers ok
// and whose read-back returns a pane showing artisan working, each counting
// its calls. lost read-backs before that show no agent; retryErr fails each
// repeated report, and readErr each read-back numbered in readErrAt, or every
// one when readErrAt is empty.
type reportFixture struct {
	p                  *launch.Process
	host               *execTabs
	warnings           *bytes.Buffer
	reports, reads     int
	reportOut          herdr.ReportOutcome
	reportErr, readErr error
	retryErr           error
	readErrAt          []int
	lost               int
	// shown, when set, is the pane every read-back after the lost ones returns.
	shown *herdr.PaneReadBack
	// onRead runs at the start of each read-back.
	onRead      func()
	sentReports []herdr.AgentReport
}

const reportOK = `{"id":"cli:request","result":{"type":"ok"}}`

func newReportFixture(t *testing.T) *reportFixture {
	f := &reportFixture{p: newProcess(t), host: newExecTabs(t), warnings: &bytes.Buffer{}, reportOut: herdr.ReportOutcome{Stdout: []byte(reportOK)}}
	f.p.Presentation = &launch.Presentation{
		Host: f.host,
		Report: func(_ context.Context, _ string, r herdr.AgentReport) (herdr.ReportOutcome, error) {
			f.reports++
			f.sentReports = append(f.sentReports, r)
			if f.reports > 1 && f.retryErr != nil {
				return herdr.ReportOutcome{Exit: 1}, f.retryErr
			}
			return f.reportOut, f.reportErr
		},
		ReadBack: func(_ context.Context, pane string) (herdr.PaneReadBack, error) {
			f.reads++
			if f.onRead != nil {
				f.onRead()
			}
			if f.readErr != nil && (len(f.readErrAt) == 0 || slices.Contains(f.readErrAt, f.reads)) {
				return herdr.PaneReadBack{}, f.readErr
			}
			if f.reads <= f.lost {
				return herdr.PaneReadBack{AgentStatus: "unknown", Pane: json.RawMessage(fmt.Sprintf(`{"agent_status":"unknown","pane_id":%q,"read":%d}`, pane, f.reads))}, nil
			}
			if f.shown != nil {
				return *f.shown, nil
			}
			return herdr.PaneReadBack{AgentStatus: "working", Agent: "artisan", Pane: json.RawMessage(fmt.Sprintf(`{"agent":"artisan","agent_status":"working","pane_id":%q,"read":%d}`, pane, f.reads))}, nil
		},
		ReportRetryDelay: time.Millisecond,
		Warnings:         f.warnings,
	}
	return f
}

// launch starts a long-running attempt, stopping it when the test ends.
func (f *reportFixture) launch(t *testing.T) error {
	t.Helper()
	return f.launchCtx(t, context.Background())
}

// launchCtx is launch with ctx.
func (f *reportFixture) launchCtx(t *testing.T, ctx context.Context) error {
	t.Helper()
	spec := prepared(t, f.p, fixtureSpec(t, "guildhall-fake: delay=10m"))
	h, err := f.p.Launch(ctx, spec)
	if err == nil {
		t.Cleanup(func() { _ = f.p.Stop(h) })
	}
	return err
}

func TestPaneLaunchRecordsOnePresentationReport(t *testing.T) {
	f := newReportFixture(t)
	if err := f.launch(t); err != nil {
		t.Fatal(err)
	}
	if len(f.host.Sent()) != 1 || f.reports != 1 || f.reads != 1 {
		t.Fatalf("sent=%q reports=%d reads=%d", f.host.Sent(), f.reports, f.reads)
	}
	// The pane's environment names the attempt last, over the process's own entries and anything inherited (d33).
	f.host.mu.Lock()
	env := f.host.spec.Env
	f.host.mu.Unlock()
	if len(env) == 0 || env[len(env)-1] != reportKey.Env() || reportKey.Env() != "GUILDHALL_ATTEMPT=app/t7/build/1" {
		t.Fatalf("pane env = %q, want it to end with %s", env, reportKey.Env())
	}
	events := presentationReports(t, f.p)
	if len(events) != 1 {
		t.Fatalf("presentation-report events = %v", events)
	}
	e := events[0]
	if e["pane"] != "w2:p1" || e["exit"] != 0.0 || e["stdout"] != reportOK || e["readback_agent_status"] != "working" || e["readback_agent"] != "artisan" || e["reports"] != 1.0 || e["readbacks"] != 1.0 {
		t.Errorf("event = %v", e)
	}
	if pane, ok := e["readback_pane"].(map[string]any); !ok || pane["pane_id"] != "w2:p1" {
		t.Errorf("readback_pane = %v", e["readback_pane"])
	}
	for _, absent := range []string{"error", "readback_error", "stdout_truncated", "retry_error"} {
		if _, has := e[absent]; has {
			t.Errorf("event has %s: %v", absent, e)
		}
	}
	if f.warnings.Len() != 0 {
		t.Errorf("warnings = %q", f.warnings)
	}
}

func TestFailedReportRecordsItsErrorAndTypesNothing(t *testing.T) {
	f := newReportFixture(t)
	f.reportErr = errors.New("herdr pane report-agent: exit 1: boom")
	f.reportOut = herdr.ReportOutcome{Exit: 1, Stdout: bytes.Repeat([]byte("x"), artisan.PresentationReportLimit+1)}
	if err := f.launch(t); !errors.Is(err, f.reportErr) {
		t.Fatalf("Launch = %v", err)
	}
	if len(f.host.Sent()) != 0 || f.reads != 0 {
		t.Fatalf("sent=%q reads=%d after a failed report", f.host.Sent(), f.reads)
	}
	events := presentationReports(t, f.p)
	if len(events) != 1 {
		t.Fatalf("presentation-report events = %v", events)
	}
	e := events[0]
	if e["pane"] != "w2:p1" || e["exit"] != 1.0 || e["reports"] != 1.0 || e["readbacks"] != 0.0 || e["error"] != f.reportErr.Error() || e["stdout_truncated"] != true || len(e["stdout"].(string)) != artisan.PresentationReportLimit {
		t.Errorf("event = %v", e)
	}
	for _, absent := range []string{"readback_agent_status", "readback_agent", "readback_pane", "readback_error", "retry_error"} {
		if _, has := e[absent]; has {
			t.Errorf("event has %s: %v", absent, e)
		}
	}
}

func TestUnsettledPaneLaunchSkipsTheReadBack(t *testing.T) {
	f := newReportFixture(t)
	f.host.failType = true
	if err := f.launch(t); err == nil {
		t.Fatal("lost typing response not returned")
	}
	if len(f.host.Sent()) != 1 || f.reads != 0 || f.reports != 1 {
		t.Fatalf("sent=%q reports=%d reads=%d", f.host.Sent(), f.reports, f.reads)
	}
	events := presentationReports(t, f.p)
	if len(events) != 1 || events[0]["exit"] != 0.0 || events[0]["reports"] != 1.0 || !strings.Contains(fmt.Sprint(events[0]["readback_error"]), "did not settle") {
		t.Errorf("presentation-report events = %v", events)
	}
}

func TestFailedReadBackOnlyWarns(t *testing.T) {
	f := newReportFixture(t)
	f.readErr = errors.New("herdr pane_not_found: pane w2:p1 not found")
	if err := f.launch(t); err != nil {
		t.Fatal(err)
	}
	// Every read-back fails, so each try is made, with a report between them.
	if len(f.host.Sent()) != 1 || f.reads != launch.DefaultReportTries || f.reports != launch.DefaultReportTries {
		t.Fatalf("sent=%q reports=%d reads=%d", f.host.Sent(), f.reports, f.reads)
	}
	events := presentationReports(t, f.p)
	if len(events) != 1 || events[0]["readback_error"] != f.readErr.Error() || events[0]["exit"] != 0.0 {
		t.Errorf("presentation-report events = %v", events)
	}
	if w := f.warnings.String(); !strings.HasPrefix(w, "guildhall: warning: ") || !strings.Contains(w, f.readErr.Error()) {
		t.Errorf("warnings = %q", w)
	}
}

func TestUnwritableEventsOnlyWarns(t *testing.T) {
	f := newReportFixture(t)
	spec := prepared(t, f.p, fixtureSpec(t, "guildhall-fake: delay=10m"))
	// A directory in place of events.jsonl refuses every append to it.
	if err := os.Mkdir(filepath.Join(reportKey.Dir(f.p.StateDir), artisan.EventsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	h, err := f.p.Launch(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.p.Stop(h) })
	if len(f.host.Sent()) != 1 || f.reports != 1 {
		t.Fatalf("sent=%q reports=%d", f.host.Sent(), f.reports)
	}
	if w := f.warnings.String(); !strings.HasPrefix(w, "guildhall: warning: record Herdr report") {
		t.Errorf("warnings = %q", w)
	}
}

func TestFirstReadBackShowingTheAgentReportsOnce(t *testing.T) {
	f := newReportFixture(t)
	f.p.Presentation.ReportTries = 3
	if err := f.launch(t); err != nil {
		t.Fatal(err)
	}
	if f.reports != 1 || f.reads != 1 || f.warnings.Len() != 0 {
		t.Fatalf("reports=%d reads=%d warnings=%q", f.reports, f.reads, f.warnings)
	}
	e := presentationReports(t, f.p)[0]
	if e["reports"] != 1.0 || e["readbacks"] != 1.0 || e["readback_agent"] != "artisan" || e["readback_agent_status"] != "working" {
		t.Errorf("event = %v", e)
	}
}

// TestLostReportIsRepeatedUntilThePaneShowsIt is the d49 regression: Herdr
// accepts the first report, made at an idle shell, then drops it, so the
// read-back after ownership shows agent_status unknown and no agent (d45
// build-1, d46 build-1). The report is repeated, now with the bridge in the
// foreground, until a read-back shows the agent.
func TestLostReportIsRepeatedUntilThePaneShowsIt(t *testing.T) {
	f := newReportFixture(t)
	f.lost = 1
	if err := f.launch(t); err != nil {
		t.Fatal(err)
	}
	if len(f.host.Sent()) != 1 || f.reports != 2 || f.reads != 2 || f.warnings.Len() != 0 {
		t.Fatalf("sent=%q reports=%d reads=%d warnings=%q", f.host.Sent(), f.reports, f.reads, f.warnings)
	}
	if !reflect.DeepEqual(f.sentReports[0], f.sentReports[1]) || f.sentReports[1].Agent != "artisan" || len(f.sentReports[1].Resume) == 0 {
		t.Errorf("repeated report %+v, first %+v", f.sentReports[1], f.sentReports[0])
	}
	events := presentationReports(t, f.p)
	if len(events) != 1 {
		t.Fatalf("presentation-report events = %v", events)
	}
	e := events[0]
	if e["reports"] != 2.0 || e["readbacks"] != 2.0 || e["readback_agent"] != "artisan" || e["readback_agent_status"] != "working" || e["readback_pane"].(map[string]any)["read"] != 2.0 {
		t.Errorf("event = %v", e)
	}
	for _, absent := range []string{"readback_error", "retry_error"} {
		if _, has := e[absent]; has {
			t.Errorf("event has %s: %v", absent, e)
		}
	}
}

func TestReportThatNeverShowsWarnsOnce(t *testing.T) {
	f := newReportFixture(t)
	f.p.Presentation.ReportTries = 4
	f.lost = 100
	if err := f.launch(t); err != nil {
		t.Fatal(err)
	}
	if len(f.host.Sent()) != 1 || f.reads != 4 || f.reports != 4 {
		t.Fatalf("sent=%q reports=%d reads=%d", f.host.Sent(), f.reports, f.reads)
	}
	w := f.warnings.String()
	if strings.Count(w, "guildhall: warning: ") != 1 || !strings.Contains(w, "Herdr pane w2:p1 does not show agent artisan after 4 read-backs (agent_status unknown)") {
		t.Errorf("warnings = %q", w)
	}
	events := presentationReports(t, f.p)
	if len(events) != 1 || events[0]["reports"] != 4.0 || events[0]["readbacks"] != 4.0 || events[0]["readback_agent"] != "" || events[0]["readback_agent_status"] != "unknown" {
		t.Errorf("presentation-report events = %v", events)
	}
}

func TestUnknownStatusWithTheAgentIsNotShown(t *testing.T) {
	f := newReportFixture(t)
	f.p.Presentation.ReportTries = 3
	f.shown = &herdr.PaneReadBack{AgentStatus: "unknown", Agent: "artisan", Pane: json.RawMessage(`{"agent":"artisan","agent_status":"unknown"}`)}
	if err := f.launch(t); err != nil {
		t.Fatal(err)
	}
	if f.reads != 3 || f.reports != 3 || !strings.Contains(f.warnings.String(), "does not show agent artisan") {
		t.Fatalf("reports=%d reads=%d warnings=%q", f.reports, f.reads, f.warnings)
	}
}

func TestFailedRetryReportIsRecordedAndTheLoopGoesOn(t *testing.T) {
	f := newReportFixture(t)
	f.lost = 2
	f.retryErr = errors.New("herdr pane report-agent: exit 1: busy")
	if err := f.launch(t); err != nil {
		t.Fatal(err)
	}
	if f.reports != 3 || f.reads != 3 || f.warnings.Len() != 0 {
		t.Fatalf("reports=%d reads=%d warnings=%q", f.reports, f.reads, f.warnings)
	}
	e := presentationReports(t, f.p)[0]
	if e["retry_error"] != f.retryErr.Error() || e["exit"] != 0.0 || e["reports"] != 3.0 || e["readback_agent"] != "artisan" {
		t.Errorf("event = %v", e)
	}
	if _, has := e["error"]; has {
		t.Errorf("a retry's error is the first report's: %v", e)
	}
}

func TestFailedReadBackMidLoopWarnsAndGoesOn(t *testing.T) {
	f := newReportFixture(t)
	f.p.Presentation.ReportTries = 3
	f.lost = 100
	f.readErr = errors.New("herdr pane get: exit 1: timeout")
	f.readErrAt = []int{2, 3}
	if err := f.launch(t); err != nil {
		t.Fatal(err)
	}
	if f.reads != 3 || f.reports != 3 {
		t.Fatalf("reports=%d reads=%d", f.reports, f.reads)
	}
	w := f.warnings.String()
	if strings.Count(w, f.readErr.Error()) < 2 || !strings.Contains(w, "does not show agent artisan after 3 read-backs") {
		t.Errorf("warnings = %q", w)
	}
	e := presentationReports(t, f.p)[0]
	if e["readback_error"] != f.readErr.Error() || e["readbacks"] != 3.0 {
		t.Errorf("event = %v", e)
	}
}

func TestCancelledContextStopsTheLoop(t *testing.T) {
	f := newReportFixture(t)
	f.p.Presentation.ReportRetryDelay = time.Minute
	f.lost = 100
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onRead = cancel
	start := time.Now()
	if err := f.launchCtx(t, ctx); err != nil {
		t.Fatalf("Launch = %v, want its handle", err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("Launch took %s after its context was cancelled", took)
	}
	if f.reads != 1 || f.reports > 2 {
		t.Fatalf("reports=%d reads=%d after cancel", f.reports, f.reads)
	}
	if len(presentationReports(t, f.p)) != 1 {
		t.Errorf("presentation-report events = %v", presentationReports(t, f.p))
	}
}
