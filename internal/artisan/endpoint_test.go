package artisan_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// holderEnv makes the test binary an unrecorded process holding a socket.
const holderEnv = "GUILDHALL_ENDPOINT_HOLDER"

// mainIfEndpointHolder binds the socket named by holderEnv, prints "ready"
// and accepts connections until it is killed or a minute passes.
func mainIfEndpointHolder() {
	path := os.Getenv(holderEnv)
	if path == "" {
		return
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println("ready")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	time.Sleep(time.Minute)
	os.Exit(0)
}

var testKey = artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}

// shortDir is a temporary state directory short enough for socket paths;
// t.TempDir is too long on macOS.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "gh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func endpointPath(t *testing.T) string {
	t.Helper()
	p, err := artisan.EndpointPath(shortDir(t), testKey)
	if err != nil {
		t.Fatalf("EndpointPath: %v", err)
	}
	return p
}

func mustSelf(t *testing.T) artisan.Identity {
	t.Helper()
	self, err := artisan.Self()
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	return self
}

func ownedBy(id artisan.Identity) artisan.OwnerLookup {
	return func() (artisan.Identity, error) { return id, nil }
}

// testHandler answers each operation with its own phase. When release is
// set, Stop reports on entered and blocks until release is closed.
type testHandler struct {
	err      error
	entered  chan struct{}
	release  chan struct{}
	statuses atomic.Int32
	stops    atomic.Int32
	cancels  atomic.Int32
}

func (h *testHandler) Status() (artisan.EndpointState, error) {
	h.statuses.Add(1)
	return artisan.EndpointState{Phase: "running", Members: []artisan.Identity{{PID: 7, Token: "boot:7"}}}, h.err
}

func (h *testHandler) Stop() (artisan.EndpointState, error) {
	h.stops.Add(1)
	if h.release != nil {
		h.entered <- struct{}{}
		<-h.release
	}
	return artisan.EndpointState{Phase: "stopped"}, h.err
}

func (h *testHandler) Cancel() (artisan.EndpointState, error) {
	h.cancels.Add(1)
	return artisan.EndpointState{Phase: "cancelled"}, h.err
}

func listen(t *testing.T, path string, h artisan.EndpointHandler) *artisan.Endpoint {
	t.Helper()
	self := mustSelf(t)
	e, err := artisan.Listen(path, testKey, self, ownedBy(self), h)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func call(t *testing.T, path string, op artisan.EndpointOp) (artisan.EndpointState, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return artisan.Call(ctx, path, testKey, mustSelf(t), op)
}

func assertExists(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s: %s is gone: %v", why, path, err)
	}
}

func assertAbsent(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s: %s exists (Lstat: %v)", why, path, err)
	}
}

// startHolder runs an unrecorded process bound at path and waits until it answers.
func startHolder(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), holderEnv+"="+path)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(out).ReadString('\n')
		if err == nil && line != "ready\n" {
			err = fmt.Errorf("holder printed %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("holder did not bind %s: %v", path, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("holder did not report ready within 10s for %s", path)
	}
	return cmd
}

