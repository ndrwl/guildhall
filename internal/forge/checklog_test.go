package forge_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
)

// TestTailWriter: a log within the limit is kept whole; a longer one keeps its tail from a line boundary, so no
// line or UTF-8 rune is split, and Dropped counts every byte cut from the head.
func TestTailWriter(t *testing.T) {
	for _, tc := range []struct {
		name, log string
		limit     int64
		chunk     int
		want      string
	}{
		{name: "under the cap", log: "one\ntwo\n", limit: 64, want: "one\ntwo\n"},
		{name: "at the cap", log: "one\ntwo\n", limit: 8, want: "one\ntwo\n"},
		{name: "over the cap at a line boundary", log: "one\ntwo\nsix\n", limit: 8, want: "two\nsix\n"},
		{name: "a cut mid-line drops the rest of that line", log: "first line\nsecond\nthird\n", limit: 10, want: "third\n"},
		{name: "streamed in small writes", log: strings.Repeat("0123456789abcde\n", 50), limit: 40, chunk: 3, want: strings.Repeat("0123456789abcde\n", 2)},
		{name: "a cut mid-rune in one long line", log: strings.Repeat("é", 20), limit: 7, want: "ééé"},
		{name: "a cut mid-rune before a newline", log: "ééééé\nok\n", limit: 9, want: "ok\n"},
		{name: "a byte order mark and CRLF under the cap", log: "\xef\xbb\xbfone\r\ntwo\r\n", limit: 64, want: "\xef\xbb\xbfone\r\ntwo\r\n"},
		{name: "a cut drops the byte order mark with the first CRLF line", log: "\xef\xbb\xbfone\r\ntwo\r\nsix\r\n", limit: 10, want: "two\r\nsix\r\n"},
		{name: "a cut between CR and LF drops the rest of that line", log: "\xef\xbb\xbfone\r\ntwo\r\nsix\r\n", limit: 6, want: "six\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &forge.TailWriter{Limit: tc.limit}
			data := []byte(tc.log)
			chunk := cmpOr(tc.chunk, len(data))
			for len(data) > 0 {
				n := min(chunk, len(data))
				if _, err := w.Write(data[:n]); err != nil {
					t.Fatal(err)
				}
				data = data[n:]
			}
			got := w.Log()
			if string(got.Text) != tc.want || got.Dropped != int64(len(tc.log)-len(tc.want)) || !utf8.Valid(got.Text) {
				t.Errorf("Log() = %q dropping %d, want %q dropping %d", got.Text, got.Dropped, tc.want, len(tc.log)-len(tc.want))
			}
			if !strings.HasSuffix(tc.log, string(got.Text)) || int64(len(got.Text)) > tc.limit {
				t.Errorf("Log() = %q is not a tail of the log within %d bytes", got.Text, tc.limit)
			}
		})
	}
}

