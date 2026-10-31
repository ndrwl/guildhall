package launch

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/workbench"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Presentation is the optional terminal boundary for Process. Host creates and
// types into a new attempt-specific workspace; Report registers the newly created pane
// with Herdr. A tab is never used as evidence of worker liveness, and is never
// closed by Process. A durable single-use launch claim prevents duplicate starts
// after uncertain API responses. Recovery reconciles attempt ownership, not panes.
// The pane invokes the same owned launcher through --pane-launch, which detaches
// its lifetime and streams its durable log; losing the pane cannot stop the worker.
// Each launch that reaches Report records what Herdr answered, and what
// ReadBack showed once the launch settled, as a "presentation-report" event,
// for diagnosis only: neither changes the launch's result. When the pane does
// not show the agent then, the report is repeated a bounded number of times,
// and a report that never shows only warns.
type Presentation struct {
	Host   adapter.TabHost
	Report func(context.Context, string, herdr.AgentReport) (herdr.ReportOutcome, error)
	// ReadBack reads the reported pane back once the launcher owns the
	// attempt, and again after each repeated report; nil records the
	// read-back as skipped and repeats nothing.
	ReadBack func(context.Context, string) (herdr.PaneReadBack, error)
	// ReportTries is the most read-backs made once the launcher owns the
	// attempt, reporting again between them until the pane shows the agent;
	// zero is DefaultReportTries.
	ReportTries int
	// ReportRetryDelay is the wait after a repeated report before the next
	// read-back; zero is DefaultReportRetryDelay.
	ReportRetryDelay time.Duration
	// Warnings receives warnings about the read-back, a report that never
	// shows, and the event; nil discards them.
	Warnings io.Writer
	// CloseAfter is the effective artisan_workspace_close_after setting, passed to
	// the bridge as WorkspaceCloseAfterFlag so its finish line states it; zero is
	// never closing automatically.
	CloseAfter time.Duration
	// Output is the effective artisan_pane_output setting, workbench.OutputWorkbench or workbench.OutputPlain,
	// passed to the bridge as PaneOutputFlag; empty is the Workbench.
	Output string
}

const (
	// DefaultReportTries is Presentation.ReportTries when it is zero.
	DefaultReportTries = 5
	// DefaultReportRetryDelay is Presentation.ReportRetryDelay when it is zero.
	DefaultReportRetryDelay = 300 * time.Millisecond
	// reportDeadline bounds the read-backs and repeated reports after the
	// launcher owns the attempt, while the caller's locks are held.
	reportDeadline = 3 * time.Second
)

// WorkspaceCloseAfterFlag is the internal bridge flag that carries
// Presentation.CloseAfter; it goes with --pane-launch only.
const WorkspaceCloseAfterFlag = "--workspace-close-after"

// PaneOutputFlag is the internal bridge flag that carries Presentation.Output; it goes with --pane-launch only.
const PaneOutputFlag = "--pane-output"

// FinishLine is the line the bridge prints when its launcher exits. closeAfter
// is the configured close delay: zero says the Herdr workspace stays open, and a
// negative delay, unknown to the bridge, names only the setting.
func FinishLine(closeAfter time.Duration) string {
	const done = "guildhall: Artisan launcher finished; result and work remain in Guildhall."
	switch {
	case closeAfter == 0:
		return done + " This Herdr workspace stays open (artisan_workspace_close_after is 0); close it yourself when done."
	case closeAfter < 0:
		return done + " This Herdr workspace closes automatically after the configured delay (artisan_workspace_close_after)."
	}
	return fmt.Sprintf("%s This Herdr workspace closes automatically in about %s (artisan_workspace_close_after); set it to 0 to keep Herdr workspaces open.", done, closeAfter)
}

// PaneClaim is the attempt-local presentation launch intent, single-use once it
// holds a tab binding: an ambiguous API call after that leaves it in place, and
// neither a caller restart nor a missing pane is permission to launch this
// attempt again. A launching claim, written before the binding, proves nothing
// was typed: a caller holding the task lock (Process.TaskLocked) that finds one
// with no owner recovers it and launches again.
const PaneClaim = "pane-launch.json"