func TestEndpointPath(t *testing.T) {
	// EndpointPath is pure: use a synthetic absolute directory so the host's
	// temporary-directory prefix cannot crowd out the hashed fallback.
	dir := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "state")
	p, err := artisan.EndpointPath(dir, testKey)
	if err != nil {
		t.Fatalf("EndpointPath: %v", err)
	}
	if want := filepath.Join(dir, "endpoints", "app.t-1.build.1.sock"); p != want {
		t.Errorf("EndpointPath = %q, want %q", p, want)
	}
	next := testKey
	next.Attempt = 2
	if q, _ := artisan.EndpointPath(dir, next); q == p || q == "" {
		t.Errorf("attempts 1 and 2 share endpoint path %q (%q)", p, q)
	}

	// Pad the step so the readable path is exactly at the limit, then one byte over.
	// A short state directory is lengthened so the step id can carry the rest.
	short := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "s", Attempt: 1}
	base, _ := artisan.EndpointPath(dir, short)
	if pad := artisan.MaxEndpointPath - len(base); pad > 40 {
		dir = filepath.Join(dir, strings.Repeat("p", pad-31))
		base, _ = artisan.EndpointPath(dir, short)
	}
	pad := artisan.MaxEndpointPath - len(base)
	if pad < 1 || pad+2 > 63 {
		t.Skipf("state directory %q leaves %d bytes of padding; the boundary needs 1 to 61", dir, pad)
	}
	atLimit := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "s" + strings.Repeat("x", pad), Attempt: 1}
	p, err = artisan.EndpointPath(dir, atLimit)
	if err != nil || len(p) != artisan.MaxEndpointPath || filepath.Base(p) != "app.t-1."+atLimit.Step+".1.sock" {
		t.Errorf("readable path at the limit: %q (%d bytes), %v; want the readable name of %d bytes", p, len(p), err, artisan.MaxEndpointPath)
	}
	over := atLimit
	over.Step += "x"
	p, err = artisan.EndpointPath(dir, over)
	if err != nil {
		t.Fatalf("one byte over the limit: %v, want the hashed fallback", err)
	}
	name := filepath.Base(p)
	if filepath.Dir(p) != filepath.Join(dir, "endpoints") || len(name) != 37 || !strings.HasSuffix(name, ".sock") || strings.Count(name, ".") != 1 {
		t.Errorf("fallback path = %q, want <dir>/endpoints/<32 hex>.sock", p)
	}
	if len(p) > artisan.MaxEndpointPath {
		t.Errorf("fallback path is %d bytes, over the %d-byte limit", len(p), artisan.MaxEndpointPath)
	}
	if again, _ := artisan.EndpointPath(dir, over); again != p {
		t.Errorf("fallback path is not deterministic: %q then %q", p, again)
	}
	other := over
	other.Attempt = 2
	if q, _ := artisan.EndpointPath(dir, other); q == p {
		t.Errorf("two long keys share fallback path %q", p)
	}
}

func TestEndpointPathRefusals(t *testing.T) {
	dir := shortDir(t)
	long := filepath.Join(dir, strings.Repeat("d", artisan.MaxEndpointPath))
	if _, err := artisan.EndpointPath(long, testKey); !errors.Is(err, artisan.ErrEndpointPath) {
		t.Errorf("state directory past the limit: %v, want ErrEndpointPath", err)
	}
	if _, err := artisan.EndpointPath("state", testKey); err == nil {
		t.Error("relative state directory accepted")
	}
	for _, k := range []artisan.AttemptKey{
		{Repository: "App", TaskID: "t-1", Step: "build", Attempt: 1},
		{Repository: "app", TaskID: "t/1", Step: "build", Attempt: 1},
		{Repository: "app", TaskID: "t-1", Step: "", Attempt: 1},
		{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 0},
	} {
		if _, err := artisan.EndpointPath(dir, k); err == nil {
			t.Errorf("invalid key %+v accepted", k)
		}
	}
	if _, err := artisan.Listen(filepath.Join(dir, strings.Repeat("e", artisan.MaxEndpointPath)), testKey, mustSelf(t), ownedBy(mustSelf(t)), &testHandler{}); !errors.Is(err, artisan.ErrEndpointPath) {
		t.Errorf("Listen past the limit: %v, want ErrEndpointPath", err)
	}
}

