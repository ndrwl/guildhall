package interp

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/spec"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// mockupQuote is an attribute's opening quote in a web mock-up, plain or escaped, since the page sits escaped in a
// frame's srcdoc attribute.
const mockupQuote = `(?:["']|&quot;|&#34;|&#x22;|&#39;|&#x27;|&apos;)?`

// mockupURL finds an external or protocol-relative URL where a web mock-up would load it: in a src or href
// attribute, a CSS url( or an @import, matched case-insensitively.
var mockupURL = regexp.MustCompile(`(?i)(?:\b(?:src|href)\s*=\s*` + mockupQuote + `|\burl\(\s*` + mockupQuote + `|@import\s+(?:url\(\s*)?` + mockupQuote + `)\s*(?:https?:|//)`)

// mockupEvent finds an on…= event attribute in a web mock-up, such as onclick= or onload=.
var mockupEvent = regexp.MustCompile(`(?i)(?:^|[\s"'/;])on[a-z]+\s*=`)

// mockupFence finds a fenced code block's opening line in a Markdown mock-up.
var mockupFence = regexp.MustCompile("(?m)^ {0,3}(?:```|~~~)")

// briefSurface is the surface of the brief designer attempt n of step was bound to, read by render.BriefSurface:
// SurfaceNone for a brief recorded before surfaces were named.
func (r *run) briefSurface(step string, n int) (render.Surface, error) {
	b, err := r.binding(step, n)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(b.Inputs, func(in boundInput) bool { return in.Port == BriefPort })
	if i < 0 {
		return "", fmt.Errorf("designer attempt %d of %s was bound to no brief", n, step)
	}
	data, err := r.read(store.DocumentRef{Path: b.Inputs[i].Path, SHA256: b.Inputs[i].SHA256})
	if err != nil {
		return "", err
	}
	s, _, err := render.BriefSurface(data)
	return s, err
}

// checkMockup refuses, as the result's fault, the outputs of designer attempt n of step s, with their contents,
// unless they are the file set its brief's surface asks for (docs/interpreter.md#design-rounds): design.md alone for
// none; for web-page a second output mockup.html, valid UTF-8 with no script, event attribute or external URL; for
// cli and chat a second output mockup.md holding a fenced code block. The web check is plain text matching that
// catches a mock-up which would render broken; the frame route's CSP is what guards the Guildmaster.
func (r *run) checkMockup(s workflow.Step, n int, outputs []adapter.Output, contents [][]byte) error {
	surface, err := r.briefSurface(s.ID, n)
	if err != nil {
		return err
	}
	return checkMockupFiles(surface, outputs, contents)
}

// checkDesignQuestions refuses, as the result's fault, a design whose `## Open questions` section is missing or is
// neither "None" nor well-formed questions in the spec's shape (spec.SpecQuestions): the design ask copies each
// question in full, so the Guildmaster can answer from the ask alone.
func checkDesignQuestions(data []byte) error {
	if _, err := spec.SpecQuestions(data); err != nil {
		return rejectOutput(fmt.Errorf("%w; the design ask copies each open question in full, so give design.md an %s section that says \"None\" or holds **Qn. title** questions, each with Options: and Recommended: lines",
			err, spec.OpenQuestionsHeading))
	}
	return nil
}

// checkMockupFiles is checkMockup's rule for a brief whose surface is surface.
func checkMockupFiles(surface render.Surface, outputs []adapter.Output, contents [][]byte) error {
	want := render.MockupFile(surface)
	if want == "" {
		if len(outputs) != 1 {
			return rejectOutput(fmt.Errorf("the brief's surface is %s, so a design lists design.md alone; got %s", surface, outputList(outputs)))
		}
		return nil
	}
	if len(outputs) != 2 || path.Base(outputs[1].Path) != want {
		return rejectOutput(fmt.Errorf("the brief's surface is %s, so a design lists two outputs, design.md and then %s; got %s", surface, want, outputList(outputs)))
	}
	data := contents[1]
	if want == render.MockupMarkdown {
		if !mockupFence.Match(data) {
			return rejectOutput(fmt.Errorf("%s holds no fenced code block with the sample output", want))
		}
		return nil
	}
	if !utf8.Valid(data) {
		return rejectOutput(fmt.Errorf("%s is not valid UTF-8", want))
	}
	lower := strings.ToLower(string(data))
	switch {
	case strings.Contains(lower, "<script") || strings.Contains(lower, "&lt;script"):
		return rejectOutput(fmt.Errorf("%s holds a script element; a mock-up uses no script", want))
	case mockupEvent.MatchString(lower):
		return rejectOutput(fmt.Errorf("%s holds an event attribute (%s); a mock-up uses no script", want, strings.TrimSpace(mockupEvent.FindString(lower))))
	case mockupURL.MatchString(lower):
		return rejectOutput(fmt.Errorf("%s loads an external URL (%s); a mock-up loads nothing from the network, only inline styles and data: images", want, mockupURL.FindString(lower)))
	}
	return nil
}

// outputList is outputs' paths for a refusal, comma-separated, or "none".
func outputList(outputs []adapter.Output) string {
	if len(outputs) == 0 {
		return "none"
	}
	paths := make([]string, len(outputs))
	for i, o := range outputs {
		paths[i] = o.Path
	}
	return strings.Join(paths, ", ")
}
