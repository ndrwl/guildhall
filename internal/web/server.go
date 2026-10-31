package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// CSP is the Content-Security-Policy every page is served with: no script, no external request, styles only inline,
// and frames, images and forms only from the page's own origin.
const CSP = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self'; frame-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'self'"

// FrameCSP is the policy an HTML document is served with on its frame route, so it runs sandboxed with no script,
// no forms and an opaque origin.
const FrameCSP = "sandbox"

//go:embed page.tmpl style.css
var assets embed.FS

var pages = template.Must(template.New("page.tmpl").Funcs(funcs).ParseFS(assets, "page.tmpl"))

// style is the pages' one stylesheet, inlined in every page.
var style = func() template.CSS {
	b, err := fs.ReadFile(assets, "style.css")
	if err != nil {
		panic(err)
	}
	return template.CSS(b) //nolint:gosec // the embedded stylesheet, never input
}()

// Options configures a Server.
type Options struct {
	// Source reads each task's page model.
	Source Source
	// Documents is the copy's document directory, state/documents, that each Document's Path is relative to.
	Documents string
	// Location is the time zone times are shown in; nil means time.Local.
	Location *time.Location
	// Log receives one line for each request that failed for a reason the page does not show; nil discards it.
	Log func(string)
	// Address is the configured [web] address, a host and port, that page links are built from. When set, every
	// request whose Host is not it, ignoring case, is refused before anything is read, and an answer's Origin must
	// be this address's own, so a page reached under another name, such as a DNS-rebinding site's, sees nothing.
	Address string
	// Identity tells which Tailscale login sent each request, Guildmaster is the login that may answer, and
	// Answerer records what it gives. Without all three, and Address, every page is read-only and every POST
	// refused.
	Identity    Identifier
	Guildmaster string
	Answerer    Answerer
}

// Server serves the task pages and reader pages from its Source. Its routes are:
//
//	GET /<repo>/<task>                  the task page
//	GET /<repo>/<task>/doc/<name>       the reader page of one of the task's documents
//	GET /<repo>/<task>/doc/<name>/raw   the document as text/plain
//	GET /<repo>/<task>/doc/<name>/frame an HTML document, sandboxed, for the reader page's frame
//	POST /<repo>/<task>/answer          an answer to one of the task's decisions, or words only
//
// Each path segment is path-escaped. Anything else, including any other method, is 404. With Options.Address set,
// a request on any route whose Host is not that address is 421.
type Server struct {
	o Options
	// token is the per-process form token every answer must carry.
	token string
}

// New returns a Server with options o.
func New(o Options) *Server {
	if o.Location == nil {
		o.Location = time.Local
	}
	return &Server{o: o, token: newToken()}
}

// ServeHTTP routes one request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", CSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
	if !s.ourHost(r.Host) {
		s.refuse(w, http.StatusMisdirectedRequest, "Wrong address", "This server answers only at its configured address, "+s.o.Address+".")
		return
	}
	segs, ok := segments(r.URL.EscapedPath())
	if ok && r.Method == http.MethodPost && len(segs) == 3 && segs[2] == "answer" {
		s.answer(w, r, segs[0], segs[1])
		return
	}
	if !ok || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		s.refuse(w, http.StatusNotFound, "Not found", "There is no page here. A task page is /<repo>/<task>.")
		return
	}
	switch {
	case len(segs) == 2:
		s.taskPage(w, r, segs[0], segs[1])
	case len(segs) == 4 && segs[2] == "doc":
		s.document(w, r, segs[0], segs[1], segs[3], "")
	case len(segs) == 5 && segs[2] == "doc" && (segs[4] == "raw" || segs[4] == "frame"):
		s.document(w, r, segs[0], segs[1], segs[3], segs[4])
	default:
		s.refuse(w, http.StatusNotFound, "Not found", "There is no page here. A task page is /<repo>/<task>.")
	}
}

// ourHost reports whether host, a request's Host or an Origin's host, names this server: any host when no Address
// is configured, else the Address, ignoring case, or its host alone when its port is HTTP's default 80.
func (s *Server) ourHost(host string) bool {
	if s.o.Address == "" {
		return true
	}
	if strings.EqualFold(host, s.o.Address) {
		return true
	}
	bare, port, err := net.SplitHostPort(s.o.Address)
	return err == nil && port == "80" && strings.EqualFold(host, bare)
}

// segments splits an escaped path into its unescaped segments; an empty segment, a trailing slash or a segment
// that does not unescape is refused.
func segments(escaped string) ([]string, bool) {
	rest, ok := strings.CutPrefix(escaped, "/")
	if !ok || rest == "" {
		return nil, false
	}
	parts := strings.Split(rest, "/")
	for i, p := range parts {
		u, err := url.PathUnescape(p)
		if err != nil || u == "" || u == "." || u == ".." {
			return nil, false
		}
		parts[i] = u
	}
	return parts, true
}