// TestListenBindsOnlyAfterOwnership is the bind-after-own ordering: no record,
// or a record naming another launcher, creates nothing.
func TestListenBindsOnlyAfterOwnership(t *testing.T) {
	path := endpointPath(t)
	self := mustSelf(t)
	other := artisan.Identity{PID: self.PID, Token: self.Token + "-other"}
	for name, owner := range map[string]artisan.OwnerLookup{
		"no record":      func() (artisan.Identity, error) { return artisan.Identity{}, store.ErrNotFound },
		"other launcher": ownedBy(other),
	} {
		e, err := artisan.Listen(path, testKey, self, owner, &testHandler{})
		if !errors.Is(err, artisan.ErrNotOwner) {
			t.Errorf("%s: Listen = %v, want ErrNotOwner", name, err)
		}
		if e != nil {
			_ = e.Close()
		}
		assertAbsent(t, path, name)
		assertAbsent(t, filepath.Dir(path), name+": endpoint directory")
	}
	e := listen(t, path, &testHandler{})
	if e.Path() != path {
		t.Errorf("Path = %q, want %q", e.Path(), path)
	}
	if _, err := call(t, path, artisan.OpStatus); err != nil {
		t.Errorf("status after binding as the recorded owner: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	assertAbsent(t, path, "closed endpoint")
	if err := e.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, err := call(t, path, artisan.OpStatus); err == nil {
		t.Error("closed endpoint still answers")
	}
}

func TestEndpointServesStatusStopAndCancel(t *testing.T) {
	path := endpointPath(t)
	h := &testHandler{}
	listen(t, path, h)
	for op, phase := range map[artisan.EndpointOp]string{artisan.OpStatus: "running", artisan.OpStop: "stopped", artisan.OpCancel: "cancelled"} {
		st, err := call(t, path, op)
		if err != nil || st.Phase != phase {
			t.Errorf("%s = %+v, %v; want phase %q", op, st, err, phase)
		}
	}
	if st, _ := call(t, path, artisan.OpStatus); len(st.Members) != 1 || st.Members[0] != (artisan.Identity{PID: 7, Token: "boot:7"}) {
		t.Errorf("status members = %+v", st.Members)
	}
	if _, err := call(t, path, "resume"); !errors.Is(err, artisan.ErrEndpointRequest) {
		t.Errorf("unknown op: %v, want ErrEndpointRequest", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	self := mustSelf(t)
	other := testKey
	other.Attempt = 2
	if _, err := artisan.Call(ctx, path, other, self, artisan.OpStatus); !errors.Is(err, artisan.ErrEndpointMismatch) {
		t.Errorf("reply for another attempt: %v, want ErrEndpointMismatch", err)
	}
	if _, err := artisan.Call(ctx, path, testKey, artisan.Identity{PID: self.PID, Token: "other"}, artisan.OpStatus); !errors.Is(err, artisan.ErrEndpointMismatch) {
		t.Errorf("reply from another launcher: %v, want ErrEndpointMismatch", err)
	}
}

func TestEndpointRejectsMismatchedTargetsBeforeDispatch(t *testing.T) {
	path := endpointPath(t)
	self := mustSelf(t)
	old := listen(t, path, &testHandler{})
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	replacement := artisan.Identity{PID: self.PID, Token: self.Token + "-replacement"}
	h := &testHandler{}
	e, err := artisan.Listen(path, testKey, replacement, ownedBy(replacement), h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	other := testKey
	other.Attempt++
	for _, op := range []artisan.EndpointOp{artisan.OpStatus, artisan.OpStop, artisan.OpCancel} {
		for _, target := range []struct {
			name     string
			key      artisan.AttemptKey
			launcher artisan.Identity
		}{
			{"stale owner", testKey, self},
			{"wrong PID", testKey, artisan.Identity{PID: replacement.PID + 1, Token: replacement.Token}},
			{"wrong attempt", other, replacement},
		} {
			t.Run(string(op)+"/"+target.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if _, err := artisan.Call(ctx, path, target.key, target.launcher, op); !errors.Is(err, artisan.ErrEndpointMismatch) {
					t.Errorf("Call = %v, want ErrEndpointMismatch", err)
				}
			})
		}
		for _, fields := range []map[string]any{
			{"op": op},
			{"op": op, "attempt": testKey.String()},
			{"op": op, "launcher": replacement},
		} {
			request, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if rep := rawExchange(t, path, string(request)+"\n"); rep["error"] == nil || rep["state"] != nil {
				t.Errorf("missing target identity: reply %v", rep)
			}
		}
	}
	if s, p, c := h.statuses.Load(), h.stops.Load(), h.cancels.Load(); s != 0 || p != 0 || c != 0 {
		t.Fatalf("rejected requests reached handlers: status=%d stop=%d cancel=%d", s, p, c)
	}
	for _, op := range []artisan.EndpointOp{artisan.OpStatus, artisan.OpStop, artisan.OpCancel} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := artisan.Call(ctx, path, testKey, replacement, op)
		cancel()
		if err != nil {
			t.Errorf("current owner %s: %v", op, err)
		}
	}
	if s, p, c := h.statuses.Load(), h.stops.Load(), h.cancels.Load(); s != 1 || p != 1 || c != 1 {
		t.Errorf("matching requests: status=%d stop=%d cancel=%d, want one each", s, p, c)
	}
}

func TestEndpointHandlerError(t *testing.T) {
	path := endpointPath(t)
	listen(t, path, &testHandler{err: errors.New("ladder failed")})
	_, err := call(t, path, artisan.OpStop)
	if !errors.Is(err, artisan.ErrEndpointRequest) || !strings.Contains(err.Error(), "ladder failed") {
		t.Errorf("handler error: %v, want ErrEndpointRequest carrying the handler's text", err)
	}
}

// rawExchange writes request to the endpoint and returns its reply line.
func rawExchange(t *testing.T, path, request string) map[string]any {
	t.Helper()
	c, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	// Requests stay within the server's read budget so closing the socket
	// with unread bytes cannot reset the connection and discard its reply.
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

// TestEndpointLineProtocol pins the scratch protocol: one JSON request line,
// one JSON reply line naming the attempt and launcher, and an error reply to
// a malformed or oversized request. Requests carry the version 1 schema
// (docs/decisions/endpoint-protocol-v1.md).
func TestEndpointLineProtocol(t *testing.T) {
	path := endpointPath(t)
	h := &testHandler{}
	listen(t, path, h)
	request, err := json.Marshal(map[string]any{"schema": "guildhall.attempt-control/1", "op": "status", "attempt": testKey.String(), "launcher": mustSelf(t)})
	if err != nil {
		t.Fatal(err)
	}
	rep := rawExchange(t, path, string(request)+"\n")
	if rep["attempt"] != testKey.String() || rep["error"] != nil {
		t.Errorf("status reply = %v", rep)
	}
	if l, ok := rep["launcher"].(map[string]any); !ok || int(l["pid"].(float64)) != os.Getpid() {
		t.Errorf("status reply launcher = %v, want this process", rep["launcher"])
	}
	for name, req := range map[string]string{
		"malformed": "not json\n",
		// Exhaust the 4 KiB request budget without a newline: no complete
		// request fits, but no unread tail can reset the socket on Windows.
		"oversized": strings.Repeat("x", 4<<10),
	} {
		rep := rawExchange(t, path, req)
		if rep["error"] == nil || rep["attempt"] != testKey.String() {
			t.Errorf("%s request: reply %v, want an error naming the attempt", name, rep)
		}
	}
	if n := h.statuses.Load(); n != 1 {
		t.Errorf("handler saw %d status calls, want 1: refused requests must not reach it", n)
	}
}

// TestEndpointAnswersStatusDuringBlockedStop: a stop that waits for the
// attempt to end must not hold up status, and the caller's deadline bounds it.
func TestEndpointAnswersStatusDuringBlockedStop(t *testing.T) {
	path := endpointPath(t)
	h := &testHandler{entered: make(chan struct{}, 2), release: make(chan struct{})}
	listen(t, path, h)
	done := make(chan error, 1)
	go func() {
		_, err := call(t, path, artisan.OpStop)
		done <- err
	}()
	select {
	case <-h.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not reach the handler within 10s")
	}
	if st, err := call(t, path, artisan.OpStatus); err != nil || st.Phase != "running" {
		t.Errorf("status during a blocked stop = %+v, %v", st, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := artisan.Call(ctx, path, testKey, mustSelf(t), artisan.OpStop); !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("stop past the caller's deadline: %v, want a deadline error", err)
	}
	close(h.release)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("released stop: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("released stop was not answered within 10s")
	}
}

// TestListenRemovesStaleSocketOfExitedOwner pins scratch L21: the socket of
// a killed launcher is left behind, and the next owner removes and rebinds it.
func TestListenRemovesStaleSocketOfExitedOwner(t *testing.T) {
	path := endpointPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	holder := startHolder(t, path)
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	assertExists(t, path, "a killed process's socket file (precondition)")
	listen(t, path, &testHandler{})
	if _, err := call(t, path, artisan.OpStatus); err != nil {
		t.Errorf("status after rebinding over a stale socket: %v", err)
	}
}

func TestListenRemovesStaleSocketOfClosedListener(t *testing.T) {
	path := endpointPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
	assertExists(t, path, "socket file kept on close (precondition)")
	listen(t, path, &testHandler{})
	if _, err := call(t, path, artisan.OpStatus); err != nil {
		t.Errorf("status after rebinding: %v", err)
	}
}

// TestListenRefusesAnsweringUnrecordedEndpoint pins scratch L21's refusal:
// a socket some other process answers on is refused and left in place.
func TestListenRefusesAnsweringUnrecordedEndpoint(t *testing.T) {
	path := endpointPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	startHolder(t, path)
	self := mustSelf(t)
	e, err := artisan.Listen(path, testKey, self, ownedBy(self), &testHandler{})
	if !errors.Is(err, artisan.ErrEndpointAnswers) {
		t.Errorf("Listen over an answering socket = %v, want ErrEndpointAnswers", err)
	}
	if e != nil {
		_ = e.Close()
	}
	assertExists(t, path, "answering endpoint")
	c, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		t.Errorf("the unrecorded endpoint no longer answers: %v", err)
	} else {
		_ = c.Close()
	}
}

func TestListenKeepsLeftoverDirectory(t *testing.T) {
	path := endpointPath(t)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	self := mustSelf(t)
	if _, err := artisan.Listen(path, testKey, self, ownedBy(self), &testHandler{}); !errors.Is(err, artisan.ErrEndpointUnknown) {
		t.Errorf("Listen over a directory = %v, want ErrEndpointUnknown", err)
	}
	assertExists(t, path, "leftover directory")
}

// TestEndpointWithStoreOwnership binds against a real ownership record: refused
// before it exists and while another launcher holds it, bound after taking it
// over, recorded with UpdateOwnership, and refused again once released.
func TestEndpointWithStoreOwnership(t *testing.T) {
	dir := shortDir(t)
	st, err := store.Open(filepath.Join(dir, "guildhall.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	path, err := artisan.EndpointPath(dir, testKey)
	if err != nil {
		t.Fatal(err)
	}
	self := mustSelf(t)
	lookup := func() (artisan.Identity, error) {
		o, err := st.Ownership(testKey.Repository, testKey.TaskID, testKey.Step, testKey.Attempt)
		if err != nil {
			return artisan.Identity{}, err
		}
		if o.State == store.OwnerReleased {
			return artisan.Identity{}, errors.New("ownership released")
		}
		return artisan.Identity(o.LauncherIdentity), nil
	}
	tryListen := func() error {
		e, err := artisan.Listen(path, testKey, self, lookup, &testHandler{})
		if err == nil {
			_ = e.Close()
		}
		return err
	}
	if err := tryListen(); !errors.Is(err, artisan.ErrNotOwner) {
		t.Errorf("Listen before ownership: %v, want ErrNotOwner", err)
	}
	digest := func(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }
	if _, err := st.CreateTask("m-task", store.Task{Repository: "app", ID: "t-1", Title: "Endpoint", Workflow: workflow.Revision{Name: "default", Digest: digest('a')}, ConfigDigest: digest('b')}); err != nil {
		t.Fatal(err)
	}
	gone := artisan.Identity{PID: self.PID, Token: self.Token + "-exited"}
	if _, err := st.StartOwnedAttempt("m-start", store.Ownership{Repository: "app", TaskID: "t-1", Step: "build", LauncherIdentity: store.ProcessIdentity(gone)}); err != nil {
		t.Fatal(err)
	}
	if err := tryListen(); !errors.Is(err, artisan.ErrNotOwner) {
		t.Errorf("Listen while another launcher owns the attempt: %v, want ErrNotOwner", err)
	}
	assertAbsent(t, path, "refused bind")
	own := store.Ownership{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1, LauncherIdentity: store.ProcessIdentity(self)}
	if _, err := st.TakeOwnership("m-take", own, launcherProbe); err != nil {
		t.Fatalf("TakeOwnership from an exited launcher: %v", err)
	}
	e, err := artisan.Listen(path, testKey, self, lookup, &testHandler{})
	if err != nil {
		t.Fatalf("Listen after taking ownership: %v", err)
	}
	defer func() { _ = e.Close() }()
	own.Endpoint, own.State = e.Path(), store.OwnerRunning
	if _, err := st.UpdateOwnership("m-endpoint", own); err != nil {
		t.Fatalf("UpdateOwnership with the endpoint: %v", err)
	}
	rec, err := st.Ownership("app", "t-1", "build", 1)
	if err != nil || rec.Endpoint != path {
		t.Errorf("recorded endpoint = %q, %v; want %q", rec.Endpoint, err, path)
	}
	if _, err := call(t, rec.Endpoint, artisan.OpStatus); err != nil {
		t.Errorf("status at the recorded endpoint: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishAttempt("m-finish", "app", "t-1", "build", 1, store.AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := tryListen(); !errors.Is(err, artisan.ErrNotOwner) {
		t.Errorf("Listen after the attempt finished: %v, want ErrNotOwner", err)
	}
}
