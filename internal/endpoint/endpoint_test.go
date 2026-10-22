package endpoint_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/endpoint"
	"github.com/ndrwl/guildhall/internal/endpoint/endpointtest"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

// The test consumer: a ping naming an owner, answered with a pong naming the server's owner.
var (
	errMismatch = errors.New("the ping endpoint serves another owner")
	errProtocol = errors.New("not ping protocol version 1")
	pingProto   = endpoint.Protocol{Schema: "guildhall.test-ping/", Version: 1, MaxRequest: 256, MaxReply: 256, Mismatch: errMismatch, Malformed: errProtocol}
)

type ping struct {
	Schema string `json:"schema"`
	Owner  string `json:"owner"`
	Block  bool   `json:"block,omitempty"`
}

type pong struct {
	Schema string `json:"schema"`
	Owner  string `json:"owner"`
	Error  string `json:"error,omitempty"`
}

// pinger is the test consumer's server; a ping that asks to block waits until release is closed.
type pinger struct {
	owner   string
	served  atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *pinger) Owns(r ping) bool { return r.Owner == p.owner }

func (p *pinger) Serve(r ping) pong {
	p.served.Add(1)
	if r.Block {
		p.entered <- struct{}{}
		<-p.release
	}
	return pong{Schema: pingProto.Current(), Owner: p.owner}
}

func (p *pinger) Refuse(msg string) pong {
	return pong{Schema: pingProto.Current(), Owner: p.owner, Error: msg}
}

func callPing(ctx context.Context, path, owner string) (pong, error) {
	return endpoint.Call(ctx, path, pingProto, ping{Schema: pingProto.Current(), Owner: owner}, func(r pong) bool { return r.Owner == owner })
}

// shortDir is a directory short enough for socket paths; t.TempDir is too long on macOS.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func newPinger() *pinger {
	return &pinger{owner: "alpha", entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func listenPing(t *testing.T, path string, p *pinger) *endpoint.Endpoint {
	t.Helper()
	e, err := endpoint.Listen[ping, pong](path, pingProto, p)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestEndpointContract(t *testing.T) {
	endpointtest.RunEndpointContract(t, func(t *testing.T) endpointtest.Harness {
		p := newPinger()
		path := filepath.Join(shortDir(t), "ep", "ping.sock")
		return endpointtest.Harness{
			Path: path, Protocol: pingProto,
			Listen: func() (endpointtest.Endpoint, error) {
				e, err := endpoint.Listen[ping, pong](path, pingProto, p)
				if err != nil {
					return nil, err
				}
				return e, nil
			},
			Call:      func(ctx context.Context) error { _, err := callPing(ctx, path, "alpha"); return err },
			CallOther: func(ctx context.Context) error { _, err := callPing(ctx, path, "beta"); return err },
			Request:   `{"schema":"guildhall.test-ping/1","owner":"alpha"}`,
			Foreign:   `{"schema":"guildhall.test-ping/1","owner":"beta"}`,
			Served:    func() int { return int(p.served.Load()) },
		}
	})
}

func TestListenRefusesALongPath(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, strings.Repeat("p", endpoint.MaxPath))
	if _, err := endpoint.Listen[ping, pong](path, pingProto, newPinger()); !errors.Is(err, endpoint.ErrPath) {
		t.Fatalf("Listen at a %d-byte path = %v, want ErrPath", len(path), err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("a refused Listen created %v (%v)", entries, err)
	}
}

func TestCallWithNothingListening(t *testing.T) {
	path := filepath.Join(shortDir(t), "ping.sock")
	ctx, cancel := context.WithTimeout(context.Background(), endpointtest.Ceiling())
	defer cancel()
	if _, err := callPing(ctx, path, "alpha"); !errors.Is(err, endpoint.ErrNotListening) {
		t.Errorf("Call with no socket file = %v, want ErrNotListening", err)
	}
	endpointtest.StaleSocket(t, path)
	if _, err := callPing(ctx, path, "alpha"); !errors.Is(err, endpoint.ErrNotListening) {
		t.Errorf("Call to a stale socket = %v, want ErrNotListening", err)
	}
}

// TestABlockedRequestDelaysNoOther: requests are served concurrently, and the caller's context bounds a Call.
func TestABlockedRequestDelaysNoOther(t *testing.T) {
	path := filepath.Join(shortDir(t), "ping.sock")
	p := newPinger()
	listenPing(t, path, p)
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), endpointtest.Ceiling())
		defer cancel()
		_, err := endpoint.Call(ctx, path, pingProto, ping{Schema: pingProto.Current(), Owner: "alpha", Block: true}, func(r pong) bool { return r.Owner == "alpha" })
		done <- err
	}()
	select {
	case <-p.entered:
	case <-time.After(endpointtest.Ceiling()):
		t.Fatal("the blocking ping did not reach the server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), endpointtest.Ceiling())
	defer cancel()
	if _, err := callPing(ctx, path, "alpha"); err != nil {
		t.Errorf("ping during a blocked one: %v", err)
	}
	short, cancelShort := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelShort()
	go func() { <-p.entered }()
	_, err := endpoint.Call(short, path, pingProto, ping{Schema: pingProto.Current(), Owner: "alpha", Block: true}, func(r pong) bool { return true })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Call past its context's deadline = %v, want DeadlineExceeded", err)
	}
	close(p.release)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("released ping: %v", err)
		}
	case <-time.After(endpointtest.Ceiling()):
		t.Fatal("the released ping was not answered")
	}
}

