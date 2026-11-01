package claude

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/workbench"
)

// updating reports whether -update, which session_test.go defines in this test binary, asks for golden files to
// be rewritten.
func updating() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

// fixtureTime stamps every event in the stream golden files.
var fixtureTime = time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)

// sequence records the event file's and stdout's writes in the order they happen.
type sequence struct{ writes []string }

type seqWriter struct {
	s      *sequence
	prefix string
}

func (w seqWriter) Write(p []byte) (int, error) {
	w.s.writes = append(w.s.writes, w.prefix+string(p))
	return len(p), nil
}

// fixtureInput is the recorded stream with two lines a live session does not produce on demand: an oversized line,
// larger than the 4 MiB line bound, after its second line, and a malformed line, the recorded tool call line cut
// short, after its sixth.
func fixtureInput(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "stream", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	oversized := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("x", maxOutputBytes) + `"}]}}` + "\n"
	malformed := lines[5][:len(lines[5])/2] + "\n"
	return []byte(strings.Join(lines[:2], "") + oversized + strings.Join(lines[2:6], "") + malformed + strings.Join(lines[6:], ""))
}

// streamFixture feeds the recorded stream to a readableStream in uneven pieces and returns its stdout, its events
// and the order of their writes.
func streamFixture(t *testing.T) (string, []byte, *sequence) {
	t.Helper()
	seq := &sequence{}
	var out, events bytes.Buffer
	ew := workbench.NewWriter(multi{&events, seqWriter{seq, "E "}})
	ew.Now = func() time.Time { return fixtureTime }
	result := &limitedBuffer{limit: maxOutputBytes}
	stream := &readableStream{out: multi{&out, seqWriter{seq, "O "}}, events: ew, result: result}
	input := fixtureInput(t)
	for i, n := 0, 1; i < len(input); i, n = i+n, n%977+13 {
		_, _ = stream.Write(input[i:min(i+n, len(input))])
	}
	stream.finish()
	if !stream.sawResult.Load() {
		t.Error("the recorded result event was not seen")
	}
	if u, err := ParseUsage(result.buf.Bytes()); err != nil || u.OutputTokens != 604 {
		t.Errorf("usage from the recorded result = %+v, %v", u, err)
	}
	return out.String(), events.Bytes(), seq
}

type multi []interface{ Write([]byte) (int, error) }

func (m multi) Write(p []byte) (int, error) {
	for _, w := range m {
		_, _ = w.Write(p)
	}
	return len(p), nil
}

