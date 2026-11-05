package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Answer is one answer, or words only, given on a task page: the decision it is about, the subject digest the page
// showed ("" for none), the option chosen ("" for words only), the words as the form sent them, and the Tailscale
// login that sent it. Confirm, when not 0, is the number of the Director's recommendation the Guildmaster confirmed,
// whose option is the answer. Tag is the attempt words said in the Say box are about, which have no Decision.
type Answer struct {
	Repository, TaskID, Decision string
	Subject                      string
	Option                       string
	Text                         string
	Login                        string
	Confirm                      int
	Tag                          string
	// Plan, on a plan approval whose spec has open questions on the page, are those questions, and Boxes the words
	// of each question's box the form sent, by number: the answer saves the boxes as drafts and writes the answered
	// questions into the one answer (PlanAnswersText), the others taking the recommendation.
	Plan  []PlanQuestion
	Boxes map[int]string
	// Picks are the changed picks of Plan's questions the form sent (planPicks): each is saved as its question's
	// answer, written once in the list, not as a "Qn (x)" line.
	Picks map[int]string
}

// Answerer records what the Guildmaster gives on a task page. It never advances a task.
//
// Answer records a.Option, with a.Text after a blank line when there is any, as the decision's answer, attributed to
// the page and a.Login, then tells the Director in a decision-answered message and wakes it; an empty a.Option
// answers a decision with no options by a.Text alone. It refuses, writing nothing, unless the decision is still open
// and bound to a.Subject and a.Option is one of its options; a retry of an answer already recorded from the page
// replays it and returns nil. With a.Confirm it answers with the option of that recommendation, refusing unless it
// is the Director's latest recommendation on the decision, and records on the task's conversation that the
// Guildmaster confirmed it, with the answer.
//
// Send records a.Text, which must not be blank, as a note to the Director about the task and decision, answering
// nothing, shows it to the Guildmaster as messages send shows a note, wakes the Director, and returns the
// message's id, NoteID's.
//
// Say is Send for words about the attempt a.Tag rather than a decision, from the Say box: it records them on the
// task's conversation under a.Tag and sends them as a note, refusing on a closed task.
//
// Round saves every box of the grill round p names as the Guildmaster's drafts, then does what its button asks:
// marks a question accepted, answered with its words or open again, or, for Send round, records the round's answers
// on the task's conversation with a grill-answered message to the Director and wakes it. It refuses unless the round
// is the latest of its attempt on an open task and not sent, and refuses, writing nothing, when the round has a draft
// newer than p.Drafts, the highest draft number its form rendered, unless the press replays the same post; Send round
// with a question unanswered saves the boxes and then refuses, naming them; a retry of a round already sent replays
// and returns nil. With p.Plan, p is a press of a plan approval's question controls instead: p.Tag is the decision,
// bound to p.Subject, and the boxes and mark are saved as its drafts, refusing unless the decision is still open;
// p.Drafts is not read, and there is no Send round, since the approval's answer sends them.
//
// With a.Plan, Answer first saves a.Boxes as the decision's drafts, then writes its answered questions after the
// answer as PlanAnswersText; an answer that would then be over the limit is refused and the drafts are kept.
//
// A refusal is a *Refusal, with the reason the page shows; a store held by a writer is ErrBusy; anything else is
// shown as a failure with no detail.
type Answerer interface {
	Answer(a Answer) error
	Send(a Answer) (string, error)
	Say(a Answer) (string, error)
	Round(p RoundPost) error
}

// The buttons of a round's form: Save, send later and Send round for the whole round, and Accept recommendation,
// Answer with my words and Change for one question.
const (
	RoundSave   = "save"
	RoundSend   = "send"
	RoundAccept = "accept"
	RoundWords  = "words"
	RoundChange = "change"
)

