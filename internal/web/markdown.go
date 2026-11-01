package web

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// markdown is the one goldmark the pages render documents with: its tables extension at goldmark's safe defaults,
// so raw HTML is omitted (no html.WithUnsafe) and a dangerous URL is blanked, the <a> or <img> kept with an empty
// href or src. The dangerous URLs are javascript:, vbscript:, file: and every data: URL except data:image/png, gif,
// jpeg or webp followed by ;base64, (case-insensitively); those four base64 images are kept, and the page's CSP
// (img-src 'self') stops the browser loading them (docs/decisions/goldmark-for-documents.md).
var markdown = goldmark.New(goldmark.WithExtensions(extension.Table))

// RenderMarkdown renders a Markdown document to HTML with goldmark at its safe defaults. It is the only HTML the
// pages pass to html/template unescaped; a document goldmark cannot render is shown escaped as preformatted text.
func RenderMarkdown(src []byte) template.HTML {
	var b bytes.Buffer
	if err := markdown.Convert(src, &b); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(string(src)) + "</pre>")
	}
	return template.HTML(b.String())
}

// grillDecisions is the "## Decisions" section of a grill notes document, up to the next heading of level one or
// two, and how many top-level list items it holds; ok is false when the document has no such section.
func grillDecisions(src []byte) (section string, items int, ok bool) {
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l), "## Decisions") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", 0, false
	}
	end := len(lines)
	fence := false
	for i := start; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			fence = !fence
		}
		if fence {
			continue
		}
		if strings.HasPrefix(l, "# ") || strings.HasPrefix(l, "## ") {
			end = i
			break
		}
		if strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* ") || strings.HasPrefix(l, "+ ") {
			items++
		}
	}
	return strings.Join(lines[start:end], "\n"), items, true
}