func cmpOr(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// TestGitHubCheckLog: CheckLog reads the job's log with gh api and keeps its tail; an empty job, a missing,
// expired or forbidden log and gh not signed in are ErrLogUnavailable, an HTTP 404 or 410 is ErrLogExpired too,
// and a cancelled context is returned as it is.
func TestGitHubCheckLog(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	f := h.Forge.(forge.GitHub)
	ctx := context.Background()
	commit := forge.Commit{Repository: h.Repository, Remote: remote, Head: h.Heads[0]}
	s := g.readState(t)
	s.Logs = map[string]string{"11": "2026-10-29T10:00:00.0000000Z ##[group]Run go test\nFAIL\n", "12": strings.Repeat("line\n", 100), "13": "", "14": "x", "15": "y"}
	s.LogStatus = map[string]int{"14": 410, "15": 403}
	g.writeState(t, s)

	got, err := f.CheckLog(ctx, commit, "11", 1<<20)
	if err != nil || string(got.Text) != s.Logs["11"] || got.Dropped != 0 {
		t.Errorf("CheckLog(11) = %q, %d, %v; want the whole log", got.Text, got.Dropped, err)
	}
	got, err = f.CheckLog(ctx, commit, "12", 64)
	if err != nil || !bytes.Equal(got.Text, []byte(strings.Repeat("line\n", 12))) || got.Dropped != 5*88 {
		t.Errorf("CheckLog(12) = %q, %d, %v; want the last 12 lines", got.Text, got.Dropped, err)
	}
	if got, err = f.CheckLog(ctx, commit, "13", 64); err != nil || len(got.Text) != 0 {
		t.Errorf("CheckLog(13) = %q, %v; want an empty log", got.Text, err)
	}
	// A real job log, as gh api printed it on 2026-10-30 (docs/evidence/failed-job-logs-redirect-2026-10-30.txt),
	// starts with a byte order mark, ends lines with CRLF and carries ANSI colour codes, which gh prints only
	// with --allow-escape-sequences.
	s = g.readState(t)
	s.Logs["16"] = "\xef\xbb\xbf2026-10-30T09:00:00.0000000Z ##[group]Run go test\r\n2026-10-30T09:00:01.0000000Z \x1b[36;1mgo test ./...\x1b[0m\r\n"
	g.writeState(t, s)
	if got, err = f.CheckLog(ctx, commit, "16", 1<<20); err != nil || string(got.Text) != s.Logs["16"] {
		t.Errorf("CheckLog(16) = %q, %v; want the log byte for byte, escape sequences included", got.Text, err)
	}

	calls := len(g.readState(t).Calls)
	for _, tc := range []struct {
		name, job string
		expired   bool
		reason    string
	}{
		{name: "an empty job", job: "", reason: "not a GitHub Actions job"},
		{name: "an unknown job", job: "99", expired: true, reason: "HTTP 404"},
		{name: "an expired log", job: "14", expired: true, reason: "HTTP 410"},
		{name: "a forbidden log", job: "15", reason: "HTTP 403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.CheckLog(ctx, commit, tc.job, 1<<20)
			if !errors.Is(err, forge.ErrLogUnavailable) || errors.Is(err, forge.ErrLogExpired) != tc.expired || !strings.Contains(forge.LogReason(err), tc.reason) {
				t.Errorf("CheckLog(%q) = %q, %v; want unavailable (expired %v) naming %q", tc.job, got.Text, err, tc.expired, tc.reason)
			}
		})
	}
	if n := len(g.readState(t).Calls); n != calls+3 {
		t.Errorf("gh ran %d times for 4 reads, want 3: an empty job runs no gh", n-calls)
	}

	s = g.readState(t)
	s.SignedOut = true
	g.writeState(t, s)
	if _, err := f.CheckLog(ctx, commit, "11", 1<<20); !errors.Is(err, forge.ErrLogUnavailable) || errors.Is(err, forge.ErrLogExpired) || !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("CheckLog signed out = %v, want unavailable carrying gh's message", err)
	}
	s.SignedOut, s.HangLogs = false, true
	g.writeState(t, s)
	f.Timeout = 500 * time.Millisecond // timing:allow intentional timeout of a tool that never answers; no readiness assertion.
	if _, err := f.CheckLog(ctx, commit, "11", 1<<20); !errors.Is(err, forge.ErrLogUnavailable) || !strings.Contains(err.Error(), "500ms") {
		t.Errorf("CheckLog hung = %v, want unavailable naming the timeout", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.CheckLog(cancelled, commit, "11", 1<<20); !errors.Is(err, context.Canceled) || errors.Is(err, forge.ErrLogUnavailable) {
		t.Errorf("CheckLog cancelled = %v, want context.Canceled unwrapped", err)
	}
}

// TestFakeCheckLog: the fake keeps the same tail as GitHub, counts its calls, and gives SetLogError's failure.
func TestFakeCheckLog(t *testing.T) {
	f := forgetest.NewFake()
	ctx := context.Background()
	commit := forge.Commit{Repository: "/repo", Remote: remote, Head: strings.Repeat("a", 40)}
	f.SetLog("1", strings.Repeat("line\n", 100))
	if got, err := f.CheckLog(ctx, commit, "1", 64); err != nil || string(got.Text) != strings.Repeat("line\n", 12) || got.Dropped != 440 {
		t.Errorf("CheckLog(1) = %q, %d, %v", got.Text, got.Dropped, err)
	}
	f.SetLogError("1", errors.New("boom"))
	if _, err := f.CheckLog(ctx, commit, "1", 64); err == nil || err.Error() != "boom" {
		t.Errorf("CheckLog after SetLogError = %v, want boom", err)
	}
	if _, err := f.CheckLog(ctx, commit, "2", 64); !errors.Is(err, forge.ErrLogExpired) {
		t.Errorf("CheckLog of an unset job = %v, want expired", err)
	}
	if _, err := f.CheckLog(ctx, commit, "", 64); !errors.Is(err, forge.ErrLogUnavailable) {
		t.Errorf("CheckLog of no job = %v, want unavailable", err)
	}
	if n := f.LogCalls(); n != 4 {
		t.Errorf("LogCalls() = %d, want 4", n)
	}
}
