package artisan

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/ndrwl/guildhall/internal/strictjson"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// # Control endpoint
//
// Each owned stage attempt has one control endpoint: an AF_UNIX stream socket
// under the Guildhall state directory, on Windows as on Unix, at the path
// EndpointPath gives (docs/artisan.md#control-endpoint). Its owning launcher
// binds it with Listen only after the attempt's ownership record names that
// launcher, and records the path in the record afterwards. The endpoint
// answers one JSON request line with one JSON reply line naming the attempt
// and the launcher, for the operations status, stop and cancel; what stop and
// cancel do is the launcher's EndpointHandler.
//
// The line protocol is versioned (docs/artisan.md#protocol): request and reply
// carry "schema": "guildhall.attempt-control/1". The endpoint decodes a
// request strictly and refuses, before matching identities or calling any
// handler, a missing or unversioned schema, a newer version, a duplicate key
// and a field the protocol does not declare, at any depth; Call refuses such
// a reply with ErrEndpointProtocol.

// Errors returned by the control endpoint.
var (
	// ErrEndpointPath refuses a socket path longer than MaxEndpointPath bytes.
	ErrEndpointPath = errors.New("endpoint path exceeds the AF_UNIX path limit")
	// ErrNotOwner refuses to bind because the attempt's ownership record does
	// not name this launcher, or cannot be read; nothing is created.
	ErrNotOwner = errors.New("attempt ownership record does not name this launcher")
	// ErrEndpointAnswers refuses to bind over a socket that accepts connections
	// although no ownership record names it; the socket file is kept.
	ErrEndpointAnswers = errors.New("an unrecorded endpoint answers at the attempt's path")
	// ErrEndpointUnknown refuses to bind over a leftover file that cannot be
	// proven stale, such as one that refuses access; the file is kept.
	ErrEndpointUnknown = errors.New("leftover endpoint file cannot be proven stale")
	// ErrEndpointMismatch reports a reply that names another attempt or
	// launcher than the caller expected.
	ErrEndpointMismatch = errors.New("endpoint answered for another attempt or launcher")
	// ErrEndpointRequest reports a request the endpoint answered with an error,
	// such as an unknown operation or a handler failure.
	ErrEndpointRequest = errors.New("endpoint refused the request")
	// ErrEndpointProtocol reports a reply that is not protocol
	// guildhall.attempt-control/1: malformed, carrying unknown or duplicate
	// fields, unversioned or, also matching strictjson.ErrNewerSchema, newer.
	ErrEndpointProtocol = errors.New("endpoint reply is not attempt-control protocol version 1")
)

// MaxEndpointPath is the longest socket path, in bytes, this platform's
// AF_UNIX address holds with its terminating NUL: 103 on macOS, 107 on Linux
// and Windows.
const MaxEndpointPath = maxEndpointPath

// AttemptKey names one stage attempt as the store keys it. It owns the
// attempt layout: the interpreter prepares Dir and the launcher runs in it,
// and neither spells the layout itself.
type AttemptKey struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	Step       string `json:"step"`
	Attempt    int    `json:"attempt"`
}

// String returns "<repository>/<task>/<step>/<attempt>", unique per attempt
// because ids cannot contain "/".
func (k AttemptKey) String() string {
	return fmt.Sprintf("%s/%s/%s/%d", k.Repository, k.TaskID, k.Step, k.Attempt)
}

// AttemptID returns "<step>-<n>", the attempt's adapter identity within its
// task and its directory's name.
func (k AttemptKey) AttemptID() string { return fmt.Sprintf("%s-%d", k.Step, k.Attempt) }

// Dir returns the attempt directory under the absolute state directory
// stateDir: "<stateDir>/<repository>/<task>/<step>-<n>". Its result file is
// ResultFile in it, and ModelDir in it is the only part a model may write.
func (k AttemptKey) Dir(stateDir string) string {
	return filepath.Join(stateDir, k.Repository, k.TaskID, k.AttemptID())
}

