// Package session is the Director session supervisor's control endpoint (docs/director.md#the-end-of-a-turn).
//
// The supervisor of session n binds one endpoint (docs/contracts/endpoint.md), an AF_UNIX stream socket on Windows
// as on Unix, at EndpointFile in the session's directory, as an attempt's launcher binds its control endpoint. The
// runtime's turn-start hook runs `guildhall director turn-started` and its end-of-turn hook
// `guildhall director turn-ended`, each of which calls Signal; the supervisor receives each signal on
// Endpoint.Turns. The line protocol is one JSON request line and one JSON reply line, each carrying
// "schema": "guildhall.session-control/1" and the session number, which the endpoint package decodes strictly and
// whose session number is the peer check. The endpoint does not authenticate its peers: access control is the
// session directory's.
package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/ndrwl/guildhall/internal/endpoint"
)

// EndpointFile is the endpoint's socket file name in the session directory.
const EndpointFile = "supervisor.sock"

// Errors returned by the session endpoint. Listen's refusals of a leftover file are the endpoint package's
// endpoint.ErrAnswers and endpoint.ErrUnknown.
var (
	// ErrPath refuses a socket path longer than endpoint.MaxPath bytes or a relative session directory.
	ErrPath = errors.New("session endpoint path is unusable")
	// ErrNoSupervisor reports that nothing listens at the session's endpoint: no supervisor runs the session.
	ErrNoSupervisor = errors.New("no supervisor listens at the session endpoint")
	// ErrMismatch reports an endpoint that serves another session than the caller named.
	ErrMismatch = errors.New("the session endpoint serves another session")
	// ErrProtocol reports a reply that is not protocol guildhall.session-control/1.
	ErrProtocol = errors.New("session endpoint reply is not session-control protocol version 1")
)

// sessionProtocol is the session control line protocol, version 1.
var sessionProtocol = endpoint.Protocol{
	Schema: "guildhall.session-control/", Version: 1, MaxRequest: 4 << 10, MaxReply: 4 << 10,
	Mismatch: ErrMismatch, Malformed: ErrProtocol,
}

// Turn is a turn signal, named by its request op.
type Turn string

// The turn signals.
const (
	// TurnStarted is a runtime's turn starting, such as at a prompt the captain submitted.
	TurnStarted Turn = "turn-started"
	// TurnEnded is a runtime's turn ending.
	TurnEnded Turn = "turn-ended"
)

type request struct {
	Schema  string `json:"schema"`
	Op      string `json:"op"`
	Session int    `json:"session"`
	// Waiter is a wait-wake or refresh request's waiter id, and Line a wake request's counts-only line.
	Waiter string `json:"waiter,omitempty"`
	Line   string `json:"line,omitempty"`
}

type reply struct {
	Schema  string `json:"schema"`
	Session int    `json:"session"`
	Error   string `json:"error,omitempty"`
	// Line is the wake line a wait-wake poll was handed, Retired set for a waiter a newer one replaced, and
	// Delivered set for a wake a registered waiter was handed.
	Line      string `json:"line,omitempty"`
	Retired   bool   `json:"retired,omitempty"`
	Delivered bool   `json:"delivered,omitempty"`
}

// EndpointPath returns the endpoint's socket path for the absolute session directory dir: dir/EndpointFile.
// A relative dir, or a path longer than endpoint.MaxPath, is ErrPath.
func EndpointPath(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("session: %w: directory %q is not absolute", ErrPath, dir)
	}
	p := filepath.Join(dir, EndpointFile)
	if len(p) > endpoint.MaxPath {
		return "", fmt.Errorf("session: %w: %q is %d bytes, limit %d", ErrPath, p, len(p), endpoint.MaxPath)
	}
	return p, nil
}

// Endpoint is a bound session endpoint.
type Endpoint struct {
	*endpoint.Endpoint
	turns chan Turn
	mu    sync.Mutex
	w     waiters
}

// Close answers every open wait-wake poll as retired, then stops the endpoint and removes its socket file,
// idempotently.
func (e *Endpoint) Close() error {
	e.w.release()
	return e.Endpoint.Close()
}

// Listen binds the endpoint of session number in the absolute session directory dir. A leftover socket file
// nothing listens on is removed first; one that answers is refused as endpoint.ErrAnswers and any other file there
// as endpoint.ErrUnknown, and kept. The endpoint serves until Close, which stops it and removes the socket file,
// idempotently.
func Listen(dir string, number int) (*Endpoint, error) {
	p, err := EndpointPath(dir)
	if err != nil {
		return nil, err
	}
	e := &Endpoint{turns: make(chan Turn, 1)}
	ep, err := endpoint.Listen[request, reply](p, sessionProtocol, signalServer{number: number, e: e})
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	e.Endpoint = ep
	return e, nil
}

// signalServer serves the turn signals of one session, delivering each to e before replying.
type signalServer struct {
	number int
	e      *Endpoint
}

func (s signalServer) Owns(req request) bool { return req.Session == s.number }

func (s signalServer) Refuse(msg string) reply {
	return reply{Schema: sessionProtocol.Current(), Session: s.number, Error: msg}
}

func (s signalServer) Serve(req request) reply {
	switch Turn(req.Op) {
	case TurnStarted, TurnEnded:
	default:
		if req.Op == OpWaitWake || req.Op == OpWake || req.Op == OpRefresh {
			return s.serveWaiter(req)
		}
		return s.Refuse(fmt.Sprintf("unknown op %q", req.Op))
	}
	s.e.deliver(Turn(req.Op))
	return reply{Schema: sessionProtocol.Current(), Session: s.number}
}

// deliver replaces any unread signal with t, so Turns holds the newest.
func (e *Endpoint) deliver(t Turn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-e.turns:
	default:
	}
	e.turns <- t
}

// Turns receives the turn signals the endpoint has answered, in order. It holds one: a signal that arrives while
// another is unread replaces it.
func (e *Endpoint) Turns() <-chan Turn { return e.turns }

// Signal sends turn to the supervisor of session number, listening in the absolute session directory dir, and
// returns once the endpoint has answered. Nothing listening is
// ErrNoSupervisor, an endpoint serving another session ErrMismatch, and a malformed reply ErrProtocol.
// ctx bounds the dial and the exchange.
func Signal(ctx context.Context, dir string, number int, turn Turn) error {
	p, err := EndpointPath(dir)
	if err != nil {
		return err
	}
	req := request{Schema: sessionProtocol.Current(), Op: string(turn), Session: number}
	rep, err := endpoint.Call(ctx, p, sessionProtocol, req, func(rep reply) bool { return rep.Session == number })
	switch {
	case errors.Is(err, endpoint.ErrNotListening):
		return fmt.Errorf("session: %w: %w", ErrNoSupervisor, err)
	case errors.Is(err, ErrMismatch):
		return fmt.Errorf("session: %w; it serves session %d, not %d", err, rep.Session, number)
	case err != nil:
		return fmt.Errorf("session: %w", err)
	case rep.Error != "":
		return fmt.Errorf("session: endpoint %s refused the %s signal: %s", p, turn, rep.Error)
	}
	return nil
}
