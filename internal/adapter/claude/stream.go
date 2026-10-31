package claude

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/ndrwl/guildhall/internal/workbench"
)

// readableStream consumes bounded NDJSON events without retaining the transcript.
// A failed presentation sink never breaks the worker's stdout pipe. Only a
// result event is retained for the existing usage/denial/final-text reporter.
// Each text and tool call it shows, and each tool result, is also written to
// events, the Workbench event file, before its plain lines go to out; events'
// write errors are ignored too, and a nil events writes none.
// os/exec serializes calls to Write; finish is called only after Wait.
// sawResult is set once a result event arrives; it alone is read while Claude runs.
type readableStream struct {
	out       io.Writer
	events    *workbench.Writer
	result    *limitedBuffer
	line      []byte
	dropping  bool
	sawResult atomic.Bool
}

func (s *readableStream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		end := len(p)
		if i >= 0 {
			end = i
		}
		if !s.dropping {
			if len(s.line)+end > maxOutputBytes {
				s.line = nil
				s.dropping = true
			} else {
				s.line = append(s.line, p[:end]...)
			}
		}
		if i < 0 {
			break
		}
		s.finish()
		p = p[i+1:]
	}
	return n, nil
}

func (s *readableStream) finish() {
	if !s.dropping && len(s.line) > 0 {
		s.event(s.line)
	}
	s.line = s.line[:0]
	s.dropping = false
}

func (s *readableStream) event(line []byte) {
	var event struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				ID    string `json:"id"`
				Name  string `json:"name"`
				Input struct {
					FilePath     string `json:"file_path"`
					NotebookPath string `json:"notebook_path"`
					Command      string `json:"command"`
				} `json:"input"`
				ToolUseID string          `json:"tool_use_id"`
				IsError   bool            `json:"is_error"`
				Content   json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &event) != nil {
		return
	}
	switch event.Type {
	case "result":
		// Do not let earlier assistant/tool volume consume the result budget.
		s.result.buf.Reset()
		s.result.truncated = false
		_, _ = s.result.Write(line)
		s.sawResult.Store(true)
	case "assistant", "user":
		for _, block := range event.Message.Content {
			switch {
			case block.Type == "text" && event.Type == "assistant":
				text := plainText(block.Text)
				s.show(workbench.Text(text, nil), text)
			case block.Type == "tool_use" && event.Type == "assistant":
				subject := strings.Join(strings.Fields(cmp.Or(block.Input.FilePath, block.Input.NotebookPath, block.Input.Command)), " ")
				runes := []rune(plainText(subject))
				if len(runes) > 160 {
					subject = string(runes[:160]) + "..."
				} else {
					subject = string(runes)
				}
				s.show(workbench.ToolCall(plainText(block.ID), plainText(block.Name), subject, nil), strings.TrimSpace("tool: "+plainText(block.Name)+" "+subject))
			case block.Type == "tool_result":
				size, lines := resultSize(block.Content)
				s.events.Write(workbench.ToolResult(plainText(block.ToolUseID), block.IsError, size, lines))
			}
		}
	}
}

// show writes e, carrying the lines of text as its log, to the event file, and then text as one line to out.
func (s *readableStream) show(e workbench.Event, text string) {
	e.Log = strings.Split(text, "\n")
	s.events.Write(e)
	_, _ = fmt.Fprintln(s.out, text)
}

// resultSize is the size of a tool_result's content, a string or an array of blocks: the bytes and lines of its
// text, and the encoded bytes of any block that is not text, such as an image.
func resultSize(content json.RawMessage) (int64, int) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return int64(len(text)), lineCount(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return 0, 0
	}
	var raw []json.RawMessage
	_ = json.Unmarshal(content, &raw)
	var size int64
	lines := 0
	for i, b := range blocks {
		if b.Type == "text" {
			size += int64(len(b.Text))
			lines += lineCount(b.Text)
		} else if i < len(raw) {
			size += int64(len(raw[i]))
		}
	}
	return size, lines
}

// lineCount is the number of lines in text: none when it is empty, and a last line without a newline counts.
func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1
}

// Keep text readable without emitting terminal control sequences.
func plainText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
}
