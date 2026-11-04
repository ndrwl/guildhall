package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/workbench"
)

// stream decodes `codex exec --json` output, one event per line, as codex-cli 0.163.0 writes it
// (docs/evidence/codex-exec-policy-2026-11-03.txt, section 5). Each agent message and each command it shows goes to
// out as a readable line, and to events, the Workbench event file, with each command's real exit code on its tool
// result; an error event or failed turn is named on errOut. It keeps the last agent message, the summed usage of
// every completed turn, and whether the last turn completed or failed. It ignores events, item types and fields it
// does not know, and lines that are not JSON or longer than maxLineBytes. os/exec serializes calls to Write;
// finish is called only after Wait.
type stream struct {
	out, errOut io.Writer
	events      *workbench.Writer
	line        []byte
	dropping    bool

	final     *string
	completed bool
	failed    string
	errors    []string
	sawUsage  bool
	used      turnUsage
}

// turnUsage is a completed turn's usage block.
type turnUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
}

func (s *stream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		end := len(p)
		if i >= 0 {
			end = i
		}
		if !s.dropping {
			if len(s.line)+end > maxLineBytes {
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

func (s *stream) finish() {
	if !s.dropping && len(s.line) > 0 {
		s.event(s.line)
	}
	s.line = s.line[:0]
	s.dropping = false
}

// event is one --json event.
type event struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage *turnUsage `json:"usage"`
	Item  *eventItem `json:"item"`
}

// eventItem is an item event's item.
type eventItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Text             string `json:"text"`
	Message          string `json:"message"`
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	ExitCode         *int   `json:"exit_code"`
	Server           string `json:"server"`
	Tool             string `json:"tool"`
	Query            string `json:"query"`
}

func (s *stream) event(line []byte) {
	var e event
	if json.Unmarshal(line, &e) != nil {
		return
	}
	switch e.Type {
	case "turn.started":
		s.completed, s.failed = false, ""
	case "turn.completed":
		s.completed = true
		if e.Usage != nil {
			s.sawUsage = true
			s.used.InputTokens += e.Usage.InputTokens
			s.used.CachedInputTokens += e.Usage.CachedInputTokens
			s.used.CacheWriteInputTokens += e.Usage.CacheWriteInputTokens
			s.used.OutputTokens += e.Usage.OutputTokens
		}
	case "turn.failed":
		s.completed = false
		s.failed = "turn failed"
		if e.Error != nil && e.Error.Message != "" {
			s.failed = plainText(e.Error.Message)
		}
		_, _ = fmt.Fprintf(s.errOut, "guildhall: %s: Codex turn failed: %s\n", Subcommand, s.failed)
	case "error":
		msg := plainText(e.Message)
		s.errors = append(s.errors, msg)
		_, _ = fmt.Fprintf(s.errOut, "guildhall: %s: Codex error: %s\n", Subcommand, msg)
	case "item.started", "item.completed":
		if e.Item != nil {
			s.item(e.Type == "item.completed", *e.Item)
		}
	}
}

// item shows one item event: an agent message once completed, which also becomes the final message; a command or
// other tool when it starts, and a command's result, with its exit code, when it completes; and a completed error
// item, a warning, on errOut.
func (s *stream) item(completed bool, it eventItem) {
	id, typ := plainText(it.ID), it.Type
	switch typ {
	case "agent_message":
		if !completed {
			return
		}
		final := it.Text
		s.final = &final
		shown := plainText(it.Text)
		s.show(workbench.Text(shown, nil), shown)
	case "command_execution":
		if !completed {
			subject := cut(plainText(strings.Join(strings.Fields(it.Command), " ")))
			s.show(workbench.ToolCall(id, "shell", subject, nil), strings.TrimSpace("tool: shell "+subject))
			return
		}
		code := -1
		if it.ExitCode != nil {
			code = *it.ExitCode
		}
		e := workbench.ToolResult(id, code != 0, int64(len(it.AggregatedOutput)), lineCount(it.AggregatedOutput))
		e.Exit = &code
		s.events.Write(e)
		if code != 0 {
			_, _ = fmt.Fprintf(s.out, "exit %d\n", code)
		}
	case "mcp_tool_call", "web_search", "file_change":
		if completed {
			return
		}
		subject := cut(plainText(strings.Join(strings.Fields(strings.Join([]string{it.Server, it.Tool, it.Query}, " ")), " ")))
		s.show(workbench.ToolCall(id, typ, subject, nil), strings.TrimSpace("tool: "+typ+" "+subject))
	case "error":
		if completed {
			_, _ = fmt.Fprintf(s.errOut, "guildhall: %s: Codex warning: %s\n", Subcommand, plainText(it.Message))
		}
	}
}

// show writes e, carrying the lines of text as its log, to the event file, and then text as one line to out.
func (s *stream) show(e workbench.Event, text string) {
	e.Log = strings.Split(text, "\n")
	s.events.Write(e)
	_, _ = fmt.Fprintln(s.out, text)
}

// failure is why the run has no answer to take, wrapping ErrNoAnswer, or nil when its last turn completed: a failed
// turn, the error events of a run with no completed turn, or a stream that ended without either.
func (s *stream) failure() error {
	switch {
	case s.completed:
		return nil
	case s.failed != "":
		return fmt.Errorf("%w: Codex turn failed: %s", ErrNoAnswer, s.failed)
	case len(s.errors) > 0:
		return fmt.Errorf("%w: Codex error: %s", ErrNoAnswer, strings.Join(s.errors, "; "))
	}
	return fmt.Errorf("%w: Codex ended without completing its turn", ErrNoAnswer)
}

// usage is the usage the completed turns report: input is input_tokens less cached_input_tokens, floored at 0,
// cache read is cached_input_tokens, cache creation is cache_write_input_tokens and output is output_tokens, all
// measured, with an unknown cost. A run with no usage block has unknown usage.
func (s *stream) usage() adapter.Usage {
	if !s.sawUsage {
		return adapter.UnknownUsage
	}
	u := adapter.UnknownUsage
	u.Tokens = adapter.Measured
	u.InputTokens = max(s.used.InputTokens-s.used.CachedInputTokens, 0)
	u.CacheReadInputTokens = max(s.used.CachedInputTokens, 0)
	u.CacheCreationInputTokens = max(s.used.CacheWriteInputTokens, 0)
	u.OutputTokens = max(s.used.OutputTokens, 0)
	if u.Validate() != nil {
		return adapter.UnknownUsage
	}
	return u
}

// cut shortens subject to 160 runes.
func cut(subject string) string {
	if runes := []rune(subject); len(runes) > 160 {
		return string(runes[:160]) + "..."
	}
	return subject
}

// lineCount is the number of lines in text: none when it is empty, and a last line without a newline counts.
func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1
}

// plainText keeps text readable without emitting terminal control sequences.
func plainText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
}
