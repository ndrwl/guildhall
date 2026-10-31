package claude

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestReadableStreamBoundsAndFinalResult(t *testing.T) {
	var out bytes.Buffer
	result := &limitedBuffer{limit: maxOutputBytes}
	stream := &readableStream{out: &out, result: result}
	// More than the entire old capture budget precedes the final event.
	_, _ = stream.Write([]byte(strings.Repeat("x", maxOutputBytes+1) + "\ninvalid\n"))
	event := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"` + strings.Repeat("a", 180) + `\nrest"}},{"type":"tool_result","content":"not displayed"}]}}` + "\n"
	for _, b := range []byte(event) {
		_, _ = stream.Write([]byte{b})
	}
	final := `{"type":"result","result":"done","total_cost_usd":0.5,"usage":{"input_tokens":10,"output_tokens":20}}`
	_, _ = stream.Write([]byte(final))
	stream.finish()
	if want := "tool: Bash " + strings.Repeat("a", 160) + "...\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
	usage, err := ParseUsage(result.buf.Bytes())
	if err != nil || usage.InputTokens != 10 || usage.OutputTokens != 20 || usage.CostUSD != 0.5 {
		t.Fatalf("final usage lost: %+v, %v", usage, err)
	}
}

// TestReadableStreamSignalsResultEvent: the result-event signal is raised only once the whole result event has
// arrived, however Write splits it, and not by other events.
func TestReadableStreamSignalsResultEvent(t *testing.T) {
	stream := &readableStream{out: &bytes.Buffer{}, result: &limitedBuffer{limit: maxOutputBytes}}
	_, _ = stream.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}` + "\n"))
	if stream.sawResult.Load() {
		t.Fatal("an assistant event raised the result signal")
	}
	final := `{"type":"result","result":"done"}` + "\n"
	half := len(final) / 2
	_, _ = stream.Write([]byte(final[:half]))
	if stream.sawResult.Load() {
		t.Fatal("half a result event raised the result signal")
	}
	_, _ = stream.Write([]byte(final[half:]))
	if !stream.sawResult.Load() {
		t.Fatal("a result event split across writes did not raise the result signal")
	}
	_, _ = stream.Write([]byte(final))
	if !stream.sawResult.Load() {
		t.Fatal("a second result event cleared the result signal")
	}
}

type brokenPresentation struct{}

func (brokenPresentation) Write([]byte) (int, error) { return 0, errors.New("pane gone") }

func TestReadableStreamIgnoresSinkFailure(t *testing.T) {
	result := &limitedBuffer{limit: maxOutputBytes}
	stream := &readableStream{out: brokenPresentation{}, result: result}
	data := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}` + "\n" + `{"type":"result","result":"done"}` + "\n")
	n, err := stream.Write(data)
	if err != nil || n != len(data) || !bytes.Contains(result.buf.Bytes(), []byte("done")) {
		t.Fatalf("sink failure stopped stream: %d, %v, %s", n, err, result.buf.Bytes())
	}
}
