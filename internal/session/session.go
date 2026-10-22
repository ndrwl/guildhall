// Package session is the Director session supervisor's control endpoint (docs/director.md#the-end-of-a-turn).
//
// The supervisor of session n binds one AF_UNIX stream socket, on Windows as on Unix, at EndpointFile in the
// session's directory, as an attempt's launcher binds its control endpoint. The runtime's end-of-turn hook runs
// `guildhall director turn-ended`, which calls SignalTurnEnded; the supervisor receives each signal on
// Endpoint.TurnEnded. The line protocol is one JSON request line and one JSON reply line, each carrying
// "schema": "guildhall.session-control/1" and the session number, decoded strictly as the attempt endpoint's
// are. The endpoint does not authenticate its peers: access control is the session directory's.
package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

// EndpointFile is the endpoint's socket file name in the session directory.
const EndpointFile = "supervisor.sock"

// Errors returned by the session endpoint.
var (
	// ErrPath refuses a socket path longer than artisan.MaxEndpointPath bytes or a relative session directory.
	ErrPath = errors.New("session endpoint path is unusable")
	// ErrNoSupervisor reports that nothing listens at the session's endpoint: no supervisor runs the session.
	ErrNoSupervisor = errors.New("no supervisor listens at the session endpoint")
	// ErrMismatch reports an endpoint that serves another session than the caller named.
	ErrMismatch = errors.New("the session endpoint serves another session")
	// ErrProtocol reports a reply that is not protocol guildhall.session-control/1.
	ErrProtocol = errors.New("session endpoint reply is not session-control protocol version 1")
)

// Limits and version of the line protocol.
const (
	schemaPrefix    = "guildhall.session-control/"
	schemaV1        = "guildhall.session-control/1"
	opTurnEnded     = "turn-ended"
	maxLineBytes    = 4 << 10
	exchangeTimeout = 5 * time.Second
	leftoverTimeout = time.Second
)

type request struct {
	Schema  string `json:"schema"`
	Op      string `json:"op"`
	Session int    `json:"session"`
}

type reply struct {
	Schema  string `json:"schema"`
	Session int    `json:"session"`
	Error   string `json:"error,omitempty"`
}

func lineOptions() strictjson.Options {
	return strictjson.Options{Schema: schemaPrefix, Version: 1, MaxBytes: maxLineBytes}
}

// EndpointPath returns the endpoint's socket path for the absolute session directory dir: dir/EndpointFile.
// A relative dir, or a path longer than artisan.MaxEndpointPath, is ErrPath.
func EndpointPath(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("session: %w: directory %q is not absolute", ErrPath, dir)
	}
	p := filepath.Join(dir, EndpointFile)
	if len(p) > artisan.MaxEndpointPath {
		return "", fmt.Errorf("session: %w: %q is %d bytes, limit %d", ErrPath, p, len(p), artisan.MaxEndpointPath)
	}
	return p, nil
}

// Endpoint is a bound session endpoint.
type Endpoint struct {
	path     string
	number   int
	ln       *net.UnixListener
	ended    chan struct{}
	once     sync.Once
	closeErr error
}

// Listen binds the endpoint of session number in the absolute session directory dir. A leftover socket file
// nothing listens on is removed first; one that answers, or any other file there, is refused and kept.
// The endpoint serves until Close.
func Listen(dir string, number int) (*Endpoint, error) {
	p, err := EndpointPath(dir)
	if err != nil {
		return nil, err
	}
	if err := removeStale(p); err != nil {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("session: bind endpoint: %w", err)
	}
	e := &Endpoint{path: p, number: number, ln: ln, ended: make(chan struct{}, 1)}
	go e.serve()
	return e, nil
}

// removeStale removes a leftover socket at path that nothing listens on and refuses anything else there.
func removeStale(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("session: leftover endpoint: %w", err)
	case fi.Mode().Type() != fs.ModeSocket:
		return fmt.Errorf("session: leftover endpoint %s is not a socket; kept", path)
	}
	c, err := net.DialTimeout("unix", path, leftoverTimeout)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("session: an endpoint already answers at %s; kept", path)
	}
	if !isRefused(err) {
		return fmt.Errorf("session: leftover endpoint %s cannot be proven stale: %w; kept", path, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("session: remove stale endpoint: %w", err)
	}
	return nil
}

