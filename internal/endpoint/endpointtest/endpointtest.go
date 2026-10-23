// Package endpointtest is the shared contract suite of internal/endpoint (docs/contracts/endpoint-endpointtest.md):
// every consumer of an endpoint runs RunEndpointContract over its own Listen and Call, so a rule the endpoint owns
// holds the same for each.
package endpointtest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/endpoint"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// Ceiling is one exchange's ceiling in the suite.
func Ceiling() time.Duration { return testenv.Ceiling(10 * time.Second) }

// Endpoint is what a consumer's Listen returns.
type Endpoint interface {
	Path() string
	Close() error
}

// Harness is one consumer over a fresh endpoint path.
type Harness struct {
	// Path is where Listen binds, in a short directory of its own that may not exist yet.
	Path string
	// Protocol is the consumer's protocol.
	Protocol endpoint.Protocol
	// Listen binds the consumer's endpoint at Path for its owner.
	Listen func() (Endpoint, error)
	// Call makes one exchange as the owner with the endpoint at Path that the consumer's server serves.
	Call func(ctx context.Context) error
	// CallOther makes one exchange naming another owner, which must fail with Protocol.Mismatch.
	CallOther func(ctx context.Context) error
	// Request is a request line naming the owner, without its newline, that the consumer serves; Foreign is the
	// same request naming another owner.
	Request, Foreign string
	// Served is how many requests have reached the consumer's server.
	Served func() int
}

// RunEndpointContract runs the endpoint contract against the consumer each newHarness call returns, one fresh
// harness per case.
func RunEndpointContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	t.Run("serves the owner", func(t *testing.T) {
		h := newHarness(t)
		e := listen(t, h)
		if e.Path() != h.Path {
			t.Errorf("Path = %q, want %q", e.Path(), h.Path)
		}
		if err := call(h.Call); err != nil {
			t.Fatalf("Call: %v", err)
		}
		if n := h.Served(); n != 1 {
			t.Errorf("served %d requests, want 1", n)
		}
		rep := RawExchange(t, h.Path, h.Request+"\n")
		if rep["schema"] != h.Protocol.Current() || rep["error"] != nil {
			t.Errorf("reply %v, want a %s reply without an error", rep, h.Protocol.Current())
		}
		checkModes(t, h.Path)
	})
	t.Run("close removes the socket", func(t *testing.T) {
		h := newHarness(t)
		e := listen(t, h)
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		if err := e.Close(); err != nil {
			t.Errorf("second Close = %v", err)
		}
		if _, err := os.Lstat(h.Path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("socket file after Close: %v", err)
		}
		if err := call(h.Call); err == nil {
			t.Error("Call after Close succeeded")
		}
	})
	t.Run("stale socket is replaced", func(t *testing.T) {
		h := newHarness(t)
		StaleSocket(t, h.Path)
		listen(t, h)
		if err := call(h.Call); err != nil {
			t.Errorf("Call after binding over a stale socket: %v", err)
		}
	})
	t.Run("answering socket is refused and kept", func(t *testing.T) {
		h := newHarness(t)
		listen(t, h)
		if e, err := h.Listen(); !errors.Is(err, endpoint.ErrAnswers) {
			if e != nil {
				_ = e.Close()
			}
			t.Errorf("Listen over a live endpoint = %v, want ErrAnswers", err)
		}
		if err := call(h.Call); err != nil {
			t.Errorf("the live endpoint stopped answering after a refused Listen: %v", err)
		}
	})
	t.Run("foreign file is refused and kept", func(t *testing.T) {
		h := newHarness(t)
		mkdir(t, filepath.Dir(h.Path))
		if err := os.WriteFile(h.Path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		refused(t, h, endpoint.ErrUnknown)
		if b, err := os.ReadFile(h.Path); err != nil || string(b) != "x" {
			t.Errorf("the regular file was not kept: %q, %v", b, err)
		}
		dir := newHarness(t)
		mkdir(t, dir.Path)
		refused(t, dir, endpoint.ErrUnknown)
		if fi, err := os.Stat(dir.Path); err != nil || !fi.IsDir() {
			t.Errorf("the directory was not kept: %v", err)
		}
	})
	t.Run("oversize request is refused", func(t *testing.T) {
		h := newHarness(t)
		listen(t, h)
		// Exhaust the request budget without a newline: no complete request fits, and no unread tail can reset
		// the socket before the reply is read on Windows.
		rep := RawExchange(t, h.Path, strings.Repeat("x", h.Protocol.MaxRequest))
		refusal(t, h, rep, "an oversize request")
	})
	t.Run("wrong peer is refused", func(t *testing.T) {
		h := newHarness(t)
		listen(t, h)
		if err := call(h.CallOther); !errors.Is(err, h.Protocol.Mismatch) {
			t.Errorf("Call naming another owner = %v, want %v", err, h.Protocol.Mismatch)
		}
		rep := RawExchange(t, h.Path, h.Foreign+"\n")
		refusal(t, h, rep, "a request naming another owner")
		if msg, _ := rep["error"].(string); msg != h.Protocol.Mismatch.Error() {
			t.Errorf("refusal %q, want the consumer's mismatch %q", msg, h.Protocol.Mismatch.Error())
		}
	})
	t.Run("old and new schemas are refused", func(t *testing.T) {
		h := newHarness(t)
		listen(t, h)
		for name, schema := range map[string]any{
			"missing":        nil,
			"unversioned":    strings.TrimSuffix(h.Protocol.Schema, "/"),
			"version zero":   h.Protocol.Schema + "0",
			"older":          h.Protocol.Schema + "-1",
			"newer":          h.Protocol.Schema + "99",
			"other protocol": "guildhall.other/1",
		} {
			rep := RawExchange(t, h.Path, withSchema(t, h.Request, schema)+"\n")
			refusal(t, h, rep, "a request whose schema is "+name)
		}
		for name, line := range map[string]string{
			"not json":       "not json",
			"unknown field":  strings.TrimSuffix(h.Request, "}") + `,"extra":true}`,
			"duplicate key":  strings.Replace(h.Request, "{", `{"schema":"`+h.Protocol.Current()+`",`, 1),
			"trailing value": h.Request + " {}",
		} {
			rep := RawExchange(t, h.Path, line+"\n")
			refusal(t, h, rep, "a request that is "+name)
		}
	})
}

