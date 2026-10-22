package artisan

type eventName uint8

const (
	eventAdvance eventName = iota
	eventCancelMarker
	eventDecideFailed
	eventEnded
	eventHeartbeatFailed
	eventLadder
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
