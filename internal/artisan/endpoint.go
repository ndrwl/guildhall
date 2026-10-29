package artisan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ndrwl/guildhall/internal/endpoint"
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
// carry "schema": "guildhall.attempt-control/1". The endpoint package binds,
// serves and calls it (docs/contracts/endpoint.md): it decodes a request
// strictly and refuses, before matching identities or calling any handler, a
// missing or unversioned schema, a newer version, a duplicate key and a field
// the protocol does not declare, at any depth; Call refuses such a reply with
// ErrEndpointProtocol.

// Errors returned by the control endpoint. ErrEndpointPath, ErrEndpointAnswers and ErrEndpointUnknown are the
// endpoint package's, which owns the rules behind them.
var (
	// ErrEndpointPath refuses a socket path longer than MaxEndpointPath bytes.
	ErrEndpointPath = endpoint.ErrPath
	// ErrNotOwner refuses to bind because the attempt's ownership record does
	// not name this launcher, or cannot be read; nothing is created.
	ErrNotOwner = errors.New("attempt ownership record does not name this launcher")
	// ErrEndpointAnswers refuses to bind over a socket that accepts connections
	// although no ownership record names it; the socket file is kept.
	ErrEndpointAnswers = endpoint.ErrAnswers
	// ErrEndpointUnknown refuses to bind over a leftover file that cannot be
	// proven stale, such as a regular file, a directory or a socket that
	// refuses access; the file is kept.
	ErrEndpointUnknown = endpoint.ErrUnknown
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
// AF_UNIX address holds with its terminating NUL: endpoint.MaxPath, 103 on
// macOS, 107 on Linux and Windows.
const MaxEndpointPath = endpoint.MaxPath

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

// AttemptEnv names, in an attempt's launcher and worker environment, the attempt they run, as
// "<repository>/<task>/<step>/<n>" (AttemptKey.String). The launcher sets it over any inherited value, so
// `guildhall messages send` run inside an attempt records the sender "artisan", even when the attempt inherited a
// Director session's GUILDHALL_SESSION and GUILDHALL_COPY. It is asserted, not authenticated.
const AttemptEnv = "GUILDHALL_ATTEMPT"

// Env returns the AttemptEnv entry naming k.
func (k AttemptKey) Env() string { return AttemptEnv + "=" + k.String() }

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

// controlProtocol is the attempt control line protocol, version 1
// (docs/artisan.md#protocol).
var controlProtocol = endpoint.Protocol{
	Schema: "guildhall.attempt-control/", Version: 1, MaxRequest: 4 << 10, MaxReply: 1 << 20,
	Mismatch: ErrEndpointMismatch, Malformed: ErrEndpointProtocol,
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
	*endpoint.Endpoint
}

// Listen binds the control endpoint of attempt k at path for launcher self
// and serves h until Close. It binds only when owner reports self, and
// otherwise returns ErrNotOwner without creating anything. A path longer than
// MaxEndpointPath is ErrEndpointPath.
//
// The endpoint package binds it (docs/contracts/endpoint.md). A leftover file
// at path is handled by its stale-file rule: taking the ownership record
// proved any previous owner exited, so a leftover socket nothing listens on
// (the connection is refused) is that owner's stale socket and is removed
// before binding. A socket that accepts a connection is an unrecorded
// endpoint: ErrEndpointAnswers, and the file is kept. Any other leftover,
// such as a regular file, a directory or a socket that refuses access,
// cannot be proven stale: ErrEndpointUnknown, and it is kept.
//
// The endpoint's parent directory is created with mode 0700 and, on Unix,
// the socket is made 0600; access control is the state directory's, and the
// endpoint does not authenticate its peers. Close follows the shared endpoint
// shutdown contract (docs/contracts/endpoint.md).
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
	e, err := endpoint.Listen[endpointRequest, endpointReply](path, controlProtocol, controlServer{attempt: k.String(), self: self, h: h})
	if err != nil {
		return nil, fmt.Errorf("artisan: %w", err)
	}
	return &Endpoint{e}, nil
}

// controlServer serves attempt control requests for one attempt and launcher.
type controlServer struct {
	attempt string
	self    Identity
	h       EndpointHandler
}

func (s controlServer) Owns(req endpointRequest) bool {
	return req.Attempt == s.attempt && req.Launcher == s.self
}

func (s controlServer) Refuse(msg string) endpointReply {
	return endpointReply{Schema: controlProtocol.Current(), Attempt: s.attempt, Launcher: s.self, Error: msg}
}

func (s controlServer) Serve(req endpointRequest) endpointReply {
	var st EndpointState
	var err error
	switch req.Op {
	case OpStatus:
		st, err = s.h.Status()
	case OpStop:
		st, err = s.h.Stop()
	case OpCancel:
		st, err = s.h.Cancel()
	default:
		err = fmt.Errorf("unknown op %q", req.Op)
	}
	if err != nil {
		return s.Refuse(err.Error())
	}
	return endpointReply{Schema: controlProtocol.Current(), Attempt: s.attempt, Launcher: s.self, State: &st}
}

// Call sends op to the endpoint at path and returns the state it answers.
// The reply must be protocol version 1, otherwise ErrEndpointProtocol, and
// name attempt k and launcher, otherwise ErrEndpointMismatch; a reply carrying
// an error, including the endpoint's refusal of the request, is
// ErrEndpointRequest. ctx bounds the dial and the whole exchange. A failed
// dial, endpoint.ErrNotListening when nothing listens, says nothing about
// whether the owner is alive: a silent endpoint never proves exit.
func Call(ctx context.Context, path string, k AttemptKey, launcher Identity, op EndpointOp) (EndpointState, error) {
	req := endpointRequest{Schema: controlProtocol.Current(), Op: op, Attempt: k.String(), Launcher: launcher}
	rep, err := endpoint.Call(ctx, path, controlProtocol, req, func(rep endpointReply) bool {
		return rep.Attempt == k.String() && rep.Launcher == launcher
	})
	switch {
	case errors.Is(err, ErrEndpointMismatch):
		return EndpointState{}, fmt.Errorf("artisan: %w; it answered for attempt %q launcher %d", err, rep.Attempt, rep.Launcher.PID)
	case err != nil:
		return EndpointState{}, fmt.Errorf("artisan: %w", err)
	case rep.Error != "":
		return EndpointState{}, fmt.Errorf("artisan: %w: %s: %s", ErrEndpointRequest, op, rep.Error)
	case rep.State == nil:
		return EndpointState{}, fmt.Errorf("artisan: endpoint %s: reply has neither state nor error", path)
	}
	return *rep.State, nil
}