func (e *Endpoint) serve() {
	for {
		c, err := e.ln.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			// A transient accept failure, such as running out of descriptors.
			time.Sleep(10 * time.Millisecond)
			continue
		}
		go e.answer(c)
	}
}

// answer reads one request line from c and writes one reply line, delivering a turn end before replying.
func (e *Endpoint) answer(c net.Conn) {
	defer func() { _ = c.Close() }()
	rep := reply{Schema: schemaV1, Session: e.number}
	_ = c.SetReadDeadline(time.Now().Add(exchangeTimeout))
	var req request
	line, err := bufio.NewReader(io.LimitReader(c, maxLineBytes)).ReadBytes('\n')
	if err == nil {
		err = strictjson.Decode(line, &req, lineOptions())
	}
	switch {
	case err != nil:
		rep.Error = fmt.Sprintf("malformed request: one %s JSON line of at most %d bytes is required: %v", schemaV1, maxLineBytes, err)
	case req.Op != opTurnEnded:
		rep.Error = fmt.Sprintf("unknown op %q", req.Op)
	case req.Session != e.number:
		rep.Error = ErrMismatch.Error()
	default:
		select {
		case e.ended <- struct{}{}:
		default: // an unread turn end already stands for this one
		}
	}
	_ = c.SetWriteDeadline(time.Now().Add(exchangeTimeout))
	_ = json.NewEncoder(c).Encode(rep)
}

// TurnEnded receives one value for each turn-ended request the endpoint has answered. It holds one: requests
// that arrive while a value is unread coalesce into it.
func (e *Endpoint) TurnEnded() <-chan struct{} { return e.ended }

// Path returns the socket path the endpoint is bound at.
func (e *Endpoint) Path() string { return e.path }

// Close stops accepting requests and removes the socket file. It is idempotent.
func (e *Endpoint) Close() error {
	e.once.Do(func() {
		e.closeErr = e.ln.Close()
		if err := os.Remove(e.path); err != nil && !errors.Is(err, fs.ErrNotExist) && e.closeErr == nil {
			e.closeErr = err
		}
	})
	return e.closeErr
}

// SignalTurnEnded tells the supervisor of session number, listening in the absolute session directory dir,
// that the runtime's turn has ended, and returns once the endpoint has answered. Nothing listening is
// ErrNoSupervisor, an endpoint serving another session ErrMismatch, and a malformed reply ErrProtocol.
// ctx bounds the dial and the exchange.
func SignalTurnEnded(ctx context.Context, dir string, number int) error {
	p, err := EndpointPath(dir)
	if err != nil {
		return err
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || isRefused(err) {
			return fmt.Errorf("session: %w: %s", ErrNoSupervisor, p)
		}
		return fmt.Errorf("session: endpoint %s: %w", p, err)
	}
	defer func() { _ = c.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Now()) })
	defer stop()
	if err := json.NewEncoder(c).Encode(request{Schema: schemaV1, Op: opTurnEnded, Session: number}); err != nil {
		return fmt.Errorf("session: endpoint %s: %w", p, err)
	}
	line, err := bufio.NewReader(io.LimitReader(c, maxLineBytes)).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("session: endpoint %s: %w", p, err)
	}
	var rep reply
	if err := strictjson.Decode(line, &rep, lineOptions()); err != nil {
		return fmt.Errorf("session: %w: %s: %w", ErrProtocol, p, err)
	}
	switch {
	case rep.Session != number:
		return fmt.Errorf("session: %w: %s serves session %d, not %d", ErrMismatch, p, rep.Session, number)
	case rep.Error != "":
		return fmt.Errorf("session: endpoint %s refused the turn end: %s", p, rep.Error)
	}
	return nil
}
