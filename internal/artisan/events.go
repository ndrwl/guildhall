package artisan

type eventName uint8

const (
	eventCancelMarker eventName = iota
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
