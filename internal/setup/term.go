package setup

// The terminal prompts are the Go form of the read and masked read helpers of the wizard template Firstmate uses,
// bin/wizard-template.sh and bin/wizard-template.ps1 in ndrwl/dev-setup at 8a095ccfeff5563f776f743d088b44c5afc57747
// (MIT, see NOTICE).

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// TermPrompter is the Prompter of a person at a terminal: cards and questions on Out, answers read a line at a
// time from In. A secret is read with echo turned off when In is a terminal, and Out shows only that it was read.
type TermPrompter struct {
	In  io.Reader
	Out io.Writer
	r   *bufio.Reader
}

// Show prints text on its own lines.
func (t *TermPrompter) Show(text string) error {
	_, err := fmt.Fprintln(t.Out, text)
	return err
}

// Confirm asks a yes-or-no question; only y or yes is yes.
func (t *TermPrompter) Confirm(q string) (bool, error) {
	if _, err := fmt.Fprintf(t.Out, "%s [y/N] ", q); err != nil {
		return false, err
	}
	line, err := t.line()
	if err != nil {
		return false, err
	}
	a := strings.ToLower(line)
	return a == "y" || a == "yes", nil
}

// Ask asks for a recorded answer.
func (t *TermPrompter) Ask(q string) (string, error) {
	if _, err := fmt.Fprintf(t.Out, "%s: ", q); err != nil {
		return "", err
	}
	return t.line()
}

// AskSecret asks for an answer that is neither echoed nor recorded.
func (t *TermPrompter) AskSecret(q string) (string, error) {
	if _, err := fmt.Fprintf(t.Out, "%s (hidden): ", q); err != nil {
		return "", err
	}
	if f, ok := t.In.(*os.File); ok {
		if restore, err := noEcho(f); err == nil {
			defer restore()
		}
	}
	v, err := t.line()
	if err == nil {
		_, err = fmt.Fprintln(t.Out, "(read)")
	}
	return v, err
}

func (t *TermPrompter) line() (string, error) {
	if t.r == nil {
		t.r = bufio.NewReader(t.In)
	}
	s, err := t.r.ReadString('\n')
	if errors.Is(err, io.EOF) && s != "" {
		err = nil
	}
	if err != nil {
		return "", fmt.Errorf("reading an answer: %w", err)
	}
	return strings.TrimRight(s, "\r\n"), nil
}