// WorkspaceLabel is the label of the Herdr workspace an attempt of a one-slice
// task is presented in: "└ <task> · <step> #<n>".
func WorkspaceLabel(k artisan.AttemptKey) string {
	return fmt.Sprintf("└ %s · %s #%d", k.TaskID, k.Step, k.Attempt)
}

// SliceWorkspaceLabel is the label of the Herdr workspace an attempt of a
// multi-PR task of total slices is presented in: "└ <task> · slice k/n ·
// <step> #<n>", the step without its slice mark. For total below 2 it is
// WorkspaceLabel.
func SliceWorkspaceLabel(k artisan.AttemptKey, total int) string {
	if total < 2 {
		return WorkspaceLabel(k)
	}
	step, slice := workflow.ParseSliceStep(k.Step)
	return fmt.Sprintf("└ %s · slice %d/%d · %s #%d", k.TaskID, slice, total, step, k.Attempt)
}

// TabLabel is the label of the tab an attempt runs in: "artisan <key>".
func TabLabel(k artisan.AttemptKey) string { return "artisan " + k.String() }

// AttemptWorkspaceLabels are the labels a Herdr workspace created for attempt
// k alone has carried: WorkspaceLabel, and TabLabel, which releases from #140
// to #145 gave the workspace too. Releases before #140 presented an attempt
// as a tab in a shared workspace, whose label is neither. A multi-PR task's
// SliceWorkspaceLabel names its slice count, which k does not hold, so
// IsAttemptWorkspaceLabel recognises it.
func AttemptWorkspaceLabels(k artisan.AttemptKey) []string {
	return []string{WorkspaceLabel(k), TabLabel(k)}
}

// IsAttemptWorkspaceLabel reports whether label is one AttemptWorkspaceLabels
// lists for k, or SliceWorkspaceLabel of k for any slice count.
func IsAttemptWorkspaceLabel(k artisan.AttemptKey, label string) bool {
	if slices.Contains(AttemptWorkspaceLabels(k), label) {
		return true
	}
	step, slice := workflow.ParseSliceStep(k.Step)
	prefix := fmt.Sprintf("└ %s · slice %d/", k.TaskID, slice)
	rest, ok := strings.CutPrefix(label, prefix)
	if !ok {
		return false
	}
	total, tail, ok := strings.Cut(rest, " · ")
	n, err := strconv.Atoi(total)
	return ok && err == nil && n >= max(2, slice) && strconv.Itoa(n) == total && tail == fmt.Sprintf("%s #%d", step, k.Attempt)
}

// workspaceLabel is the label at's Herdr workspace is created with.
func (a Attempt) workspaceLabel() string { return SliceWorkspaceLabel(a.Key, a.Slices) }

// workspaceLabels are the labels closing at's workspace accepts.
func (a Attempt) workspaceLabels() []string {
	labels := AttemptWorkspaceLabels(a.Key)
	if l := a.workspaceLabel(); !slices.Contains(labels, l) {
		labels = append(labels, l)
	}
	return labels
}

// launchingClaim is the PaneClaim a launch writes before it has a tab binding.
const launchingClaim = "{\"state\":\"launching\"}\n"

// abandonedCloseTimeout bounds a recovering launch's close of the workspace
// its stale launching claim records.
const abandonedCloseTimeout = 20 * time.Second

// launching is a launching claim: state "launching", and the Herdr workspace
// and tabs the launch has created so far, in creation order, the workspace's
// starting tab first. A launching claim proves nothing was typed.
type launching struct {
	State     string           `json:"state"`
	Workspace string           `json:"workspace,omitempty"`
	Tabs      []adapter.TabRef `json:"tabs,omitempty"`
}

// write overwrites the claim at path with c, never removing it.
func (c launching) write(path string) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// staleLaunchingClaim reads the claim at path and reports whether it is
// exactly a launching claim: one JSON object holding only state "launching",
// and optionally the workspace and tabs a launch created, with no tab binding.
// Each recorded tab must be a complete reference into the recorded workspace.
// A legacy {"state":"launching"} claim records nothing.
func staleLaunchingClaim(path string) (launching, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return launching{}, false
	}
	var c launching
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF || c.State != "launching" {
		return launching{}, false
	}
	if len(c.Tabs) > 0 && c.Workspace == "" {
		return launching{}, false
	}
	for _, t := range c.Tabs {
		if t.WorkspaceID != c.Workspace || t.Session == "" || t.TabID == "" || t.PaneID == "" {
			return launching{}, false
		}
	}
	return c, true
}