// RoundPost is one press of a button of a grill round's form: the round, by its tag and conversation entry number,
// the highest draft number of the round the form rendered (0 for none, and for a form from an older build that posts
// no drafts), the button (a Round constant) and for a question's button its number, the words of every box the form
// showed, by question number, and the Tailscale login that pressed it.
type RoundPost struct {
	Repository, TaskID, Tag string
	Round                   int
	Drafts                  int
	Act                     string
	Question                int
	Boxes                   map[int]string
	Login                   string
	// Subject and Plan are set on a press of a plan approval's question controls: the subject digest the page
	// showed and the spec's open questions.
	Subject string
	Plan    []PlanQuestion
	// Picks are a plan approval's changed picks the form sent, the letter by question number, each a letter of its
	// question's options (planPicks); a press saves them as that question's answer, but its own question's.
	Picks map[int]string
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

// NoteID is the message id of words sent on the page about one decision, or from the Say box about one attempt:
// NoteIDPrefix and 16 hex of a sha256 over the task, the decision or attempt and the words, so a double submit sends
// one note.
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
// the configured Host that every request's Host and an answer's Origin are checked against.
func (s *Server) answering() bool {
	return s.o.Answerer != nil && s.o.Identity != nil && s.o.Guildmaster != "" && s.o.Host != ""
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

// sameOrigin reports whether r comes from a page of this server: its Origin names the configured Host, by the same
// test as ServeHTTP's and never one read from the request's Host, or, when the browser sent no Origin or the opaque
// "null", its Sec-Fetch-Site is same-origin, which ServeHTTP's Host check ties to the configured Host. A request
// with neither is refused.
func (s *Server) sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.Path == "" &&
			sameHost(u.Host, s.o.Host)
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// pageState is what one request adds to a page: who is looking, the form token, and an ask's refusal or sent
// words, or the Say box's.
type pageState struct {
	identity
	Token string
	// Decision is the ask the refusal or the sent words are about.
	Decision string
	Refused  string
	Words    string
	Sent     string
	// Picks are the changed picks the refused answer sent, the letter by question number, which its cards check.
	Picks map[int]string
	// SayRefused is why words from the Say box were not sent, SayWords the words it kept, and Said the id of the
	// note it just sent.
	SayRefused, SayWords, Said string
	// RoundRefused is why the last press of a round's button was refused, and RoundBoxes the words its boxes held,
	// shown again in place of the saved drafts.
	RoundRefused string
	RoundBoxes   map[int]string
}

// readForm reads a POST's form, bounded by maxFormBytes, and reports whether it could; when it could not, it has
// written the refusal page.
func (s *Server) readForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.refuse(w, http.StatusRequestEntityTooLarge, "Not recorded", "The answer could not be read, so nothing was written. Go back and try a shorter answer.")
		return false
	}
	return true
}

// gate is the one check every POST passes before it may write, after ServeHTTP's Host check: the server answers at
// all, the request is from the Guildmaster's Tailscale login, it came from this server's own page and it carries the
// form token. It returns who sent r and, for a request it refuses, the status and the reason the page shows; reason
// is "" for a request that passes.
func (s *Server) gate(r *http.Request) (who identity, status int, reason string) {
	who = s.who(r)
	switch {
	case !s.answering():
		return who, http.StatusForbidden, "this page is read-only: the copy's [web] table names no guildmaster login"
	case !who.Answer:
		return who, http.StatusForbidden, "only the Guildmaster's Tailscale login can answer"
	case !s.sameOrigin(r):
		return who, http.StatusForbidden, "the answer did not come from this page"
	case subtle.ConstantTimeCompare([]byte(r.PostForm.Get("token")), []byte(s.token)) != 1:
		return who, http.StatusForbidden, "the page is out of date; reload it and answer again"
	}
	return who, 0, ""
}

// failed shows the page again, through again, for an Answerer's error and reports whether there was one: its
// refusal's status and reason, a busy store, or a failure logged as what and shown with no detail.
func (s *Server) failed(err error, what string, again func(int, string)) bool {
	if ref, ok := errors.AsType[*Refusal](err); ok {
		again(ref.Status, ref.Reason)
		return true
	}
	if errors.Is(err, ErrBusy) {
		again(http.StatusServiceUnavailable, "the store is busy; try again in a moment")
		return true
	}
	if err != nil {
		s.log("web: " + what + ": " + err.Error())
		again(http.StatusInternalServerError, "Guildhall could not record it; ask the Director")
		return true
	}
	return false
}