// ModelDir is the subdirectory of the attempt directory a model-run Artisan
// may write: the copy of its prompt it reads and the draft result it writes,
// which its wrapper publishes as ResultFile. The attempt's control files,
// ResultFile, its cancel marker, OutcomeFile, LockFile, EventsFile and the
// launcher's log, are never in it.
const ModelDir = "model"

func (k AttemptKey) validate() error {
	for _, id := range []string{k.Repository, k.TaskID, k.Step} {
		if !workflow.ValidID(id) {
			return fmt.Errorf("artisan: attempt id %q must be lowercase letters, digits and hyphens, starting with a letter", id)
		}
	}
	if k.Attempt < 1 {
		return fmt.Errorf("artisan: attempt number %d must be positive", k.Attempt)
	}
	return nil
}

// EndpointPath returns the socket path of attempt k under the absolute state
// directory stateDir: "<stateDir>/endpoints/<repository>.<task>.<step>.<attempt>.sock".
// When that exceeds MaxEndpointPath it returns the hashed fallback
// "<stateDir>/endpoints/<32 hex digits of sha256(k.String())>.sock", which
// cannot collide with a readable name; when that is still too long it returns
// ErrEndpointPath. The path is a pure function of stateDir and k.
func EndpointPath(stateDir string, k AttemptKey) (string, error) {
	if err := k.validate(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(stateDir) {
		return "", fmt.Errorf("artisan: endpoint state directory %q is not absolute", stateDir)
	}
	dir := filepath.Join(stateDir, "endpoints")
	p := filepath.Join(dir, fmt.Sprintf("%s.%s.%s.%d.sock", k.Repository, k.TaskID, k.Step, k.Attempt))
	if len(p) <= MaxEndpointPath {
		return p, nil
	}
	sum := sha256.Sum256([]byte(k.String()))
	p = filepath.Join(dir, hex.EncodeToString(sum[:16])+".sock")
	if len(p) > MaxEndpointPath {
		return "", fmt.Errorf("artisan: %w: %d bytes under %q, limit %d", ErrEndpointPath, len(p), stateDir, MaxEndpointPath)
	}
	return p, nil
}

// EndpointOp is one operation the endpoint serves.
type EndpointOp string

// The operations an endpoint serves.
const (
	OpStatus EndpointOp = "status"
	OpStop   EndpointOp = "stop"
	OpCancel EndpointOp = "cancel"
)

// EndpointState is what the launcher answers a request with.
type EndpointState struct {
	// Phase is the launcher's phase, such as "starting", "running" or "ended".
	Phase string `json:"phase"`
	// Members are the contained processes, when the handler lists them.
	Members []Identity `json:"members,omitempty"`
}

// EndpointHandler is the launcher behind an endpoint. The endpoint calls it
// concurrently, one call per request, and replies when the call returns, so
// a Stop that blocks until the attempt has ended does not delay a Status.
// A returned error is sent to the caller as the reply's error.
type EndpointHandler interface {
	Status() (EndpointState, error)
	Stop() (EndpointState, error)
	Cancel() (EndpointState, error)
}

// OwnerLookup returns the launcher identity the attempt's ownership record
// names while the record is not released. An attempt with no record, or a
// released one, is an error.
type OwnerLookup func() (Identity, error)

// Limits and version of the line protocol.
const (
	// endpointSchema prefixes the protocol version in every request and reply.
	endpointSchema  = "guildhall.attempt-control/"
	endpointVersion = 1
	// endpointSchemaV1 is the schema this endpoint speaks and Call sends.
	endpointSchemaV1 = "guildhall.attempt-control/1"
	maxRequestBytes  = 4 << 10
	maxReplyBytes    = 1 << 20
	// exchangeTimeout bounds reading a request and writing its reply.
	exchangeTimeout = 5 * time.Second
	// leftoverTimeout bounds the connection attempt that tests a leftover file.
	leftoverTimeout = time.Second
)

// endpointOptions are the strict decoding rules for a line of at most max bytes.
func endpointOptions(max int) strictjson.Options {
	return strictjson.Options{Schema: endpointSchema, Version: endpointVersion, MaxBytes: max}
}

type endpointRequest struct {
	Schema   string     `json:"schema"`
	Op       EndpointOp `json:"op"`
	Attempt  string     `json:"attempt"`
	Launcher Identity   `json:"launcher"`
}

type endpointReply struct {
	Schema   string         `json:"schema"`
	Attempt  string         `json:"attempt"`
	Launcher Identity       `json:"launcher"`
	State    *EndpointState `json:"state,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// Endpoint is a bound control endpoint.
type Endpoint struct {
	path     string
	key      AttemptKey
	self     Identity
	h        EndpointHandler
	ln       *net.UnixListener
	once     sync.Once
	closeErr error
}

// Listen binds the control endpoint of attempt k at path for launcher self
// and serves h until Close. It binds only when owner reports self, and
// otherwise returns ErrNotOwner without creating anything. A path longer than
// MaxEndpointPath is ErrEndpointPath.
//
// A leftover file at path is handled by the stale-file rule. Taking the
// ownership record proved any previous owner exited, so a leftover nothing
// listens on (the connection is refused) is that owner's stale socket and is
// removed before binding. A leftover that accepts a connection is an
// unrecorded endpoint: ErrEndpointAnswers, and the file is kept. Any other
// leftover, such as one that refuses access or a directory, cannot be proven
// stale: ErrEndpointUnknown, and it is kept.
//
// The endpoint's parent directory is created with mode 0700 and, on Unix,
// the socket is made 0600; access control is the state directory's, and the
// endpoint does not authenticate its peers.
func Listen(path string, k AttemptKey, self Identity, owner OwnerLookup, h EndpointHandler) (*Endpoint, error) {
	if err := k.validate(); err != nil {
		return nil, err
	}
	if len(path) > MaxEndpointPath {
		return nil, fmt.Errorf("artisan: %w: %q is %d bytes, limit %d", ErrEndpointPath, path, len(path), MaxEndpointPath)
	}
	got, err := owner()
	if err != nil {
		return nil, fmt.Errorf("artisan: %w: %w", ErrNotOwner, err)
	}
	if got != self {
		return nil, fmt.Errorf("artisan: %w: attempt %s is recorded for launcher %d", ErrNotOwner, k, got.PID)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("artisan: endpoint directory: %w", err)
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("artisan: bind endpoint: %w", err)
	}
	if err := restrictEndpoint(path); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("artisan: restrict endpoint: %w", err)
	}
	e := &Endpoint{path: path, key: k, self: self, h: h, ln: ln}
	go e.serve()
	return e, nil
}

// removeStale applies the stale-file rule to whatever is at path.
func removeStale(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("artisan: %w: %w", ErrEndpointUnknown, err)
	case fi.IsDir():
		return fmt.Errorf("artisan: %w: %s is a directory; kept", ErrEndpointUnknown, path)
	}
	c, err := net.DialTimeout("unix", path, leftoverTimeout)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("artisan: %w: %s; kept", ErrEndpointAnswers, path)
	}
	if !isRefused(err) {
		return fmt.Errorf("artisan: %w: %s: %w; kept", ErrEndpointUnknown, path, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("artisan: remove stale endpoint: %w", err)
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

// answer reads one request line from c and writes one reply line.
func (e *Endpoint) answer(c net.Conn) {
	defer func() { _ = c.Close() }()
	rep := endpointReply{Schema: endpointSchemaV1, Attempt: e.key.String(), Launcher: e.self}
	_ = c.SetReadDeadline(time.Now().Add(exchangeTimeout))
	var req endpointRequest
	line, err := bufio.NewReader(io.LimitReader(c, maxRequestBytes)).ReadBytes('\n')
	if err == nil {
		err = strictjson.Decode(line, &req, endpointOptions(maxRequestBytes))
	}
	if errors.Is(err, strictjson.ErrNewerSchema) {
		rep.Error = fmt.Sprintf("unsupported protocol version: %v", err)
	} else if err != nil {
		rep.Error = fmt.Sprintf("malformed request: one %s JSON line of at most %d bytes is required: %v", endpointSchemaV1, maxRequestBytes, err)
	} else if req.Attempt != rep.Attempt || req.Launcher != e.self {
		rep.Error = ErrEndpointMismatch.Error()
	} else {
		var st EndpointState
		switch req.Op {
		case OpStatus:
			st, err = e.h.Status()
		case OpStop:
			st, err = e.h.Stop()
		case OpCancel:
			st, err = e.h.Cancel()
		default:
			err = fmt.Errorf("unknown op %q", req.Op)
		}
		if err != nil {
			rep.Error = err.Error()
		} else {
			rep.State = &st
		}
	}
	_ = c.SetWriteDeadline(time.Now().Add(exchangeTimeout))
	_ = json.NewEncoder(c).Encode(rep)
}

// Path returns the socket path the endpoint is bound at.
func (e *Endpoint) Path() string { return e.path }

// Close stops accepting requests and removes the socket file; requests being
// answered still get their reply. It is idempotent.
func (e *Endpoint) Close() error {
	e.once.Do(func() { e.closeErr = e.ln.Close() })
	return e.closeErr
}

// Call sends op to the endpoint at path and returns the state it answers.
// The reply must be protocol version 1, otherwise ErrEndpointProtocol, and
// name attempt k and launcher, otherwise ErrEndpointMismatch; a reply carrying
// an error, including the endpoint's refusal of the request, is ErrEndpointRequest. ctx bounds the dial and
// the whole exchange. A failed dial says nothing about whether the owner is
// alive: a silent endpoint never proves exit.
func Call(ctx context.Context, path string, k AttemptKey, launcher Identity, op EndpointOp) (EndpointState, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return EndpointState{}, fmt.Errorf("artisan: endpoint %s: %w", path, err)
	}
	defer func() { _ = c.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Now()) })
	defer stop()
	if err := json.NewEncoder(c).Encode(endpointRequest{Schema: endpointSchemaV1, Op: op, Attempt: k.String(), Launcher: launcher}); err != nil {
		return EndpointState{}, callErr(ctx, path, err)
	}
	line, err := bufio.NewReader(io.LimitReader(c, maxReplyBytes)).ReadBytes('\n')
	if err != nil {
		return EndpointState{}, callErr(ctx, path, err)
	}
	var rep endpointReply
	if err := strictjson.Decode(line, &rep, endpointOptions(maxReplyBytes)); err != nil {
		return EndpointState{}, fmt.Errorf("artisan: %w: %s: %w", ErrEndpointProtocol, path, err)
	}
	if rep.Attempt != k.String() || rep.Launcher != launcher {
		return EndpointState{}, fmt.Errorf("artisan: %w: %s answered for attempt %q launcher %d", ErrEndpointMismatch, path, rep.Attempt, rep.Launcher.PID)
	}
	if rep.Error != "" {
		return EndpointState{}, fmt.Errorf("artisan: %w: %s: %s", ErrEndpointRequest, op, rep.Error)
	}
	if rep.State == nil {
		return EndpointState{}, fmt.Errorf("artisan: endpoint %s: reply has neither state nor error", path)
	}
	return *rep.State, nil
}

// callErr reports a failed exchange, as the context's error once it is done.
func callErr(ctx context.Context, path string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("artisan: endpoint %s: %w: %w", path, ctx.Err(), err)
	}
	return fmt.Errorf("artisan: endpoint %s: %w", path, err)
}
