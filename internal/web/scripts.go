package web

import (
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"io/fs"
	"path"
	"strings"
)

// baseCSP is the policy every page carried before own scripts: no script, no external request, styles only
// inline, and frames, images and forms only from the page's own origin.
const baseCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self'; frame-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'self'"

// ownScript is one of Guildhall's own scripts: a hand-written file under scripts/, embedded in the binary, inlined
// in the task list, task and reader pages, and listed by Hash in the CSP's script-src. Body and Hash come from the
// same bytes, so the inlined text and its hash cannot drift apart.
type ownScript struct {
	Name string
	Body template.JS
	Hash string // 'sha256-<base64>', a CSP source expression
}

// ownScripts are the embedded scripts, read once when the package loads, in file-name order.
var ownScripts = loadScripts(assets)

// CSP is the Content-Security-Policy every page is served with: baseCSP, plus a script-src listing only the own
// scripts' hashes, so no other script runs, including any inside Artisan-written content, and connect-src 'self',
// so the own scripts allowed a request, poll.js and list.js, can ask their page's since route or /list.json and
// nothing else can be reached. Its script-src never lists 'unsafe-inline', 'unsafe-eval', 'unsafe-hashes', 'strict-dynamic', 'self' or
// a host, and with no own scripts it is baseCSP exactly.
var CSP = policy(ownScripts)

func loadScripts(fsys fs.FS) []ownScript {
	names, err := fs.Glob(fsys, "scripts/*.js")
	if err != nil {
		panic(err)
	}
	out := make([]ownScript, 0, len(names))
	for _, name := range names {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			panic(err)
		}
		if strings.Contains(strings.ToLower(string(b)), "</script") {
			panic("web: own script " + name + " holds </script")
		}
		sum := sha256.Sum256(b)
		out = append(out, ownScript{
			Name: path.Base(name),
			Body: template.JS(b), //nolint:gosec // an embedded own script, never input
			Hash: "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'",
		})
	}
	return out
}

// connectSrc is the one request source own scripts may use: the page's own origin, which serves poll.js's since
// route and list.js's /list.json (docs/decisions/own-scripts.md, docs/decisions/list-page-scripts.md).
const connectSrc = "connect-src 'self'"

// policy is baseCSP with a script-src of scripts' hashes and connectSrc, or baseCSP itself when there are none.
func policy(scripts []ownScript) string {
	if len(scripts) == 0 {
		return baseCSP
	}
	hashes := make([]string, len(scripts))
	for i, s := range scripts {
		hashes[i] = s.Hash
	}
	return baseCSP + "; script-src " + strings.Join(hashes, " ") + "; " + connectSrc
}

// listOnly are the own scripts only the task list inlines, and pageOnly those only the task and reader pages
// inline; every other own script is inlined by all three. The one CSP lists every own script's hash, so each page's
// set is a subset of what it allows.
var (
	listOnly = map[string]bool{"list.js": true}
	pageOnly = map[string]bool{"poll.js": true}
)

// scriptSet is the bodies of the own scripts skip does not name, in file-name order.
func scriptSet(skip map[string]bool) []template.JS {
	var out []template.JS
	for _, s := range ownScripts {
		if !skip[s.Name] {
			out = append(out, s.Body)
		}
	}
	return out
}

// scripts are the own scripts the task and reader pages inline, and listScripts those the task list inlines.
var (
	scripts     = scriptSet(listOnly)
	listScripts = scriptSet(pageOnly)
)
