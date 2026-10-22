// Package endpoint is Guildhall's one control endpoint (docs/contracts/endpoint.md): an AF_UNIX stream socket, on
// Windows as on Unix, serving a versioned JSON line protocol, one request line answered by one reply line. Its
// consumers, the attempt control endpoint (artisan.Listen and artisan.Call) and the Director session's turn signals
// (session.Listen and session.Signal), each name their protocol and their owner; the endpoint owns binding, the
// stale-file rule, permissions, strict decoding, the peer check, the exchange bound and removal.
//
// The endpoint does not authenticate its peers: access control is the directory's. Its directory is created 0700
// and, on Unix, its socket made 0600.
package endpoint

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
	"strconv"
	"sync"
	"time"

	"github.com/ndrwl/guildhall/internal/strictjson"
)

// Errors returned by Listen and Call.
var (
	// ErrPath refuses a socket path longer than MaxPath bytes; nothing is created.
	ErrPath = errors.New("endpoint path exceeds the AF_UNIX path limit")
	// ErrAnswers refuses to bind over a socket that accepts connections; the socket file is kept.
	ErrAnswers = errors.New("an endpoint already answers at the path")
	// ErrUnknown refuses to bind over a leftover file that cannot be proven stale: one that is not a socket, such
	// as a regular file or a directory, or a socket that refuses access; the file is kept.
	ErrUnknown = errors.New("leftover endpoint file cannot be proven stale")
	// ErrNotListening reports a Call that found no socket file at the path, or one nothing listens on. It says
	// nothing about whether the owner is alive: a silent endpoint never proves exit.
	ErrNotListening = errors.New("nothing listens at the endpoint")
)

// MaxPath is the longest socket path, in bytes, this platform's AF_UNIX address holds with its terminating NUL:
// 103 on macOS, 107 on Linux and Windows.
const MaxPath = maxPath

const (
	// leftoverTimeout bounds the connection attempt that tests a leftover file.
	leftoverTimeout = time.Second
	// acceptRetry is the pause after a transient accept failure, such as running out of descriptors.
	acceptRetry = 10 * time.Millisecond
)

// exchangeTimeout bounds client connect and request write, and server request read and reply write. It is a variable
// only so the package's tests can shorten it.
var exchangeTimeout = 5 * time.Second

// Protocol is one consumer's line protocol.
type Protocol struct {
	// Schema prefixes the version in the "schema" field of every request and reply, such as
	// "guildhall.attempt-control/".
	Schema string
	// Version is the one version the consumer speaks; a newer one is refused as strictjson.ErrNewerSchema.
	Version int
	// MaxRequest and MaxReply bound a request and a reply line, in bytes.
	MaxRequest, MaxReply int
	// Mismatch is the consumer's error for a peer that names another owner: the endpoint answers such a request
	// with its text and Call wraps it for a reply naming another owner.
	Mismatch error
	// Malformed is the consumer's error for a reply that is not its protocol.
	Malformed error
}

// Current is the schema every request and reply carries: Schema then Version.
func (p Protocol) Current() string { return p.Schema + strconv.Itoa(p.Version) }

func (p Protocol) options(max int) strictjson.Options {
	return strictjson.Options{Schema: p.Schema, Version: p.Version, MaxBytes: max}
}

// Server is a consumer behind an endpoint, serving requests Req with replies Rep, each a JSON object whose "schema"
// field the consumer sets to its Protocol's Current. The endpoint calls it concurrently, one call per request, and
// replies when the call returns, so a request that blocks does not delay another.
type Server[Req, Rep any] interface {
	// Owns reports whether a strictly decoded request names this endpoint's owner.
	Owns(req Req) bool
	// Serve answers a request that names the owner, refusals of its own included, such as an unknown operation.
	Serve(req Req) Rep
	// Refuse is the reply naming the owner and carrying the refusal msg, for a request the endpoint refuses before
	// Serve.
	Refuse(msg string) Rep
}

// Endpoint is a bound endpoint.
type Endpoint struct {
	path     string
	ln       net.Listener
	once     sync.Once
	closeErr error
}

// Listen binds an endpoint at path speaking p for s and serves until Close. A path longer than MaxPath is ErrPath.
// Its directory is created 0700 when missing and, on Unix, the socket is made 0600.
//
// A leftover file at path is bound over only once proven stale: a socket that refuses the connection, because
// nothing listens on it, is removed first. A socket that accepts the connection is ErrAnswers and anything else,
// such as a regular file, a directory or a socket that refuses access, is ErrUnknown; either is kept.
//
// Each connection is one exchange: one request line of at most p.MaxRequest bytes, decoded with strictjson under
// p's schema and version, is answered by one reply line. A request that is not one such line, including a missing,
// unversioned or newer schema, a duplicate key and an undeclared field at any depth, is answered with s.Refuse and
// never reaches Owns or Serve; one that names another owner is answered with s.Refuse carrying p.Mismatch's text
// and never reaches Serve. A peer that sends no complete line within exchangeTimeout, or does not read its reply
// within it, is refused and disconnected.
func Listen[Req, Rep any](path string, p Protocol, s Server[Req, Rep]) (*Endpoint, error) {
	if len(path) > MaxPath {
		return nil, fmt.Errorf("endpoint: %w: %q is %d bytes, limit %d", ErrPath, path, len(path), MaxPath)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("endpoint: directory: %w", err)
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("endpoint: bind: %w", err)
	}
	if err := restrict(path); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("endpoint: restrict: %w", err)
	}
	e := &Endpoint{path: path, ln: ln}
	go serve(ln, p, s)
	return e, nil
}