// closeAbandoned closes the workspace stale records a failed launch of at
// creating, best effort and bounded by abandonedCloseTimeout. It reports
// whether the workspace is closed or already gone, and otherwise why not.
func (pr *Presentation) closeAbandoned(ctx context.Context, at Attempt, stale launching) (bool, string) {
	closer, ok := pr.Host.(adapter.AbandonedWorkspaceCloser)
	if !ok {
		return false, "the terminal host cannot close an abandoned workspace"
	}
	ctx, cancel := context.WithTimeout(ctx, abandonedCloseTimeout)
	defer cancel()
	if err := closer.CloseAbandonedWorkspace(ctx, stale.Workspace, stale.Tabs, at.workspaceLabels(), TabLabel(at.Key)); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (p *Process) launchPane(ctx context.Context, at Attempt, cmd *exec.Cmd, presentation *Presentation) (interp.Handle, error) {
	if presentation.Host == nil || presentation.Report == nil {
		return interp.Handle{}, errors.New("launch: incomplete pane presentation")
	}
	// Reconcile durable ownership before opening any terminal. In particular a
	// live or unverified owner can never be replaced because its pane disappeared.
	if owner, err := at.owner(); err != nil || owner != nil {
		classification, statusErr := Status(ctx, at)
		return interp.Handle{}, fmt.Errorf("launch: %s already owned (%+v): %w", at.Key, classification, errors.Join(err, statusErr, errors.New("reconcile with artisan status/resume before retrying")))
	}
	claimPath := filepath.Join(at.Dir(), PaneClaim)
	claim, err := os.OpenFile(claimPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	var stale launching
	recoverable := false
	if errors.Is(err, fs.ErrExist) && p.TaskLocked {
		stale, recoverable = staleLaunchingClaim(claimPath)
	}
	switch {
	case err == nil:
		if _, err := claim.WriteString(launchingClaim); err != nil {
			_ = claim.Close()
			return interp.Handle{}, err
		}
		if err := claim.Close(); err != nil {
			return interp.Handle{}, err
		}
	case recoverable:
		// The task lock proves the claimant's launch has ended, and a launching
		// claim proves it typed nothing, so no worker exists for that launch.
		// The workspace it recorded creating is closed first, best effort, so
		// the replacement leaves no orphan behind (d91). The claim is
		// overwritten, never removed, so it is never absent.
		closed, reason := false, ""
		if stale.Workspace != "" {
			closed, reason = presentation.closeAbandoned(ctx, at, stale)
		}
		if err := os.WriteFile(claimPath, []byte(launchingClaim), 0o600); err != nil {
			return interp.Handle{}, err
		}
		presentation.recovered(at, stale.Workspace, closed, reason)
	default:
		classification, statusErr := Status(ctx, at)
		return interp.Handle{}, fmt.Errorf("launch: pane launch already claimed or unavailable (%+v): %w", classification, errors.Join(err, statusErr))
	}
	label := TabLabel(at.Key)
	// The launching claim records each workspace and tab as it is created, so a
	// launch that fails before its binding leaves them for recovery to close.
	workspace, err := presentation.Host.CreateWorkspace(ctx, at.workspaceLabel(), cmd.Dir)
	record := launching{State: "launching"}
	if workspace.ID != "" {
		record.Workspace = workspace.ID
		if t := workspace.InitialTab; t.ID() != "" {
			record.Tabs = append(record.Tabs, adapter.TabRef{Session: t.Session(), WorkspaceID: t.WorkspaceID(), TabID: t.ID(), PaneID: t.PaneID()})
		}
		if werr := record.write(claimPath); werr != nil {
			return interp.Handle{}, errors.Join(err, werr)
		}
	}
	if err != nil {
		return interp.Handle{}, err
	}
	l := adapter.TabLauncher{Host: presentation.Host, WorkspaceID: workspace.ID}
	tab, shell, err := l.Open(ctx, label, cmd.Dir, append(slices.Clone(p.Env), at.Key.Env()))
	if tab.TabID != "" {
		record.Tabs = append(record.Tabs, tab)
		if werr := record.write(claimPath); werr != nil {
			return interp.Handle{}, errors.Join(err, werr)
		}
	}
	if err != nil {
		return interp.Handle{}, err
	}
	b, err := json.Marshal(tab)
	if err != nil {
		return interp.Handle{}, err
	}
	if err := os.WriteFile(filepath.Join(at.Dir(), PaneClaim), b, 0o600); err != nil {
		return interp.Handle{}, err
	}
	agent := herdr.AgentReport{Source: "guildhall", Agent: "artisan", State: "working", Resume: ResumeArgv(at)}
	outcome, err := presentation.Report(ctx, tab.PaneID, agent)
	report := artisan.PresentationReport{Pane: tab.PaneID, Exit: outcome.Exit, Stdout: outcome.Stdout, Err: err, Reports: 1}
	if err != nil {
		presentation.log(at, report)
		return interp.Handle{}, err
	}
	argv := append(append([]string(nil), cmd.Args...), "--pane-launch", WorkspaceCloseAfterFlag, presentation.CloseAfter.String(),
		PaneOutputFlag, cmp.Or(presentation.Output, workbench.OutputWorkbench))
	if err := l.Type(ctx, tab, shell, argv); err != nil {
		report.ReadBackErr = errLaunchUnsettled
		presentation.log(at, report)
		// Type may have succeeded despite losing its response. Reconcile files and
		// ownership for diagnosis, but never send a second command or stop a worker.
		classification, statusErr := Status(ctx, at)
		return interp.Handle{}, fmt.Errorf("launch: pane command uncertain (%+v): %w", classification, errors.Join(err, statusErr))
	}
	c := &child{at: at, pane: true, exited: make(chan struct{})}
	if err := p.owned(ctx, c); err != nil {
		report.ReadBackErr = errLaunchUnsettled
		presentation.log(at, report)
		classification, statusErr := Status(ctx, at)
		return interp.Handle{}, fmt.Errorf("launch: pane ownership uncertain (%+v): %w", classification, errors.Join(err, statusErr))
	}
	presentation.settle(ctx, c.exited, agent, &report)
	presentation.log(at, report)
	h := interp.Handle{Identity: at.Identity(), Ref: fmt.Sprintf("%s pid %d", at.Key, c.identity.PID)}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started == nil {
		p.started = map[interp.Handle]*child{}
	}
	p.started[h] = c
	return h, nil
}

var (
	// errLaunchUnsettled is the read-back skipped because typing the bridge
	// command or waiting for its launcher to own the attempt failed.
	errLaunchUnsettled = errors.New("read-back skipped: the launch did not settle")
	// errNoReadBack is the read-back skipped because none is configured.
	errNoReadBack = errors.New("read-back skipped: no read-back configured")
)

// settle reads r's pane back once the launcher owns the attempt and, while the
// pane does not show agent, reports agent again and reads back again, up to
// ReportTries read-backs within reportDeadline. Herdr accepts the first report,
// made while the tab's idle shell is the pane's foreground, and can then drop
// it unless the bridge takes the foreground quickly; a report made with the
// bridge in the foreground holds until it leaves (d49). settle stops at once
// when ctx is done or the launcher has exited, since Herdr clears an agent
// whose process left. Every result is recorded in r and a failure only warns:
// it never fails the launch, types anything or stops anything.
func (pr *Presentation) settle(ctx context.Context, exited <-chan struct{}, agent herdr.AgentReport, r *artisan.PresentationReport) {
	if pr.ReadBack == nil {
		r.ReadBackErr = errNoReadBack
		return
	}
	tries, delay := pr.ReportTries, pr.ReportRetryDelay
	if tries <= 0 {
		tries = DefaultReportTries
	}
	if delay <= 0 {
		delay = DefaultReportRetryDelay
	}
	loopCtx, cancel := context.WithTimeout(ctx, reportDeadline)
	defer cancel()
	for !pr.readBack(loopCtx, agent.Agent, r) {
		if r.ReadBacks >= tries {
			break
		}
		if loopCtx.Err() != nil || launcherExited(exited) {
			break
		}
		if _, err := pr.Report(loopCtx, r.Pane, agent); err != nil {
			r.RetryErr = err
		}
		r.Reports++
		select {
		case <-time.After(delay):
		case <-loopCtx.Done():
		case <-exited:
		}
		if loopCtx.Err() != nil || launcherExited(exited) {
			break
		}
	}
	if shown(agent.Agent, r) || ctx.Err() != nil || launcherExited(exited) {
		return
	}
	status := r.ReadBackAgentStatus
	if r.ReadBackErr != nil {
		status = "unread: " + r.ReadBackErr.Error()
	}
	pr.warn("Herdr pane %s does not show agent %s after %d read-backs (agent_status %s); the attempt runs, but Herdr's tab will show no agent", r.Pane, agent.Agent, r.ReadBacks, status)
}

// readBack reads r's pane back once into r, reporting whether it shows agent;
// a failure is recorded in r and warned about, never returned.
func (pr *Presentation) readBack(ctx context.Context, agent string, r *artisan.PresentationReport) bool {
	r.ReadBacks++
	got, err := pr.ReadBack(ctx, r.Pane)
	if err != nil {
		r.ReadBackErr, r.ReadBackAgentStatus, r.ReadBackAgent, r.ReadBackPane = err, "", "", nil
		pr.warn("read back Herdr pane %s: %v", r.Pane, err)
		return false
	}
	r.ReadBackErr = nil
	r.ReadBackAgentStatus, r.ReadBackAgent, r.ReadBackPane = got.AgentStatus, got.Agent, got.Pane
	if r.ReadBackPane == nil {
		r.ReadBackPane = json.RawMessage("null")
	}
	return shown(agent, r)
}

// shown reports whether r's last read-back succeeded and shows agent: its
// agent is agent and its agent_status is known. unknown is a valid reported
// state, and also what Herdr shows once it has dropped a report.
func shown(agent string, r *artisan.PresentationReport) bool {
	return r.ReadBackErr == nil && r.ReadBackPane != nil && r.ReadBackAgent == agent && r.ReadBackAgentStatus != "unknown"
}

// launcherExited reports whether exited is closed.
func launcherExited(exited <-chan struct{}) bool {
	select {
	case <-exited:
		return true
	default:
		return false
	}
}

// log appends r to at's events as one line; a failure only warns.
func (pr *Presentation) log(at Attempt, r artisan.PresentationReport) {
	if err := artisan.LogPresentationReport(at.Dir(), r); err != nil {
		pr.warn("record Herdr report for %s: %v", at.Key, err)
	}
}

// recovered warns that at's stale launching claim was recovered, and what
// became of the workspace abandoned it recorded, if any, and appends that to
// at's events; a failed event write only warns.
func (pr *Presentation) recovered(at Attempt, abandoned string, closed bool, reason string) {
	switch {
	case abandoned == "":
		pr.warn("recovered a stale launching claim for %s: no command was typed for it; an earlier Herdr workspace %q may have been left behind by a lost create response, so close it by hand", at.Key, at.workspaceLabel())
	case closed:
		pr.warn("recovered a stale launching claim for %s: no command was typed for it; closed the Herdr workspace %s it had created", at.Key, abandoned)
	default:
		pr.warn("recovered a stale launching claim for %s: no command was typed for it; left open the Herdr workspace %s %q it had created (%s), so close it by hand", at.Key, abandoned, at.workspaceLabel(), reason)
	}
	if err := artisan.LogPaneClaimRecovered(at.Dir(), at.workspaceLabel(), abandoned, closed, reason); err != nil {
		pr.warn("record pane claim recovery for %s: %v", at.Key, err)
	}
}

func (pr *Presentation) warn(format string, args ...any) {
	if pr.Warnings != nil {
		_, _ = fmt.Fprintf(pr.Warnings, "guildhall: warning: "+format+"\n", args...)
	}
}