// read reads the task from the Source, writing the refusal page for an error, and reports whether it did.
func (s *Server) read(w http.ResponseWriter, repo, id string) (Task, bool) {
	t, err := s.o.Source.Task(repo, id)
	switch {
	case errors.Is(err, ErrNotFound):
		s.refuse(w, http.StatusNotFound, "No such task", "No task "+repo+"/"+id+" in this copy.")
		return Task{}, false
	case errors.Is(err, ErrBusy):
		s.refuse(w, http.StatusServiceUnavailable, "The store is busy", "The Director is writing to the store. Reload in a moment.")
		return Task{}, false
	case err != nil:
		s.log("web: read " + repo + "/" + id + ": " + err.Error())
		s.refuse(w, http.StatusInternalServerError, "The task could not be read", "Guildhall could not read this task from the store. Reload, or ask the Director.")
		return Task{}, false
	}
	return t, true
}

// taskPage serves the task page to whoever r is from: with the forms for the Guildmaster's login, read-only for
// anyone else. ?sent=<message id>&ask=<decision> after words only shows that ask's words-only-sent state.
func (s *Server) taskPage(w http.ResponseWriter, r *http.Request, repo, id string) {
	t, ok := s.read(w, repo, id)
	if !ok {
		return
	}
	st := pageState{identity: s.who(r), Token: s.token}
	if sent := r.URL.Query().Get("sent"); strings.HasPrefix(sent, NoteIDPrefix) {
		st.Sent, st.Decision = sent, r.URL.Query().Get("ask")
	}
	s.render(w, http.StatusOK, "task", s.taskView(t, st))
}

// document serves the reader page, or with part "raw" or "frame" the document itself, of the task's document
// name: only a name recorded on this task resolves, its file is read from its recorded path under the document
// directory, never from the URL, and served only when its sha256 matches the record.
func (s *Server) document(w http.ResponseWriter, r *http.Request, repo, id, name, part string) {
	t, ok := s.read(w, repo, id)
	if !ok {
		return
	}
	var doc *Document
	for i := range t.Documents {
		d := &t.Documents[i]
		if d.Repository == repo && d.TaskID == id && d.Name == name {
			doc = d
		}
	}
	if doc == nil {
		s.refuse(w, http.StatusNotFound, "No such document", "Task "+repo+"/"+id+" has no document "+name+".")
		return
	}
	body, status, why := s.verified(*doc)
	if status != http.StatusOK {
		s.refuse(w, status, "Document refused", why)
		return
	}
	html := isHTML(doc.Path)
	switch part {
	case "raw":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	case "frame":
		if !html {
			s.refuse(w, http.StatusNotFound, "Not found", "Only an HTML document has a frame; "+name+" is not one.")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", FrameCSP)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	v := readerView{taskView: s.taskView(t, pageState{identity: s.who(r), Token: s.token}), Doc: s.docView(t, *doc)}
	switch {
	case html:
		v.Frame = true
	case isMarkdown(doc.Path):
		v.HTML = RenderMarkdown(body)
	default:
		v.Text = string(body)
	}
	s.render(w, http.StatusOK, "reader", v)
}

// verified reads doc's file under the document directory and checks it against its recorded sha256, returning its
// bytes and 200, or a status and a reason that names the document and never a filesystem path: 404 for a file that
// is missing, 409 for one that no longer matches its record.
func (s *Server) verified(doc Document) ([]byte, int, string) {
	clean := path.Clean(doc.Path)
	if doc.Path == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(doc.Path, "\\") {
		return nil, http.StatusConflict, "The record of " + doc.Name + " names no file inside the document directory."
	}
	body, err := os.ReadFile(filepath.Join(s.o.Documents, filepath.FromSlash(clean)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, http.StatusNotFound, "The file of " + doc.Name + " is missing from the copy's documents."
	}
	if err != nil {
		s.log("web: read document " + doc.Repository + "/" + doc.TaskID + "/" + doc.Name + ": " + err.Error())
		return nil, http.StatusInternalServerError, "The file of " + doc.Name + " could not be read."
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != strings.ToLower(doc.SHA256) {
		return nil, http.StatusConflict, "The file of " + doc.Name + " does not match the sha256 recorded for it, so it is not shown."
	}
	return body, http.StatusOK, ""
}

func isHTML(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == ".html" || ext == ".htm"
}

func isMarkdown(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == ".md" || ext == ".markdown"
}

// refusal is the refusal page's model.
type refusal struct {
	Style        template.CSS
	Title, Words string
}

func (s *Server) refuse(w http.ResponseWriter, status int, title, words string) {
	s.render(w, status, "refusal", refusal{Style: style, Title: title, Words: words})
}

// render executes the named page into a buffer and writes it with status, so a page that fails to render is a
// plain 500 rather than half a page.
func (s *Server) render(w http.ResponseWriter, status int, name string, v any) {
	var b bytes.Buffer
	if err := pages.ExecuteTemplate(&b, name, v); err != nil {
		s.log("web: render " + name + ": " + err.Error())
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("The page could not be rendered.\n"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}

func (s *Server) log(line string) {
	if s.o.Log != nil {
		s.o.Log(line)
	}
}

// pathEscape escapes one path segment.
func pathEscape(s string) string { return url.PathEscape(s) }
