package endpoint_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/endpoint"
	"github.com/ndrwl/guildhall/internal/endpoint/endpointtest"
)

// encodingPong holds the exchange after Serve returns but before its reply is
// written, the interleaving that used to let a launcher exit during a stop reply.
type encodingPong struct {
	entered chan struct{}
	release chan struct{}
}

func (p encodingPong) MarshalJSON() ([]byte, error) {
	close(p.entered)
	<-p.release
	return json.Marshal(pong{Schema: pingProto.Current(), Owner: "alpha"})
}

type encodingPinger struct{ reply encodingPong }

func (encodingPinger) Owns(ping) bool               { return true }
func (s encodingPinger) Serve(ping) encodingPong    { return s.reply }
func (s encodingPinger) Refuse(string) encodingPong { return s.reply }

func TestCloseWaitsForReplyWrite(t *testing.T) {
	reply := encodingPong{entered: make(chan struct{}), release: make(chan struct{})}
	path := filepath.Join(shortDir(t), "ping.sock")
	e, err := endpoint.Listen[ping, encodingPong](path, pingProto, encodingPinger{reply})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	// Release before endpoint cleanup even if an assertion fails.
	release := sync.OnceFunc(func() { close(reply.release) })
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), endpointtest.Ceiling())
	defer cancel()
	answered := make(chan error, 1)
	go func() {
		r, err := callPing(ctx, path, "alpha")
		if err == nil && (r.Owner != "alpha" || r.Error != "") {
			err = errProtocol
		}
		answered <- err
	}()
	select {
	case <-reply.entered:
	case <-ctx.Done():
		t.Fatal("reply encoder was not entered")
	}
	closed := make(chan error, 2)
	for range 2 {
		go func() { closed <- e.Close() }()
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before the stop reply was written: %v", err)
	case <-time.After(100 * time.Millisecond):
		// Close must still be draining the deliberately blocked reply.
	}
	release()
	for range 2 {
		select {
		case err := <-closed:
			if err != nil {
				t.Errorf("Close: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("Close did not finish after the reply was released")
		}
	}
	select {
	case err := <-answered:
		if err != nil {
			t.Fatalf("stop reply: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stop reply was not received")
	}
}
