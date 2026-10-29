package artisan

import (
	"bytes"
	"encoding/json"
)

type eventName uint8

const (
	eventAdvance eventName = iota
	eventCancelMarker
	eventDecideFailed
	eventEnded
	eventHeartbeatFailed
	eventLadder
	eventPaneClaimRecovered
	eventPresentationReport
	eventResultAccepted
	eventRung
	eventRungEmptied
	eventRungSkipped
	eventRungTimeout
	eventRunning
	eventSettled
	eventSignal
	eventSignalForwarded
	eventStopAfterDecision
	eventStopBeforeStart
	eventWorkerExited
	eventWorkerOutlivedResult
)

var eventNames = []eventName{
	eventAdvance,
	eventCancelMarker,
	eventDecideFailed,
	eventEnded,
	eventHeartbeatFailed,
	eventLadder,
	eventPaneClaimRecovered,
	eventPresentationReport,
	eventResultAccepted,
	eventRung,
	eventRungEmptied,
	eventRungSkipped,
	eventRungTimeout,
	eventRunning,
	eventSettled,
	eventSignal,
	eventSignalForwarded,
	eventStopAfterDecision,
	eventStopBeforeStart,
	eventWorkerExited,
	eventWorkerOutlivedResult,
}

func (n eventName) String() string {
	switch n {
	case eventAdvance:
		return "advance"
	case eventCancelMarker:
		return "cancel-marker"
	case eventDecideFailed:
		return "decide-failed"
	case eventEnded:
		return "ended"
	case eventHeartbeatFailed:
		return "heartbeat-failed"
	case eventLadder:
		return "ladder"
	case eventPaneClaimRecovered:
		return "pane-claim-recovered"
	case eventPresentationReport:
		return "presentation-report"
	case eventResultAccepted:
		return "result-accepted"
	case eventRung:
		return "rung"
	case eventRungEmptied:
		return "rung-emptied"
	case eventRungSkipped:
		return "rung-skipped"
	case eventRungTimeout:
		return "rung-timeout"
	case eventRunning:
		return "running"
	case eventSettled:
		return "settled"
	case eventSignal:
		return "signal"
	case eventSignalForwarded:
		return "signal-forwarded"
	case eventStopAfterDecision:
		return "stop-after-decision"
	case eventStopBeforeStart:
		return "stop-before-start"
	case eventWorkerExited:
		return "worker-exited"
	case eventWorkerOutlivedResult:
		return "worker-outlived-result"
	}
	panic("unknown launcher event")
}

// LogAdvance appends an "advance" event to EventsFile in the attempt
// directory dir: the launcher's advance of the attempt's task ran command,
// such as "step done", which exited with exit, or did not start when exit is
// negative, and failed with err when err is not nil. It returns the error of
// the write.
func LogAdvance(dir, command string, exit int, err error) error {
	fields := map[string]any{"command": command}
	if exit >= 0 {
		fields["exit"] = exit
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	l := &Launcher{c: LauncherConfig{Dir: dir}}
	l.event(eventAdvance, fields)
	return l.logErr
}

// LogPaneClaimRecovered appends a "pane-claim-recovered" event to EventsFile
// in the attempt directory dir: a pane launch holding the task lock found the
// attempt's stale launching claim, with no owner, and launched over it; an
// earlier Herdr workspace labelled workspace may have been left behind by a
// lost create response. It is appended like LogPresentationReport and returns
// the error of the write.
func LogPaneClaimRecovered(dir, workspace string) error {
	l := &Launcher{c: LauncherConfig{Dir: dir}}
	l.event(eventPaneClaimRecovered, map[string]any{"workspace": workspace})
	return l.logErr
}

// PresentationReportLimit is the most bytes of Herdr's stdout, and of the
// read-back pane, a "presentation-report" event keeps.
const PresentationReportLimit = 4 << 10

// PresentationReport is what registering an attempt's pane with Herdr
// returned, for diagnosis only.
type PresentationReport struct {
	// Pane is the pane id reported.
	Pane string
	// Exit is Herdr's exit code, or -1 when it did not run or was stopped.
	Exit int
	// Stdout is Herdr's stdout.
	Stdout []byte
	// Err is the report's error, when it failed.
	Err error
	// ReadBackAgentStatus and ReadBackPane are the pane's agent_status and
	// raw pane object read back once after the report; ReadBackPane is nil
	// when nothing was read back.
	ReadBackAgentStatus string
	ReadBackPane        json.RawMessage
	// ReadBackErr says why the read-back failed or was skipped.
	ReadBackErr error
}

// LogPresentationReport appends a "presentation-report" event to EventsFile
// in the attempt directory dir, truncating Herdr's stdout and the read-back
// pane to PresentationReportLimit. It is appended by the process that starts
// a pane launch, as one write of one whole line, without the attempt lock.
// It returns the error of the write.
func LogPresentationReport(dir string, r PresentationReport) error {
	fields := map[string]any{"pane": r.Pane, "exit": r.Exit}
	stdout := r.Stdout
	if len(stdout) > PresentationReportLimit {
		stdout = stdout[:PresentationReportLimit]
		fields["stdout_truncated"] = true
	}
	fields["stdout"] = string(stdout)
	if r.Err != nil {
		fields["error"] = r.Err.Error()
	}
	if r.ReadBackPane != nil {
		fields["readback_agent_status"] = r.ReadBackAgentStatus
		pane := r.ReadBackPane
		var compact bytes.Buffer
		if json.Compact(&compact, pane) == nil {
			pane = compact.Bytes()
		}
		if len(pane) <= PresentationReportLimit && json.Valid(pane) {
			fields["readback_pane"] = json.RawMessage(pane)
		} else {
			fields["readback_pane"] = string(pane[:min(len(pane), PresentationReportLimit)])
		}
	}
	if r.ReadBackErr != nil {
		fields["readback_error"] = r.ReadBackErr.Error()
	}
	l := &Launcher{c: LauncherConfig{Dir: dir}}
	l.event(eventPresentationReport, fields)
	return l.logErr
}