// removeStale applies the stale-file rule to whatever is at path.
func removeStale(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("endpoint: %w: %w", ErrUnknown, err)
	case fi.Mode().Type() != fs.ModeSocket:
		return fmt.Errorf("endpoint: %w: %s is not a socket; kept", ErrUnknown, path)
	}
	c, err := net.DialTimeout("unix", path, leftoverTimeout)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("endpoint: %w: %s; kept", ErrAnswers, path)
	}
	if !isRefused(err) {
		return fmt.Errorf("endpoint: %w: %s: %w; kept", ErrUnknown, path, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("endpoint: remove stale socket: %w", err)
	}
	return nil
}

func serve[Req, Rep any](ln net.Listener, p Protocol, s Server[Req, Rep]) {
	for {
		c, err := ln.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			time.Sleep(acceptRetry)
			continue
		}
		go answer(c, p, s)
	}
}

// answer reads one request line from c and writes one reply line.
func answer[Req, Rep any](c net.Conn, p Protocol, s Server[Req, Rep]) {
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(exchangeTimeout))
	var req Req
	line, err := bufio.NewReader(io.LimitReader(c, int64(p.MaxRequest))).ReadBytes('\n')
	if err == nil {
		err = strictjson.Decode(line, &req, p.options(p.MaxRequest))
	}
	var rep Rep
	switch {
	case errors.Is(err, strictjson.ErrNewerSchema):
		rep = s.Refuse(fmt.Sprintf("unsupported protocol version: %v", err))
	case err != nil:
		rep = s.Refuse(fmt.Sprintf("malformed request: one %s JSON line of at most %d bytes is required: %v", p.Current(), p.MaxRequest, err))
	case !s.Owns(req):
		rep = s.Refuse(p.Mismatch.Error())
	default:
		rep = s.Serve(req)
	}
	_ = c.SetWriteDeadline(time.Now().Add(exchangeTimeout))
	_ = json.NewEncoder(c).Encode(rep)
}

// Path returns the socket path the endpoint is bound at.
func (e *Endpoint) Path() string { return e.path }

// Close stops accepting requests and removes the socket file; requests being answered still get their reply. It
// is idempotent.
func (e *Endpoint) Close() error {
	e.once.Do(func() {
		e.closeErr = e.ln.Close()
		if err := os.Remove(e.path); err != nil && !errors.Is(err, fs.ErrNotExist) && e.closeErr == nil {
			e.closeErr = err
		}
	})
	return e.closeErr
}

// Call sends req to the endpoint at path speaking p and returns its reply, which must be strictly p in one line of at
// most p.MaxReply bytes, otherwise p.Malformed, also matching strictjson.ErrNewerSchema for a newer version, and must name the owner the caller
// expects, as owns reports, otherwise p.Mismatch with the reply. No socket at path, or one nothing listens on, is
// ErrNotListening. Connect and request write are also bounded by exchangeTimeout. Waiting for the handler's
// reply is bounded only by ctx, allowing stop to finish its ladder. Once ctx is done the error wraps its error.
// A reply's own refusal is the consumer's to read.
func Call[Req, Rep any](ctx context.Context, path string, p Protocol, req Req, owns func(Rep) bool) (Rep, error) {
	var rep Rep
	d := net.Dialer{Timeout: exchangeTimeout}
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || isRefused(err) {
			return rep, fmt.Errorf("endpoint %s: %w: %w", path, ErrNotListening, err)
		}
		return rep, fmt.Errorf("endpoint %s: %w", path, err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetWriteDeadline(time.Now().Add(exchangeTimeout))
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Now()) })
	defer stop()
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return rep, exchangeErr(ctx, path, err)
	}
	line, err := bufio.NewReader(io.LimitReader(c, int64(p.MaxReply))).ReadBytes('\n')
	if errors.Is(err, io.EOF) && len(line) >= p.MaxReply {
		return rep, fmt.Errorf("%w: %s: reply exceeds %d bytes", p.Malformed, path, p.MaxReply)
	}
	if err != nil {
		return rep, exchangeErr(ctx, path, err)
	}
	if err := strictjson.Decode(line, &rep, p.options(p.MaxReply)); err != nil {
		return rep, fmt.Errorf("%w: %s: %w", p.Malformed, path, err)
	}
	if !owns(rep) {
		return rep, fmt.Errorf("%w: %s", p.Mismatch, path)
	}
	return rep, nil
}

// exchangeErr reports a failed exchange, as the context's error once it is done.
func exchangeErr(ctx context.Context, path string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("endpoint %s: %w: %w", path, ctx.Err(), err)
	}
	return fmt.Errorf("endpoint %s: %w", path, err)
}
