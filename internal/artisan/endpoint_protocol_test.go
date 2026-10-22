package artisan_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/strictjson"
	"github.com/ndrwl/guildhall/internal/testenv"
)

const controlSchema = "guildhall.attempt-control/1"

// requestLine fills format's op, attempt and launcher verbs with op, this
// test's attempt and this process's identity, and ends the line.
func requestLine(t *testing.T, format string, op artisan.EndpointOp) string {
	t.Helper()
	launcher, err := json.Marshal(mustSelf(t))
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(format, op, testKey.String(), launcher) + "\n"
}

// TestEndpointAcceptsVersionedRequest: a version 1 request reaches the
// handler and the reply carries the same schema.
func TestEndpointAcceptsVersionedRequest(t *testing.T) {
	path := endpointPath(t)
	h := &testHandler{}
	listen(t, path, h)
	rep := rawExchange(t, path, requestLine(t, `{"schema":"`+controlSchema+`","op":%q,"attempt":%q,"launcher":%s}`, artisan.OpStatus))
	if rep["error"] != nil || rep["schema"] != controlSchema || rep["state"] == nil {
		t.Errorf("version 1 status reply = %v", rep)
	}
	if n := h.statuses.Load(); n != 1 {
		t.Errorf("handler saw %d status calls, want 1", n)
	}
}

// TestEndpointRefusesUnsupportedRequestsBeforeDispatch: requests outside
// protocol version 1 are refused with a versioned error reply, and no
// status, stop or cancel handler runs.
func TestEndpointRefusesUnsupportedRequestsBeforeDispatch(t *testing.T) {
	path := endpointPath(t)
	h := &testHandler{}
	listen(t, path, h)
	cases := map[string]string{
		"future version":        `{"schema":"guildhall.attempt-control/99","version":99,"op":%q,"attempt":%q,"launcher":%s}`,
		"next version":          `{"schema":"guildhall.attempt-control/2","op":%q,"attempt":%q,"launcher":%s}`,
		"missing version":       `{"op":%q,"attempt":%q,"launcher":%s}`,
		"version zero":          `{"schema":"guildhall.attempt-control/0","op":%q,"attempt":%q,"launcher":%s}`,
		"other protocol":        `{"schema":"guildhall.message/1","op":%q,"attempt":%q,"launcher":%s}`,
		"duplicate schema":      `{"schema":"` + controlSchema + `","schema":"` + controlSchema + `","op":%q,"attempt":%q,"launcher":%s}`,
		"duplicate op":          `{"schema":"` + controlSchema + `","op":"status","op":%q,"attempt":%q,"launcher":%s}`,
		"unknown field":         `{"schema":"` + controlSchema + `","op":%q,"attempt":%q,"launcher":%s,"force":true}`,
		"case variant":          `{"schema":"` + controlSchema + `","Op":%q,"attempt":%q,"launcher":%s}`,
		"unknown in launcher":   `{"schema":"` + controlSchema + `","op":%q,"attempt":%q,"launcher":%.0s{"pid":1,"token":"t","boot":"b"}}`,
		"duplicate in launcher": `{"schema":"` + controlSchema + `","op":%q,"attempt":%q,"launcher":%.0s{"pid":1,"pid":2,"token":"t"}}`,
		"launcher pid string":   `{"schema":"` + controlSchema + `","op":%q,"attempt":%q,"launcher":%.0s{"pid":"1","token":"t"}}`,
		"launcher not object":   `{"schema":"` + controlSchema + `","op":%q,"attempt":%q,"launcher":%.0s"1:t"}`,
		"launcher list":         `{"schema":"` + controlSchema + `","op":%q,"attempt":%q,"launcher":[%s]}`,
	}
	for _, op := range []artisan.EndpointOp{artisan.OpStatus, artisan.OpStop, artisan.OpCancel} {
		for name, format := range cases {
			t.Run(string(op)+"/"+name, func(t *testing.T) {
				rep := rawExchange(t, path, requestLine(t, format, op))
				msg, _ := rep["error"].(string)
				if msg == "" || rep["state"] != nil || rep["schema"] != controlSchema || rep["attempt"] != testKey.String() {
					t.Fatalf("reply %v, want a version 1 error reply naming the attempt", rep)
				}
				if strings.Contains(name, "version") && name != "missing version" && name != "version zero" && !strings.Contains(msg, controlSchema) {
					t.Errorf("newer-version refusal %q does not name %s", msg, controlSchema)
				}
			})
		}
	}
	if s, p, c := h.statuses.Load(), h.stops.Load(), h.cancels.Load(); s != 0 || p != 0 || c != 0 {
		t.Fatalf("refused requests reached handlers: status=%d stop=%d cancel=%d", s, p, c)
	}
}

// replyServer answers every connection with reply after reading one line.
func replyServer(t *testing.T, path, reply string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
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
			_ = c.SetDeadline(time.Now().Add(testenv.Ceiling(10 * time.Second)))
			_, _ = bufio.NewReader(c).ReadBytes('\n')
			_, _ = c.Write([]byte(reply + "\n"))
			_ = c.Close()
		}
	}()
}

// TestCallRefusesUnsupportedReplies: Call accepts only a version 1 reply.
func TestCallRefusesUnsupportedReplies(t *testing.T) {
	self := mustSelf(t)
	launcher, err := json.Marshal(self)
	if err != nil {
		t.Fatal(err)
	}
	reply := func(schema, extra string) string {
		return fmt.Sprintf(`{%s"attempt":%q,"launcher":%s,"state":{"phase":"stopped"}%s}`, schema, testKey.String(), launcher, extra)
	}
	v1 := `"schema":"` + controlSchema + `",`
	for name, tc := range map[string]struct {
		reply string
		newer bool
	}{
		"future version":    {reply(`"schema":"guildhall.attempt-control/2",`, ""), true},
		"missing version":   {reply("", ""), false},
		"unknown field":     {reply(v1, `,"stopped":true`), false},
		"duplicate state":   {reply(v1, `,"state":{"phase":"running"}`), false},
		"unknown in state":  {strings.Replace(reply(v1, ""), `{"phase":"stopped"}`, `{"phase":"stopped","exit":0}`, 1), false},
		"malformed members": {strings.Replace(reply(v1, ""), `{"phase":"stopped"}`, `{"phase":"stopped","members":[{"pid":1,"token":"t","extra":1}]}`, 1), false},
		"not json":          {"stopped", false},
	} {
		t.Run(name, func(t *testing.T) {
			path := endpointPath(t)
			replyServer(t, path, tc.reply)
			ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(10*time.Second))
			defer cancel()
			st, err := artisan.Call(ctx, path, testKey, self, artisan.OpStop)
			if !errors.Is(err, artisan.ErrEndpointProtocol) {
				t.Fatalf("Call = %+v, %v; want ErrEndpointProtocol", st, err)
			}
			if errors.Is(err, strictjson.ErrNewerSchema) != tc.newer {
				t.Errorf("Call = %v; newer schema reported %v, want %v", err, !tc.newer, tc.newer)
			}
		})
	}

	path := endpointPath(t)
	replyServer(t, path, reply(v1, ""))
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(10*time.Second))
	defer cancel()
	if st, err := artisan.Call(ctx, path, testKey, self, artisan.OpStop); err != nil || st.Phase != "stopped" {
		t.Errorf("version 1 reply: %+v, %v", st, err)
	}
}
