package claude

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// readableStream consumes bounded NDJSON events without retaining the transcript.
// A failed presentation sink never breaks the worker's stdout pipe. Only a
// result event is retained for the existing usage/denial/final-text reporter.
// os/exec serializes calls to Write; finish is called only after Wait.
type readableStream struct {
	out      io.Writer
	result   *limitedBuffer
	line     []byte
	dropping bool
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
				Name  string `json:"name"`
				Input struct {
					FilePath     string `json:"file_path"`
					NotebookPath string `json:"notebook_path"`
					Command      string `json:"command"`
				} `json:"input"`
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
	case "assistant":
		for _, block := range event.Message.Content {
			switch block.Type {
			case "text":
				_, _ = fmt.Fprintln(s.out, plainText(block.Text))
			case "tool_use":
				subject := strings.Join(strings.Fields(cmp.Or(block.Input.FilePath, block.Input.NotebookPath, block.Input.Command)), " ")
				runes := []rune(plainText(subject))
				if len(runes) > 160 {
					subject = string(runes[:160]) + "..."
				} else {
					subject = string(runes)
				}
				_, _ = fmt.Fprintln(s.out, strings.TrimSpace("tool: "+plainText(block.Name)+" "+subject))
			}
		}
	}
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
