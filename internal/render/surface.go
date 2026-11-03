package render

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
)

// Surface is what a design round changes that the Guildmaster will see, named by the Director in the round's brief
// (docs/tasks.md#design-rounds); it decides the mock-up the designer writes beside design.md.
type Surface string

const (
	// SurfaceWebPage is a web page: the mock-up is MockupHTML, the real task page in a desktop and a phone frame.
	SurfaceWebPage Surface = "web-page"
	// SurfaceCLI is command-line output: the mock-up is MockupMarkdown, the exact sample output in fenced blocks.
	SurfaceCLI Surface = "cli"
	// SurfaceChat is a chat reply: the mock-up is MockupMarkdown, the exact sample reply in fenced blocks.
	SurfaceChat Surface = "chat"
	// SurfaceNone is no visible surface: the designer writes design.md alone.
	SurfaceNone Surface = "none"
)

// Surfaces lists every surface, in the order usage text names them.
var Surfaces = []Surface{SurfaceWebPage, SurfaceCLI, SurfaceChat, SurfaceNone}

// The mock-up files a designer writes as the second output of its design port, by surface.
const (
	MockupHTML     = "mockup.html"
	MockupMarkdown = "mockup.md"
)

// BriefPort is the input port a designer attempt is given its round's brief on.
const BriefPort = "brief"

// surfacePrefix starts a brief's first line, which names its surface.
const surfacePrefix = "Surface:"

// ParseSurface returns the surface s names, refusing any name that is not one of Surfaces.
func ParseSurface(s string) (Surface, error) {
	if v := Surface(s); slices.Contains(Surfaces, v) {
		return v, nil
	}
	return "", fmt.Errorf("unknown surface %q: want one of %s", s, surfaceNames())
}

func surfaceNames() string {
	names := make([]string, len(Surfaces))
	for i, s := range Surfaces {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

// ComposeBrief is the brief `task design` records for surface s and the Director's text: the first line
// "Surface: <s>", a blank line, then the text.
func ComposeBrief(s Surface, text string) string {
	return surfacePrefix + " " + string(s) + "\n\n" + text
}

// BriefSurface reads the surface from a recorded brief's first line, "Surface: <surface>". It is the one parser the
// `task design` request, designer intake and the designer prompt share. found is false for a brief whose first line
// is no Surface: line, such as one recorded before surfaces were named, which reads as SurfaceNone; err is set for a
// Surface: line naming no known surface.
func BriefSurface(brief []byte) (s Surface, found bool, err error) {
	line, _, _ := bytes.Cut(brief, []byte("\n"))
	rest, ok := strings.CutPrefix(strings.TrimRight(string(line), "\r"), surfacePrefix)
	if !ok {
		return SurfaceNone, false, nil
	}
	s, err = ParseSurface(strings.TrimSpace(rest))
	if err != nil {
		return "", true, fmt.Errorf("brief's Surface: line: %w", err)
	}
	return s, true, nil
}

// designKind is the step kind of a designer attempt, whose prompt names its brief's surface.
const designKind = "design"

// designSurface is the surface of a designer attempt's brief, the input on BriefPort, read by BriefSurface; it is ""
// for any other kind and SurfaceNone for a designer given no brief.
func designSurface(kind string, inputs []InputFile, data [][]byte) (Surface, error) {
	if kind != designKind {
		return "", nil
	}
	for i, in := range inputs {
		if in.Port == BriefPort {
			s, _, err := BriefSurface(data[i])
			if err != nil {
				return "", fmt.Errorf("render: %s: %w", in.Path, err)
			}
			return s, nil
		}
	}
	return SurfaceNone, nil
}

// MockupFile is the mock-up file a designer writes for surface s, "" for SurfaceNone.
func MockupFile(s Surface) string {
	switch s {
	case SurfaceWebPage:
		return MockupHTML
	case SurfaceCLI, SurfaceChat:
		return MockupMarkdown
	}
	return ""
}
