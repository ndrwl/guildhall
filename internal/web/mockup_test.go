package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// askOf is the HTML of the ask id on page, up to its answer tiles.
func askOf(t *testing.T, page, id string) string {
	t.Helper()
	i := strings.Index(page, `id="`+id+`"`)
	if i < 0 {
		t.Fatalf("the page has no ask %s", id)
	}
	ask, _, _ := strings.Cut(page[i:], `class="opts"`)
	return ask
}

// TestDesignMockup: a design ask's HTML mock-up is one "Open mock-up" link to its frame route, never framed or
// inlined in the task page, and the frame serves it under the no-network policy; a Markdown mock-up is rendered
// inline, script-free, and not linked; a design ask with no mock-up shows neither.
func TestDesignMockup(t *testing.T) {
	s, _, _, _ := fixtureServer(t)
	page := get(t, s, "/guildhall/d122").Body.String()

	html := askOf(t, page, "design-4")
	once(t, html, `<a href="/guildhall/d122/doc/designer-4-design-2/frame" target="_blank" rel="noopener">Open mock-up</a>`)
	for _, bad := range []string{"<iframe", "srcdoc", "Mock-up</p>"} {
		if strings.Contains(html, bad) {
			t.Errorf("the HTML mock-up's ask holds %q; it must only link the frame", bad)
		}
	}
	// This pins the frame's header and that srcdoc survives; whether the srcdoc frames render under FrameCSP is a
	// browser matter, covered by the manual Chrome check recorded in the PR, not by any test here. That check
	// (d147 build-3) served a two-frame mock-up through this route: both frames rendered, the 390px frame took
	// the max-width:760px layout, and Chrome made no request beyond the frame document itself.
	frame := get(t, s, "/guildhall/d122/doc/designer-4-design-2/frame")
	if frame.Code != http.StatusOK || frame.Header().Get("Content-Security-Policy") != FrameCSP || !strings.Contains(frame.Body.String(), "srcdoc=") {
		t.Errorf("the mock-up's frame: %d, CSP %q:\n%s", frame.Code, frame.Header().Get("Content-Security-Policy"), frame.Body)
	}

	md := askOf(t, page, "design-5")
	once(t, md, `<p class="ph">Mock-up</p>`)
	once(t, md, `<pre><code>$ guildhall task design d122 --surface cli`)
	if strings.Contains(md, "Open mock-up") || strings.Contains(md, "designer-5-design-2") {
		t.Error("a Markdown mock-up is linked as well as shown")
	}

	none := askOf(t, page, "accept--s2-1")
	if strings.Contains(none, "mock-up") || strings.Contains(none, "Mock-up") {
		t.Error("an ask with no mock-up shows one")
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestDesignMockupRefused: a Markdown mock-up whose file does not match its digest is not shown; the ask links its
// reader page instead, which says why. A hostile Markdown mock-up loses its script and javascript: link.
func TestDesignMockupRefused(t *testing.T) {
	const p = "tasks/guildhall/d122/designer-5/mockup.md"
	t.Run("mismatch", func(t *testing.T) {
		s, _, dir, _ := fixtureServer(t)
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), []byte("```\nedited\n```\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ask := askOf(t, get(t, s, "/guildhall/d122").Body.String(), "design-5")
		once(t, ask, `<a href="/guildhall/d122/doc/designer-5-design-2" target="_blank" rel="noopener">read designer-5-design-2</a>`)
		if strings.Contains(ask, "edited") || strings.Contains(ask, "Mock-up</p>") {
			t.Errorf("a mock-up that does not verify is shown:\n%s", ask)
		}
	})
	t.Run("hostile", func(t *testing.T) {
		dir, sums := writeFixtureFiles(t)
		hostile := "<script>alert(1)</script>\n\n[x](javascript:alert(1))\n\n```\nok\n```\n"
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), []byte(hostile), 0o644); err != nil {
			t.Fatal(err)
		}
		sums[p] = sha256Hex(hostile)
		s := New(Options{Source: &fakeSource{tasks: map[string]Task{"guildhall/d122": fixtureTask(sums)}}, Documents: dir, Location: time.UTC, Host: testHost})
		ask := askOf(t, get(t, s, "/guildhall/d122").Body.String(), "design-5")
		for _, bad := range []string{"<script", "javascript:"} {
			if strings.Contains(ask, bad) {
				t.Errorf("a hostile mock-up's ask holds %q", bad)
			}
		}
		once(t, ask, "<pre><code>ok\n</code></pre>")
	})
}