func listen(t *testing.T, h Harness) Endpoint {
	t.Helper()
	e, err := h.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func call(fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), Ceiling())
	defer cancel()
	return fn(ctx)
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

// refused checks that Listen refuses to bind over the leftover at h.Path with want.
func refused(t *testing.T, h Harness, want error) {
	t.Helper()
	e, err := h.Listen()
	if e != nil {
		_ = e.Close()
	}
	if !errors.Is(err, want) {
		t.Errorf("Listen over a leftover = %v, want %v", err, want)
	}
}

// refusal checks that rep is the consumer's refusal, a reply with its schema and an error, and that no refused
// request reached its server.
func refusal(t *testing.T, h Harness, rep map[string]any, what string) {
	t.Helper()
	if msg, _ := rep["error"].(string); msg == "" || rep["schema"] != h.Protocol.Current() {
		t.Errorf("%s: reply %v, want a %s refusal", what, rep, h.Protocol.Current())
	}
	if n := h.Served(); n != 0 {
		t.Errorf("%s reached the consumer's server: %d served", what, n)
	}
}

// withSchema is request with its schema field set to schema, or removed when schema is nil.
func withSchema(t *testing.T, request string, schema any) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(request), &m); err != nil {
		t.Fatalf("request %q is not a JSON object: %v", request, err)
	}
	if schema == nil {
		delete(m, "schema")
	} else {
		m["schema"] = schema
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// RawExchange writes request, which should stay within the endpoint's read budget, to the endpoint at path and
// returns its reply line decoded.
func RawExchange(t *testing.T, path, request string) map[string]any {
	t.Helper()
	c, err := net.DialTimeout("unix", path, Ceiling())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(Ceiling()))
	_, writeErr := c.Write([]byte(request))
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("no reply line to %.40q: %v (write error: %v)", request, err, writeErr)
	}
	var rep map[string]any
	if err := json.Unmarshal(line, &rep); err != nil {
		t.Fatalf("reply %q is not JSON: %v", line, err)
	}
	return rep
}

// StaleSocket leaves a socket file at path that nothing listens on, as an owner that exited leaves it.
func StaleSocket(t *testing.T, path string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if fi, err := os.Lstat(path); err != nil || fi.Mode().Type() != fs.ModeSocket {
		t.Fatalf("stale socket at %s: %v", path, err)
	}
}
