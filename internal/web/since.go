package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// sinceReply is GET /<repo>/<task>/since's body, which poll.js reads: the server's start id, the conversation's last
// message number, and how many of the messages after the page's are the Director's, with the newest one's tag and
// time ("15:04"), "" when there is none.
type sinceReply struct {
	Start string `json:"start"`
	Last  int    `json:"last"`
	New   int    `json:"new"`
	Tag   string `json:"tag,omitempty"`
	At    string `json:"at,omitempty"`
}

// pollView is what a task page gives poll.js through the new-reply bar's data-* attributes: the since route, the
// task page's path that Reload goes to, the number of the conversation's last message the page shows and the
// server's start id it was rendered under.
type pollView struct {
	Since, Base, Start string
	Last               int
}

// newPollView is the new-reply bar's model for t's page, only for the Guildmaster's login on an open task: no one
// else can answer, so no one else needs to know of a reply or a stale form token, and a closed task takes no talk.
func (s *Server) newPollView(t Task, st pageState) *pollView {
	if !st.Answer || t.State != "open" {
		return nil
	}
	v := &pollView{Base: taskHref(t.Repository, t.ID), Start: s.start}
	v.Since = v.Base + "/since"
	for _, m := range t.Conversation {
		v.Last = max(v.Last, m.Number)
	}
	return v
}

// since handles GET /<repo>/<task>/since?after=<n>, the poll behind the new-reply bar: for the Guildmaster's login
// only, it reads the conversation's tail through Source.Tail, which opens the store read-only and builds no page
// model, and answers sinceReply as JSON. It writes nothing, so it needs no form token or Origin; ServeHTTP's Host
// check comes first. A missing after is 0, and one that is not a number as strconv.Itoa writes it is 400.
func (s *Server) since(w http.ResponseWriter, r *http.Request, repo, id string) {
	who := s.who(r)
	if !s.answering() || !who.Answer {
		s.refuse(w, http.StatusForbidden, "Not allowed", "Only the Guildmaster's Tailscale login is told of new talk.")
		return
	}
	// A browser names the site a request came from; only the page's own poll, or the address typed by hand, is
	// answered, so no other site's page can even ask.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		s.refuse(w, http.StatusForbidden, "Not allowed", "Only this server's own pages ask for new talk.")
		return
	}
	after := 0
	if a := r.URL.Query().Get("after"); a != "" {
		n, err := strconv.Atoi(a)
		if err != nil || n < 0 || strconv.Itoa(n) != a {
			s.refuse(w, http.StatusBadRequest, "Bad request", "after must be a message number.")
			return
		}
		after = n
	}
	tail, err := s.o.Source.Tail(repo, id, after)
	switch {
	case errors.Is(err, ErrNotFound):
		s.refuse(w, http.StatusNotFound, "No such task", "No task "+repo+"/"+id+" in this copy.")
		return
	case errors.Is(err, ErrBusy):
		s.refuse(w, http.StatusServiceUnavailable, "The store is busy", "The Director is writing to the store.")
		return
	case err != nil:
		s.log("web: poll " + repo + "/" + id + ": " + err.Error())
		s.refuse(w, http.StatusInternalServerError, "The task could not be read", "Guildhall could not read this task's talk.")
		return
	}
	reply := sinceReply{Start: s.start, Last: tail.Last}
	for _, m := range tail.Newer {
		if m.Number <= after || m.Author != AuthorDirector {
			continue
		}
		reply.New++
		reply.Tag, reply.At = m.Tag, s.clock(m.At)
	}
	b, err := json.Marshal(reply)
	if err != nil {
		s.log("web: poll " + repo + "/" + id + ": " + err.Error())
		s.refuse(w, http.StatusInternalServerError, "The task could not be read", "Guildhall could not read this task's talk.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(b, '\n'))
}