// TestReadableStreamFixtureEvents: a recorded stream gives the expected Workbench events, its stdout is the
// wrapper's plain text and tool lines exactly as before the Workbench, and thinking, system, malformed and
// oversized lines give neither.
func TestReadableStreamFixtureEvents(t *testing.T) {
	stdout, events, _ := streamFixture(t)
	wantOut := "# Plan\n\nThis fixture will **run two tools in parallel** and then check `go test`:\n\n" +
		"- Spawn a subagent to count lines in `spec.md`\n- Read `spec.md` directly\n\n```go\nfunc main() { fmt.Println(\"plan\") }\n```\n" +
		"tool: Agent\ntool: Read /work/spec.md\ntool: Bash wc -l spec.md\ntool: Bash go test ./...\ntool: Edit /work/x_test.go\n" +
		"`go test ./...` failed at `x_test.go:5` on `t.Fatal(\"a\")`, so I replaced that call with `_ = t`. I haven't re-run the tests since the edit.\n"
	if stdout != wantOut {
		t.Errorf("stdout =\n%q\nwant\n%q", stdout, wantOut)
	}
	golden := filepath.Join("testdata", "stream", "session.events.jsonl")
	if updating() {
		if err := os.WriteFile(golden, events, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(events, want) {
		t.Errorf("events =\n%s\nwant\n%s", events, want)
	}
	got := workbench.ReadAll(bytes.NewReader(events))
	kinds := make([]string, len(got))
	for i, e := range got {
		kinds[i] = string(e.Kind)
	}
	// The Agent and Read calls are one assistant message's parallel calls; the subagent's own Bash call and result
	// come between the Read's result and the Agent's.
	wantKinds := "text tool_call tool_call tool_result tool_call tool_result tool_result tool_call tool_result tool_call tool_result text"
	if strings.Join(kinds, " ") != wantKinds {
		t.Errorf("event kinds = %s, want %s", strings.Join(kinds, " "), wantKinds)
	}
	const agentID, readID = "toolu_01XkoctdZAyGGVJKfSja3B5z", "toolu_018qeHmRo2z54S1GRUBJbP9M"
	if got[1].ID != agentID || got[2].ID != readID {
		t.Errorf("parallel calls = %s, %s; want the Agent then the Read", got[1].ID, got[2].ID)
	}
	// The out-of-order results name their own calls, and the error and the array content are kept.
	if r := got[3]; r.ID != readID || r.Error == nil || *r.Error || *r.Bytes != int64(len("1\t# Spec\n2\t\n3\tDo the thing.\n4\t")) || *r.Lines != 4 {
		t.Errorf("Read result = %+v", r)
	}
	if r := got[6]; r.ID != agentID || r.Error == nil || *r.Error || *r.Bytes != 739 || *r.Lines != 8 {
		t.Errorf("Agent array-content result = %+v", r)
	}
	errOut := "Exit code 1\n--- FAIL: TestX (0.00s)\n    x_test.go:5: a\nFAIL\nFAIL\tx\t0.002s\nFAIL"
	if r := got[8]; r.ID != "toolu_01BwBo6Rphw4q5o9kdWge3an" || r.Error == nil || !*r.Error || *r.Bytes != int64(len(errOut)) || *r.Lines != 6 {
		t.Errorf("Bash error result = %+v", r)
	}
}

// TestReadableStreamWritesEachEventBeforeItsLines: every event that carries log lines is written to the event file
// just before those lines go to stdout, so a bridge that has read a line has its event too.
func TestReadableStreamWritesEachEventBeforeItsLines(t *testing.T) {
	_, _, seq := streamFixture(t)
	for i, w := range seq.writes {
		text, ok := strings.CutPrefix(w, "O ")
		if !ok {
			continue
		}
		if i == 0 || !strings.HasPrefix(seq.writes[i-1], "E ") {
			t.Fatalf("stdout %q was not just after its event: %q", text, seq.writes[:i])
		}
		e, ok := workbench.Decode([]byte(strings.TrimPrefix(seq.writes[i-1], "E ")))
		if !ok || strings.Join(e.Log, "\n")+"\n" != text {
			t.Errorf("stdout %q follows event %+v, whose log differs", text, e)
		}
	}
}

// TestReadableStreamEventFileCap: at the file cap the wrapper writes one truncated event and no more, and stdout
// still carries every line.
func TestReadableStreamEventFileCap(t *testing.T) {
	var out, events bytes.Buffer
	ew := workbench.NewWriter(&events)
	ew.Limit = 1200
	stream := &readableStream{out: &out, events: ew, result: &limitedBuffer{limit: maxOutputBytes}}
	for range 20 {
		_, _ = stream.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}` + "\n"))
	}
	if got := strings.Count(out.String(), "working on it\n"); got != 20 {
		t.Errorf("stdout has %d lines, want 20", got)
	}
	if events.Len() > 1200 {
		t.Errorf("the event file is %d bytes, over its 1200 byte cap", events.Len())
	}
	got := workbench.ReadAll(bytes.NewReader(events.Bytes()))
	if len(got) < 2 || got[len(got)-1].Kind != workbench.KindTruncated {
		t.Fatalf("events = %+v, want text events ending with one truncated event", got)
	}
	for _, e := range got[:len(got)-1] {
		if e.Kind != workbench.KindText {
			t.Errorf("event before the cap = %+v", e)
		}
	}
	if workbench.MaxFileBytes != 64<<20 {
		t.Errorf("MaxFileBytes = %d, want 64 MiB", workbench.MaxFileBytes)
	}
}

// TestReadableStreamIgnoresEventWriterFailure: a failing event file never breaks Claude's pipe or the stdout lines.
func TestReadableStreamIgnoresEventWriterFailure(t *testing.T) {
	var out bytes.Buffer
	result := &limitedBuffer{limit: maxOutputBytes}
	stream := &readableStream{out: &out, events: workbench.NewWriter(brokenPresentation{}), result: result}
	data := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"working"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"a\nb"}]}}` + "\n" + `{"type":"result","result":"done"}` + "\n")
	n, err := stream.Write(data)
	if err != nil || n != len(data) || out.String() != "working\ntool: Bash ls\n" || !bytes.Contains(result.buf.Bytes(), []byte("done")) {
		t.Fatalf("event writer failure changed the stream: %d, %v, %q", n, err, out.String())
	}
}

// TestResultSize: a tool result's size is its text's bytes and lines, for string or block content.
func TestResultSize(t *testing.T) {
	image := `{"type":"image","source":{"type":"base64","data":"QUJD"}}`
	for _, tc := range []struct {
		raw   string
		size  int64
		lines int
	}{
		{`"one line"`, 8, 1},
		{`"a\nb\n"`, 4, 2},
		{`""`, 0, 0},
		{`[{"type":"text","text":"x\ny"},{"type":"text","text":"z"}]`, 4, 3},
		{`[` + image + `]`, int64(len(image)), 0},
		{`null`, 0, 0},
		{`{}`, 0, 0},
	} {
		size, lines := resultSize(json.RawMessage(tc.raw))
		if size != tc.size || lines != tc.lines {
			t.Errorf("resultSize(%s) = %d, %d; want %d, %d", tc.raw, size, lines, tc.size, tc.lines)
		}
	}
}
