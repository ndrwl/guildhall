package workbench

import (
	"path/filepath"
	"time"
)

// Merger renders launcher.log's lines and the event file's events as one stream (docs/artisan.md#workbench).
// Events render in file order. An event with Log lines waits until those exact lines arrive next from
// launcher.log; they are consumed and the event is rendered instead of them. An event without Log lines renders
// as soon as every earlier event has. A launcher.log line no pending event claims is rendered as another line.
// Because a wrapper writes each event before its stdout, nothing is shown twice and nothing is lost. If a line
// opens a later event's lines while an earlier event still waits, the earlier event's lines were lost, so the
// stalled events render then and the later event takes the line. Without an event file, or after a KindTruncated event, lines are rendered as Renderer.Plain does.
type Merger struct {
	r       *Renderer
	pending []Event
	matched int
	plain   bool
}

// NewMerger returns a Merger rendering with r.
func NewMerger(r *Renderer) *Merger { return &Merger{r: r} }

// Events queues events read from the event file, in order, and renders those now due.
func (m *Merger) Events(events ...Event) {
	if m.plain {
		return
	}
	m.pending = append(m.pending, events...)
	m.drain()
}

// drain renders each pending event at the head of the queue that claims no further launcher.log lines.
func (m *Merger) drain() {
	for len(m.pending) > 0 && m.matched == len(m.pending[0].Log) {
		e := m.pending[0]
		m.pending, m.matched = m.pending[1:], 0
		m.r.Event(e)
		if e.Kind == KindTruncated {
			m.plain, m.pending = true, nil
		}
	}
}

// Line renders or consumes one complete launcher.log line. events says whether the event file exists; a line
// read with none is rendered plainly, since a wrapper writing events creates the file before its first line.
func (m *Merger) Line(line string, events bool) {
	switch {
	case m.plain || !events:
		m.r.Plain(line)
	case len(m.pending) > 0 && m.pending[0].Log[m.matched] == line:
		m.matched++
		m.drain()
	case m.resync(line):
	default:
		m.r.Other(line)
	}
}

// resync handles a line that opens a later pending event's lines while an earlier event still waits for its own.
// A wrapper writes its lines in event order, so the earlier event's lines will never come (its stdout write
// failed): the stalled events render now, rather than holding every later event behind them and then showing
// those again at Flush, and line is consumed as the later event's first. It reports whether it consumed line.
func (m *Merger) resync(line string) bool {
	for j := 1; j < len(m.pending); j++ {
		if log := m.pending[j].Log; len(log) == 0 || log[0] != line {
			continue
		}
		for rest := len(m.pending) - j; len(m.pending) > rest; {
			m.matched = len(m.pending[0].Log)
			m.drain()
		}
		if m.plain {
			m.r.Plain(line)
			return true
		}
		m.matched = 1
		m.drain()
		return true
	}
	return false
}

// Flush renders every event still pending, whose lines never reached launcher.log, so none is lost.
func (m *Merger) Flush() {
	for len(m.pending) > 0 {
		m.matched = len(m.pending[0].Log)
		m.drain()
	}
}

// QuietAfter is how long a pane goes with no new output before its first quiet line; each later one comes after
// twice the quiet so far: 1m, 2m, 4m, 8m.
const QuietAfter = time.Minute

// Quiet tracks when output last arrived live, never from event timestamps, so replayed history never makes a
// quiet line.
type Quiet struct {
	last time.Time
	next time.Duration
}

// Arrived records that new output reached the pane at now.
func (q *Quiet) Arrived(now time.Time) { q.last, q.next = now, QuietAfter }

// Due reports the quiet line due at now, if any: the quiet so far, at QuietAfter and each doubling after.
func (q *Quiet) Due(now time.Time) (time.Duration, bool) {
	if q.next == 0 || now.Sub(q.last) < q.next {
		return 0, false
	}
	d := q.next
	q.next *= 2
	return d, true
}

// Session tails one attempt's launcher.log and event file into a Merger.
type Session struct {
	r      *Renderer
	m      *Merger
	q      Quiet
	log    fileTail
	events fileTail
	lines  lineTail
	evs    lineTail
	seen   bool
}

// NewSession returns a Session over logPath and the event file in dir, rendering with r. Its quiet clock starts
// at now.
func NewSession(r *Renderer, logPath, dir string, now time.Time) *Session {
	s := &Session{r: r, m: NewMerger(r),
		log: fileTail{path: logPath}, events: fileTail{path: filepath.Join(dir, File)},
		lines: lineTail{limit: MaxEventBytes, split: true}, evs: lineTail{limit: MaxEventBytes}}
	s.q.Arrived(now)
	return s
}

// Poll renders everything appended since the last poll, launcher.log first and then the event file, so every
// wrapper line read has its event read too, and then a quiet line if one is due at now. It returns the
// renderer's first output error.
func (s *Session) Poll(now time.Time) error {
	for {
		data, _ := s.log.read()
		if len(data) == 0 {
			break
		}
		s.q.Arrived(now)
		lines := s.lines.feed(data)
		s.readEvents(now)
		for _, l := range lines {
			s.m.Line(string(l), s.seen)
		}
	}
	s.readEvents(now)
	if d, ok := s.q.Due(now); ok {
		s.r.Quiet(d)
	}
	return s.r.Err()
}

func (s *Session) readEvents(now time.Time) {
	for {
		data, ok := s.events.read()
		s.seen = s.seen || ok
		if len(data) == 0 {
			return
		}
		s.q.Arrived(now)
		for _, l := range s.evs.feed(data) {
			if e, ok := Decode(l); ok {
				s.m.Events(e)
			}
		}
	}
}

// Finish renders what is left once the launcher has exited: a last line with no newline, and any pending event.
// It closes the session's files whether or not rendering succeeds.
func (s *Session) Finish(now time.Time) error {
	defer s.Close()
	if err := s.Poll(now); err != nil {
		return err
	}
	if l, ok := s.lines.rest(); ok {
		s.m.Line(string(l), s.seen)
	}
	s.m.Flush()
	return s.r.Err()
}

// Close closes the files the session tails, which Windows cannot remove while they are open. It is safe to call
// more than once; the session reads nothing more afterwards.
func (s *Session) Close() {
	s.log.close()
	s.events.close()
}
