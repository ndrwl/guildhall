// Package workbench is the Workbench: the formatted, read-only live display of one Artisan attempt in its Herdr
// pane (docs/artisan.md#workbench). A runtime wrapper writes adapter-neutral stream events to File in the attempt
// directory; the pane bridge merges them with launcher.log and renders both. launcher.log stays the plain and
// complete record of the attempt: nothing here writes to it.
package workbench

import (
	"encoding/json"
	"io"
	"time"
)

// File is the event file a runtime wrapper writes in the attempt directory, beside the result file and outside
// the model directory.
const File = "workbench.jsonl"

// Schema and Version name the shape of one event line.
const (
	Schema  = "guildhall.workbench-event"
	Version = 1
)

// MaxEventBytes bounds one event line, as the Claude wrapper bounds one stream-json line; a larger event is not
// written. MaxFileBytes bounds the whole event file: at the cap the writer writes one KindTruncated event and stops.
const (
	MaxEventBytes = 4 << 20
	MaxFileBytes  = 64 << 20
)

// Kind is what one event reports.
type Kind string

// Event kinds.
const (
	// KindText is assistant text, already stripped of control characters.
	KindText Kind = "text"
	// KindToolCall is one tool call: its id, tool name and short subject.
	KindToolCall Kind = "tool_call"
	// KindToolResult is the result of the call with the same id: whether it is an error, and its size.
	KindToolResult Kind = "tool_result"
	// KindEnd is written once the runtime exits, with its exit status.
	KindEnd Kind = "end"
	// KindTruncated is the last event of a file that reached MaxFileBytes.
	KindTruncated Kind = "truncated"
)

// Event is one line of File. Text and tool-call events carry Log, the exact plain lines the wrapper writes to its
// stdout, and so to launcher.log, for the event; the bridge uses them to show the event instead of those lines.
type Event struct {
	Schema  string    `json:"schema"`
	Version int       `json:"version"`
	Seq     int64     `json:"seq"`
	Time    time.Time `json:"time"`
	Kind    Kind      `json:"kind"`
	Text    string    `json:"text,omitempty"`
	ID      string    `json:"id,omitempty"`
	Name    string    `json:"name,omitempty"`
	Subject string    `json:"subject,omitempty"`
	Error   *bool     `json:"error,omitempty"`
	Bytes   *int64    `json:"bytes,omitempty"`
	Lines   *int      `json:"lines,omitempty"`
	Exit    *int      `json:"exit,omitempty"`
	Log     []string  `json:"log,omitempty"`
}

// Text is a KindText event for text, which the wrapper wrote to stdout as log.
func Text(text string, log []string) Event { return Event{Kind: KindText, Text: text, Log: log} }

// ToolCall is a KindToolCall event, which the wrapper wrote to stdout as log.
func ToolCall(id, name, subject string, log []string) Event {
	return Event{Kind: KindToolCall, ID: id, Name: name, Subject: subject, Log: log}
}

// ToolResult is a KindToolResult event for the call id.
func ToolResult(id string, isError bool, size int64, lines int) Event {
	return Event{Kind: KindToolResult, ID: id, Error: &isError, Bytes: &size, Lines: &lines}
}

// End is a KindEnd event for a runtime that exited with status exit.
func End(exit int) Event { return Event{Kind: KindEnd, Exit: &exit} }

// Writer writes events to an event file, bounded by MaxEventBytes per event and Limit in all. Write errors are
// ignored, as a presentation sink's are: after one, the writer writes nothing more, so a torn line is never
// followed by another. A nil *Writer writes nothing. It is not safe for concurrent use.
type Writer struct {
	w io.Writer
	// Now stamps each event; nil is time.Now.
	Now func() time.Time
	// Limit is the file cap; zero is MaxFileBytes.
	Limit   int64
	seq     int64
	written int64
	stopped bool
}

// NewWriter returns a Writer appending to w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// truncatedReserve is room kept below Limit for the KindTruncated event.
const truncatedReserve = 256

// Write stamps e with the schema, the next sequence number and the time, and writes it as one line, unless it
// exceeds MaxEventBytes. An event that would take the file past its limit is replaced by a KindTruncated event,
// the last one written.
func (w *Writer) Write(e Event) {
	if w == nil || w.stopped || w.w == nil {
		return
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	limit := w.Limit
	if limit <= 0 {
		limit = MaxFileBytes
	}
	w.seq++
	e.Schema, e.Version, e.Seq, e.Time = Schema, Version, w.seq, now()
	line, err := json.Marshal(e)
	if err != nil || len(line)+1 > MaxEventBytes {
		return
	}
	if w.written+int64(len(line))+1 > limit-truncatedReserve {
		w.stopped = true
		line, err = json.Marshal(Event{Schema: Schema, Version: Version, Seq: w.seq, Time: e.Time, Kind: KindTruncated})
		if err != nil {
			return
		}
	}
	n, err := w.w.Write(append(line, '\n'))
	w.written += int64(n)
	if err != nil {
		w.stopped = true
	}
}

// Decode parses one event line leniently: unknown fields are ignored, and a line that is not a version 1
// workbench event, or is malformed, is refused.
func Decode(line []byte) (Event, bool) {
	var e Event
	if json.Unmarshal(line, &e) != nil || e.Schema != Schema || e.Version != Version || e.Kind == "" {
		return Event{}, false
	}
	return e, true
}

// ReadAll decodes every event of r in order, skipping malformed and oversized lines.
func ReadAll(r io.Reader) []Event {
	t := lineTail{limit: MaxEventBytes}
	data, _ := io.ReadAll(r)
	var out []Event
	for _, line := range t.feed(data) {
		if e, ok := Decode(line); ok {
			out = append(out, e)
		}
	}
	return out
}
