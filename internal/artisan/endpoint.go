package artisan

import (
	"context"
	"errors"
	"fmt"

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
)

// MaxEndpointPath is the longest socket path, in bytes, this platform's
// AF_UNIX address holds with its terminating NUL: 103 on macOS, 107 on Linux
// and Windows.
const MaxEndpointPath = maxEndpointPath

// AttemptKey names one stage attempt as the store keys it.
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
	return "", errors.New("not implemented")
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

// Endpoint is a bound control endpoint.
type Endpoint struct {
	path string
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
	return nil, errors.New("not implemented")
}

// Path returns the socket path the endpoint is bound at.
func (e *Endpoint) Path() string { return e.path }

// Close stops accepting requests and removes the socket file; requests being
// answered still get their reply. It is idempotent.
func (e *Endpoint) Close() error { return errors.New("not implemented") }

// Call sends op to the endpoint at path and returns the state it answers.
// The reply must name attempt k and launcher, otherwise ErrEndpointMismatch;
// a reply carrying an error is ErrEndpointRequest. ctx bounds the dial and
// the whole exchange. A failed dial says nothing about whether the owner is
// alive: a silent endpoint never proves exit.
func Call(ctx context.Context, path string, k AttemptKey, launcher Identity, op EndpointOp) (EndpointState, error) {
	return EndpointState{}, errors.New("not implemented")
}