// answer handles POST /<repo>/<task>/answer: it passes the gate, then gives the Answerer the answer, the confirmed
// recommendation or the words, and redirects to the ask; a refusal shows the page again with its reason on the ask
// and the words and picks kept.
//
// Changed picks (readPicks) become "Qn (x)" lines before the words (withPicks) on Changes requested and on words
// only; Approved with a changed pick is refused, since it would take the recommendation the pick changed; any other
// answer ignores them. A malformed pick field is refused before the Answerer. On a plan approval with open questions
// on the page, Approved and Changes requested instead save each changed pick as its question's answer (Answer.Picks).
func (s *Server) answer(w http.ResponseWriter, r *http.Request, repo, id string) {
	if !s.readForm(w, r) {
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
	confirm := r.PostForm.Get("confirm")
	boxes := formBoxes(r, maxPlanBoxes)
	who, status, reason := s.gate(r)
	st := pageState{identity: who, Token: s.token, Decision: a.Decision, Words: a.Text, RoundBoxes: boxes}
	again := func(status int, reason string) {
		st.Refused = reason
		t, ok := s.read(w, repo, id)
		if !ok {
			return
		}
		s.render(w, status, "task", s.taskView(t, st))
	}
	if reason != "" {
		again(status, reason)
		return
	}
	picks, ok := readPicks(r.PostForm)
	if !ok {
		again(http.StatusBadRequest, "a pick is malformed")
		return
	}
	st.Picks = picks
	if act := r.PostForm.Get("act"); act != "" {
		s.planPress(w, r, a, act, boxes, picks, who, again)
		return
	}
	if a.Decision != "" && !words && (planOption(a.Option) || confirm != "") {
		// A plan approval's answer carries its open questions' answers, read from the spec the page shows; its
		// changed picks are answers of their questions, written once in that list, so they are no "Qn (x)" lines
		// and Approved takes them.
		t, ok := s.read(w, repo, id)
		if !ok {
			return
		}
		if plan := s.planQuestions(t, a.Decision); plan != nil {
			a.Plan, a.Boxes, a.Picks = plan, boxes, planPicks(plan, picks)
			picks = nil
		}
	}
	if confirm != "" && !words {
		n, err := strconv.Atoi(confirm)
		if err != nil || n < 1 {
			again(http.StatusBadRequest, "the confirmation names no recommendation")
			return
		}
		a.Confirm, a.Option = n, ""
	}
	switch {
	case a.Decision == "":
		again(http.StatusBadRequest, "the answer names no decision")
		return
	case !words && send != "answer" && a.Option == "" && a.Confirm == 0:
		again(http.StatusBadRequest, "choose an answer, or send your words only")
		return
	}
	if len(picks) > 0 && send == "" && a.Confirm == 0 {
		switch a.Option {
		case "approved":
			again(http.StatusBadRequest, "you changed "+pickNames(picks)+"; choose Changes requested")
			return
		case "changes_requested":
			a.Text = withPicks(picks, a.Text)
		}
	}
	if words && len(picks) > 0 {
		a.Text = withPicks(picks, a.Text)
	}
	a.Login = who.Login
	var err error
	var sent string
	if words {
		sent, err = s.o.Answerer.Send(a)
	} else {
		err = s.o.Answerer.Answer(a)
	}
	if s.failed(err, "answer "+repo+"/"+id+" #"+a.Decision, again) {
		return
	}
	target := taskHref(repo, id)
	if words {
		target += "?" + url.Values{"sent": {sent}, "ask": {a.Decision}}.Encode()
	}
	http.Redirect(w, r, target+"#"+url.PathEscape(a.Decision), http.StatusSeeOther)
}

// planOption reports whether option is one a plan approval's answer sends its open questions' answers with.
func planOption(option string) bool {
	return option == "approved" || option == "changes_requested"
}

// readPicks reads an answer's pick-<N> fields, the cards' radio groups: N a positive number as strconv.Itoa writes
// it, the value one lower-case letter or, on the recommended pick, empty, and each field sent once. It returns the
// changed picks, the letter by question number, and false for any field of another shape.
func readPicks(form url.Values) (map[int]string, bool) {
	var picks map[int]string
	for k, vs := range form {
		num, ok := strings.CutPrefix(k, "pick-")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(num)
		if err != nil || n < 1 || strconv.Itoa(n) != num || len(vs) != 1 {
			return nil, false
		}
		switch l := vs[0]; {
		case l == "":
		case len(l) == 1 && l[0] >= 'a' && l[0] <= 'z':
			if picks == nil {
				picks = map[int]string{}
			}
			picks[n] = l
		default:
			return nil, false
		}
	}
	return picks, true
}

// pickNumbers are the question numbers of picks, in order.
func pickNumbers(picks map[int]string) []int {
	return slices.Sorted(maps.Keys(picks))
}

// pickNames names the questions of picks, in order, as "Q2" or "Q2, Q3".
func pickNames(picks map[int]string) string {
	var names []string
	for _, n := range pickNumbers(picks) {
		names = append(names, "Q"+strconv.Itoa(n))
	}
	return strings.Join(names, ", ")
}

// withPicks is the text an answer records with changed picks: a "Qn (x)" line per pick in question order, then a
// blank line and words, or the lines alone when words are blank, so the same picks and words give the same bytes.
func withPicks(picks map[int]string, words string) string {
	var lines []string
	for _, n := range pickNumbers(picks) {
		lines = append(lines, "Q"+strconv.Itoa(n)+" ("+picks[n]+")")
	}
	text := strings.Join(lines, "\n")
	if strings.TrimSpace(words) != "" {
		text += "\n\n" + words
	}
	return text
}

// say handles POST /<repo>/<task>/say, the Say box: it passes the gate, then gives the Answerer the words about the
// attempt the box names, answering nothing, and redirects to that attempt's talk; a refusal shows the page again
// with its reason in the box and the words kept.
func (s *Server) say(w http.ResponseWriter, r *http.Request, repo, id string) {
	if !s.readForm(w, r) {
		return
	}
	a := Answer{Repository: repo, TaskID: id, Tag: r.PostForm.Get("tag"), Text: r.PostForm.Get("text")}
	who, status, reason := s.gate(r)
	st := pageState{identity: who, Token: s.token, SayWords: a.Text}
	again := func(status int, reason string) {
		st.SayRefused = reason
		t, ok := s.read(w, repo, id)
		if !ok {
			return
		}
		s.render(w, status, "task", s.taskView(t, st))
	}
	switch {
	case reason != "":
		again(status, reason)
		return
	case a.Tag == "":
		again(http.StatusBadRequest, "the words name no attempt")
		return
	}
	a.Login = who.Login
	sent, err := s.o.Answerer.Say(a)
	if s.failed(err, "say "+repo+"/"+id+" "+a.Tag, again) {
		return
	}
	target := taskHref(repo, id) + "?" + url.Values{"said": {sent}}.Encode()
	http.Redirect(w, r, target+"#"+url.PathEscape(talkID(a.Tag)), http.StatusSeeOther)
}

// round handles POST /<repo>/<task>/round, a button of a grill round's form: it passes the gate, then gives the
// Answerer the round, the button and every box's words, and redirects to the round, or after Send round to its
// talk; a refusal shows the page again with its reason on the round and the boxes' words kept.
func (s *Server) round(w http.ResponseWriter, r *http.Request, repo, id string) {
	if !s.readForm(w, r) {
		return
	}
	p := RoundPost{Repository: repo, TaskID: id, Tag: r.PostForm.Get("tag"), Boxes: formBoxes(r, MaxRoundQuestions)}
	who, status, reason := s.gate(r)
	st := pageState{identity: who, Token: s.token, RoundBoxes: p.Boxes}
	again := func(status int, reason string) {
		st.RoundRefused = reason
		t, ok := s.read(w, repo, id)
		if !ok {
			return
		}
		s.render(w, status, "task", s.taskView(t, st))
	}
	if reason != "" {
		again(status, reason)
		return
	}
	act, q, ok := roundAct(r.PostForm.Get("act"), MaxRoundQuestions)
	n, err := strconv.Atoi(r.PostForm.Get("round"))
	drafts, derr := 0, error(nil)
	if _, posted := r.PostForm["drafts"]; posted {
		// A form from an older build posts no drafts, which is 0.
		drafts, derr = strconv.Atoi(r.PostForm.Get("drafts"))
	}
	switch {
	case p.Tag == "":
		again(http.StatusBadRequest, "the round names no attempt")
		return
	case err != nil || n < 1:
		again(http.StatusBadRequest, "the form names no round")
		return
	case !ok:
		again(http.StatusBadRequest, "the form names no button of the round")
		return
	case derr != nil || drafts < 0:
		again(http.StatusBadRequest, "the form's drafts number does not read")
		return
	}
	p.Round, p.Drafts, p.Act, p.Question, p.Login = n, drafts, act, q, who.Login
	if s.failed(s.o.Answerer.Round(p), "round "+repo+"/"+id+" "+p.Tag, again) {
		return
	}
	target := taskHref(repo, id)
	switch {
	case act == RoundSend:
		target += "#" + url.PathEscape(talkID(p.Tag))
	case q > 0:
		target += "#" + url.PathEscape(roundQuestionID(p.Tag, q))
	default:
		target += "#" + url.PathEscape(p.Tag)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// roundAct reads a round button's value: save or send, or accept-<n>, words-<n> or change-<n> for question n, from
// 1 to most.
func roundAct(v string, most int) (act string, question int, ok bool) {
	switch v {
	case RoundSave, RoundSend:
		return v, 0, true
	}
	act, num, found := strings.Cut(v, "-")
	n, err := strconv.Atoi(num)
	if !found || err != nil || n < 1 || n > most || (act != RoundAccept && act != RoundWords && act != RoundChange) {
		return "", 0, false
	}
	return act, n, true
}

// maxPlanBoxes bounds the question boxes a plan approval's form is read for; a spec's open questions are cut at
// spec.QuestionsMax bytes, far fewer.
const maxPlanBoxes = 100

// formBoxes are the words of each question box r's form sent, text-<n> for n from 1 to most, by question number.
func formBoxes(r *http.Request, most int) map[int]string {
	boxes := map[int]string{}
	for n := 1; n <= most; n++ {
		if words, ok := r.PostForm["text-"+strconv.Itoa(n)]; ok && len(words) > 0 {
			boxes[n] = words[0]
		}
	}
	return boxes
}

// planPress is a press of a plan approval's question controls, posted with the approval's form: Accept
// recommendation, Answer with my words or Change on one question, or Save on them all. It gives the Answerer the
// boxes, the changed picks and the mark to save as the decision's drafts, read against the spec the page shows, and
// redirects to the question, or to the ask; a refusal shows the page again with its reason on the ask and every
// box's words and pick kept.
func (s *Server) planPress(w http.ResponseWriter, r *http.Request, a Answer, act string, boxes, picks map[int]string, who identity, again func(int, string)) {
	act, q, ok := roundAct(act, maxPlanBoxes)
	switch {
	case a.Decision == "":
		again(http.StatusBadRequest, "the answer names no decision")
		return
	case !ok || act == RoundSend:
		again(http.StatusBadRequest, "the form names no button of the open questions")
		return
	}
	t, found := s.read(w, a.Repository, a.TaskID)
	if !found {
		return
	}
	plan := s.planQuestions(t, a.Decision)
	if plan == nil {
		again(http.StatusConflict, "changed since you loaded the page: #"+a.Decision+" has no open questions to answer here; reload it")
		return
	}
	p := RoundPost{Repository: a.Repository, TaskID: a.TaskID, Tag: a.Decision, Round: PlanRound, Act: act, Question: q, Boxes: boxes,
		Login: who.Login, Subject: a.Subject, Plan: plan, Picks: planPicks(plan, picks)}
	if s.failed(s.o.Answerer.Round(p), "plan questions "+a.Repository+"/"+a.TaskID+" #"+a.Decision, again) {
		return
	}
	target := taskHref(a.Repository, a.TaskID) + "#" + url.PathEscape(a.Decision)
	if q > 0 {
		target = taskHref(a.Repository, a.TaskID) + "#" + url.PathEscape(roundQuestionID(a.Decision, q))
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
