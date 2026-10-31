package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
)

// Answer is one answer, or words only, given on a task page: the decision it is about, the subject digest the page
// showed ("" for none), the option chosen ("" for words only), the words as the form sent them, and the Tailscale
// login that sent it.
type Answer struct {
	Repository, TaskID, Decision string
	Subject                      string
	Option                       string
	Text                         string
	Login                        string
}

// Answerer records what the Guildmaster gives on a task page. It never advances a task.
//
// Answer records a.Option, with a.Text after a blank line when there is any, as the decision's answer, attributed to
// the page and a.Login, then tells the Director in a decision-answered message and wakes it; an empty a.Option
// answers a decision with no options by a.Text alone. It refuses, writing nothing, unless the decision is still open
// and bound to a.Subject and a.Option is one of its options; a retry of an answer already recorded from the page
// replays it and returns nil.
//
// Send records a.Text, which must not be blank, as a note to the Director about the task and decision, answering
// nothing, shows it to the Guildmaster as messages send shows a note, wakes the Director, and returns the
// message's id, NoteID's.
//
// A refusal is a *Refusal, with the reason the page shows; a store held by a writer is ErrBusy; anything else is
// shown as a failure with no detail.
type Answerer interface {
	Answer(a Answer) error
	Send(a Answer) (string, error)
}

// Refusal is an Answerer's refusal: nothing was written, Status is the HTTP status the page is shown again with and
// Reason what it says on the ask.
type Refusal struct {
	Status int
	Reason string
}

func (r *Refusal) Error() string { return r.Reason }

// Refuse returns a *Refusal with status and reason.
func Refuse(status int, reason string) error { return &Refusal{Status: status, Reason: reason} }

// NoteIDPrefix begins the id of every words-only note the page sends.
const NoteIDPrefix = "page-note."

// NoteID is the message id of words sent on the page about one decision: NoteIDPrefix and 16 hex of a sha256 over
// the task, the decision and the words, so a double submit sends one note.
func NoteID(repo, task, decision, text string) string {
	sum := sha256.Sum256([]byte(repo + "\x00" + task + "\x00" + decision + "\x00" + text))
	return NoteIDPrefix + hex.EncodeToString(sum[:8])
}

// AnsweredID is the message id of the decision-answered message for one decision, sent once since a decision is
// answered once.
func AnsweredID(repo, task, decision string) string {
	sum := sha256.Sum256([]byte(repo + "\x00" + task + "\x00" + decision))
	return "page-answered." + hex.EncodeToString(sum[:8])
}

// maxFormBytes bounds an answer's request body: the largest answer, 64 KiB, with every line ending CRLF and
// percent-encoded, comfortably fits.
const maxFormBytes = 1 << 20

// identity is who a request is from and whether that login may answer.
type identity struct {
	Login  string
	Answer bool
}

// answering reports whether the server answers at all: it has an Answerer, an Identifier, a Guildmaster login and
// the configured Address that every request's Host and an answer's Origin are checked against.
func (s *Server) answering() bool {
	return s.o.Answerer != nil && s.o.Identity != nil && s.o.Guildmaster != "" && s.o.Address != ""
}

// who tells which login sent r, and whether it may answer: only the configured Guildmaster's login, told by the
// Identifier, may. With answering off, or no identity, the page is read-only.
func (s *Server) who(r *http.Request) identity {
	if !s.answering() {
		return identity{}
	}
	login, err := s.o.Identity.Login(r.Context(), r.RemoteAddr)
	if err != nil {
		if !errors.Is(err, ErrNoIdentity) {
			s.log("web: identity of " + r.RemoteAddr + ": " + err.Error())
		}
		return identity{}
	}
	return identity{Login: login, Answer: login == s.o.Guildmaster}
}

// newToken is a per-process random token for the page's forms.
func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("web: no randomness for the form token: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// sameOrigin reports whether r comes from a page of this server: its Origin is the configured Address's own, never
// one read from the request's Host, or, when the browser sent no Origin or the opaque "null", its Sec-Fetch-Site is
// same-origin, which ServeHTTP's Host check ties to the Address. A request with neither is refused.
func (s *Server) sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.Path == "" &&
			s.o.Address != "" && s.ourHost(u.Host)
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// pageState is what one request adds to a page: who is looking, the form token, and an ask's refusal or sent
// words.
type pageState struct {
	identity
	Token string
	// Decision is the ask the refusal or the sent words are about.
	Decision string
	Refused  string
	Words    string
	Sent     string
}

// answer handles POST /<repo>/<task>/answer: it checks identity, origin and token, then gives the Answerer the
// answer or the words, and redirects to the ask; a refusal shows the page again with its reason on the ask and the
// words kept.
func (s *Server) answer(w http.ResponseWriter, r *http.Request, repo, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.refuse(w, http.StatusRequestEntityTooLarge, "Not recorded", "The answer could not be read, so nothing was written. Go back and try a shorter answer.")
		return
	}
	a := Answer{Repository: repo, TaskID: id, Decision: r.PostForm.Get("decision"), Subject: r.PostForm.Get("subject"),
		Option: r.PostForm.Get("option"), Text: r.PostForm.Get("text")}
	send := r.PostForm.Get("send")
	words := send == "words"
	if send == "answer" {
		// An ask with no options is answered by the words alone.
		a.Option = ""
	}
	who := s.who(r)
	st := pageState{identity: who, Token: s.token, Decision: a.Decision, Words: a.Text}
	again := func(status int, reason string) {
		st.Refused = reason
		t, ok := s.read(w, repo, id)
		if !ok {
			return
		}
		s.render(w, status, "task", s.taskView(t, st))
	}
	switch {
	case !s.answering():
		again(http.StatusForbidden, "this page is read-only: the copy's [web] table names no guildmaster login")
		return
	case !who.Answer:
		again(http.StatusForbidden, "only the Guildmaster's Tailscale login can answer")
		return
	case !s.sameOrigin(r):
		again(http.StatusForbidden, "the answer did not come from this page")
		return
	case subtle.ConstantTimeCompare([]byte(r.PostForm.Get("token")), []byte(s.token)) != 1:
		again(http.StatusForbidden, "the page is out of date; reload it and answer again")
		return
	case a.Decision == "":
		again(http.StatusBadRequest, "the answer names no decision")
		return
	case !words && send != "answer" && a.Option == "":
		again(http.StatusBadRequest, "choose an answer, or send your words only")
		return
	}
	a.Login = who.Login
	var err error
	var sent string
	if words {
		sent, err = s.o.Answerer.Send(a)
	} else {
		err = s.o.Answerer.Answer(a)
	}
	if ref, ok := errors.AsType[*Refusal](err); ok {
		again(ref.Status, ref.Reason)
		return
	}
	if errors.Is(err, ErrBusy) {
		again(http.StatusServiceUnavailable, "the store is busy; try again in a moment")
		return
	}
	if err != nil {
		s.log("web: answer " + repo + "/" + id + " #" + a.Decision + ": " + err.Error())
		again(http.StatusInternalServerError, "Guildhall could not record it; ask the Director")
		return
	}
	target := taskHref(repo, id)
	if words {
		target += "?" + url.Values{"sent": {sent}, "ask": {a.Decision}}.Encode()
	}
	http.Redirect(w, r, target+"#"+url.PathEscape(a.Decision), http.StatusSeeOther)
}
