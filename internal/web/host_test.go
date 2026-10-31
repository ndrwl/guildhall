package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWrongHost: any request whose Host is not Options.Host is refused with 421 and the CSP before the Source is
// read, on every route and method; the right host and port, in any letter case, is served as before.
func TestWrongHost(t *testing.T) {
	s, src, _, _ := fixtureServer(t)
	s.o.Host = "host.tail.ts.net:4388"
	do := func(method, target, host string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, target, nil)
		r.Host = host
		s.ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, target := range []string{"/guildhall/d122", "/guildhall/d122/doc/plan-1-spec/raw"} {
			for _, host := range []string{"evil.example", "evil.example:4388", "host.tail.ts.net:4389", "host.tail.ts.net", "host.tail.ts.net.:4388", ""} {
				src.calls = 0
				w := do(method, target, host)
				if w.Code != http.StatusMisdirectedRequest || w.Header().Get("Content-Security-Policy") != CSP || src.calls != 0 {
					t.Errorf("%s %s Host %q: status %d, CSP %q, %d Source calls; want 421, the CSP and none",
						method, target, host, w.Code, w.Header().Get("Content-Security-Policy"), src.calls)
				}
			}
		}
		for _, host := range []string{"host.tail.ts.net:4388", "HOST.Tail.ts.net:4388"} {
			if w := do(method, "/guildhall/d122", host); w.Code != http.StatusOK {
				t.Errorf("%s Host %q: status %d, want 200", method, host, w.Code)
			}
		}
	}
}

// TestNoHostFailsClosed: a server with no Options.Host refuses every request.
func TestNoHostFailsClosed(t *testing.T) {
	src := &fakeSource{tasks: map[string]Task{"r/t1": {Repository: "r", ID: "t1", Title: "A task", State: "open", Stands: "not started"}}}
	w := get(t, New(Options{Source: src}), "/r/t1")
	if w.Code != http.StatusMisdirectedRequest || src.calls != 0 {
		t.Errorf("status %d with %d Source calls; want 421 and none", w.Code, src.calls)
	}
}
