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
// scripts' hashes, so no other script runs, including any inside Artisan-written content. It never lists
// 'unsafe-inline', 'unsafe-eval', 'unsafe-hashes', 'strict-dynamic', 'self' or a host, and with no own scripts it
// is baseCSP exactly.
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

// policy is baseCSP with a script-src of scripts' hashes, or baseCSP itself when there are none.
func policy(scripts []ownScript) string {
	if len(scripts) == 0 {
		return baseCSP
	}
	hashes := make([]string, len(scripts))
	for i, s := range scripts {
		hashes[i] = s.Hash
	}
	return baseCSP + "; script-src " + strings.Join(hashes, " ")
}

// scripts are the own scripts' bodies, which the task list, task and reader pages inline.
var scripts = func() []template.JS {
	out := make([]template.JS, len(ownScripts))
	for i, s := range ownScripts {
		out[i] = s.Body
	}
	return out
}()