// replyServer answers every connection with reply after reading one line.
func replyServer(t *testing.T, path, reply string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.SetDeadline(time.Now().Add(endpointtest.Ceiling()))
			_, _ = bufio.NewReader(c).ReadBytes('\n')
			_, _ = c.Write([]byte(reply + "\n"))
			_ = c.Close()
		}
	}()
}

// TestCallRefusesUnsupportedReplies: Call accepts only a strict reply in the consumer's protocol naming the owner.
func TestCallRefusesUnsupportedReplies(t *testing.T) {
	for name, tc := range map[string]struct {
		reply string
		want  error
		newer bool
	}{
		"newer version":   {`{"schema":"guildhall.test-ping/2","owner":"alpha"}`, errProtocol, true},
		"missing version": {`{"owner":"alpha"}`, errProtocol, false},
		"unknown field":   {`{"schema":"guildhall.test-ping/1","owner":"alpha","extra":1}`, errProtocol, false},
		"duplicate key":   {`{"schema":"guildhall.test-ping/1","owner":"alpha","owner":"alpha"}`, errProtocol, false},
		"oversize":        {`{"schema":"guildhall.test-ping/1","owner":"` + strings.Repeat("a", 300) + `"}`, errProtocol, false},
		"not json":        {"pong", errProtocol, false},
		"another owner":   {`{"schema":"guildhall.test-ping/1","owner":"beta"}`, errMismatch, false},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(shortDir(t), "ping.sock")
			replyServer(t, path, tc.reply)
			ctx, cancel := context.WithTimeout(context.Background(), endpointtest.Ceiling())
			defer cancel()
			_, err := callPing(ctx, path, "alpha")
			if !errors.Is(err, tc.want) {
				t.Fatalf("Call = %v, want %v", err, tc.want)
			}
			if errors.Is(err, strictjson.ErrNewerSchema) != tc.newer {
				t.Errorf("Call = %v; newer schema reported %v, want %v", err, !tc.newer, tc.newer)
			}
		})
	}
	path := filepath.Join(shortDir(t), "ping.sock")
	replyServer(t, path, `{"schema":"guildhall.test-ping/1","owner":"alpha","error":"busy"}`)
	ctx, cancel := context.WithTimeout(context.Background(), endpointtest.Ceiling())
	defer cancel()
	if rep, err := callPing(ctx, path, "alpha"); err != nil || rep.Error != "busy" {
		t.Errorf("a refusal reply: %+v, %v; want it returned for the consumer to read", rep, err)
	}
}

func ExampleProtocol_Current() {
	fmt.Println(pingProto.Current())
	// Output: guildhall.test-ping/1
}
