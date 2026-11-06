package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

// feedReply is GET /list.json's body: the task list as data, never HTML. It holds what the page shows in its
// default order, unfiltered, so the browser filters and sorts it itself: the band's lines, Needs you, the stage
// groups, Recently finished and the stats. Every time is a feedTime.
type feedReply struct {
	ReadAt   feedTime    `json:"read_at"`
	Count    string      `json:"count"`
	You      bool        `json:"you"`
	Meta     string      `json:"meta"`
	Open     int         `json:"open"`
	Needs    []feedRow   `json:"needs"`
	Groups   []feedGroup `json:"groups"`
	Finished *feedGroup  `json:"finished"`
	Stats    feedStats   `json:"stats"`
}

// feedTime is one time of the feed: its instant as RFC 3339 and its exact time in the server's zone, the <time>'s
// datetime and title; the browser writes the relative text.
type feedTime struct {
	At    string `json:"at"`
	Title string `json:"title"`
}

func newFeedTime(tv *timeView) *feedTime {
	if tv == nil {
		return nil
	}
	return &feedTime{At: tv.ISO, Title: tv.Title}
}

// feedGroup is one group of the list: its anchor, title, status line, whether its bar is blue (a running stage),
// the merge queue's front banners and its rows.
type feedGroup struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Status  string       `json:"status"`
	Moving  bool         `json:"moving"`
	Banners []feedBanner `json:"banners,omitempty"`
	Rows    []feedRow    `json:"rows"`
}

// feedBanner is a merge-queue front's banner: "Front: d208, PR #371.", "Waiting on CI" and since when.
type feedBanner struct {
	Front string    `json:"front"`
	Wait  string    `json:"wait"`
	Since *feedTime `json:"since,omitempty"`
}

// feedRow is one task's row, its cells as the page writes them, with the task's own repository, priority and
// attempt for sorting.
type feedRow struct {
	Repository string    `json:"repository"`
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Href       string    `json:"href"`
	Title      string    `json:"title"`
	Priority   int       `json:"priority"`
	Pri        string    `json:"pri,omitempty"`
	Step       string    `json:"step,omitempty"`
	Rest       string    `json:"rest,omitempty"`
	Attempt    int       `json:"attempt,omitempty"`
	Held       bool      `json:"held,omitempty"`
	Slice      string    `json:"slice,omitempty"`
	PR         *feedPR   `json:"pr,omitempty"`
	Since      *feedTime `json:"since,omitempty"`
	Ask        string    `json:"ask,omitempty"`
	You        bool      `json:"you,omitempty"`
	Position   int       `json:"position,omitempty"`
	Front      bool      `json:"front,omitempty"`
}

// feedPR is a row's pull request: its number, its link ("" when its URL is not http or https) and its CI mark's
// class ("ok", "run" or "bad") and words.
type feedPR struct {
	Number int    `json:"number"`
	Href   string `json:"href,omitempty"`
	CI     string `json:"ci,omitempty"`
	CIWord string `json:"ci_word,omitempty"`
}

// feedStats is the stats strip: its numbers, an unavailable vital null, and Text, the strip's words.
type feedStats struct {
	Running      int         `json:"running"`
	RunningLimit int         `json:"running_limit"`
	StartQueue   int         `json:"start_queue"`
	MergeQueue   int         `json:"merge_queue"`
	Fronts       []frontLink `json:"fronts"`
	MergedToday  int         `json:"merged_today"`
	Day          string      `json:"day"`
	Spend        feedSpend   `json:"spend"`
	Machine      feedMachine `json:"machine"`
	Text         stripView   `json:"text"`
}

type feedSpend struct {
	Attempts    int     `json:"attempts"`
	Tokens      int64   `json:"tokens"`
	CostUSD     float64 `json:"cost_usd"`
	CostMeasure string  `json:"cost_measure"`
}

type feedMachine struct {
	Load      *float64 `json:"load"`
	Cores     int      `json:"cores"`
	MemUsed   *uint64  `json:"mem_used"`
	MemTotal  *uint64  `json:"mem_total"`
	DiskFree  *uint64  `json:"disk_free"`
	DiskTotal *uint64  `json:"disk_total"`
}

// listFeed handles GET /list.json, the task list's data for the page's own script: read-only for every login, the
// same Source read as the page, through ServeHTTP's Host check and headers. A busy store is 503 and any other
// failure 500 with no detail; every refusal is feedRefuse's JSON, never HTML.
func (s *Server) listFeed(w http.ResponseWriter) {
	l, st, ok := s.readList(w, feedRefuse)
	if !ok {
		return
	}
	b, err := json.Marshal(s.feed(l, st))
	if err != nil {
		s.log("web: the task list feed: " + err.Error())
		feedRefuse(w, http.StatusInternalServerError, "", "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(b, '\n'))
}

// feedRefuse writes the feed's refusal for status as application/json: {"error":"busy"} for 503, {"error":
// "wrong address"} for 421 and {"error":"failed"} for anything else. It takes readList's refusal arguments but
// ignores the page's title and words, so the feed carries no HTML and no detail.
func feedRefuse(w http.ResponseWriter, status int, _, _ string) {
	reason := "failed"
	switch status {
	case http.StatusServiceUnavailable:
		reason = "busy"
	case http.StatusMisdirectedRequest:
		reason = "wrong address"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + reason + `"}` + "\n"))
}

// feed is the feed's reply for l and st, from the page's own model in its default order.
func (s *Server) feed(l TaskList, st Stats) feedReply {
	v := s.listView(l, st, listQuery{})
	out := feedReply{ReadAt: *newFeedTime(&v.ReadAt), Count: v.Count, You: v.You, Meta: v.Meta, Open: v.Open,
		Needs: feedRows(v.Needs), Groups: []feedGroup{}, Stats: newFeedStats(st, v.Strip)}
	for _, g := range v.Groups {
		out.Groups = append(out.Groups, newFeedGroup(g))
	}
	if v.Finished != nil {
		g := newFeedGroup(*v.Finished)
		out.Finished = &g
	}
	return out
}

func newFeedGroup(g listGroup) feedGroup {
	out := feedGroup{ID: g.ID, Title: g.Title, Status: g.Status, Moving: strings.Contains(" "+g.Class+" ", " mv "), Rows: feedRows(g.Rows)}
	for _, b := range g.Banners {
		out.Banners = append(out.Banners, feedBanner{Front: b.Front, Wait: b.Wait, Since: newFeedTime(b.Since)})
	}
	return out
}

func feedRows(rows []listRow) []feedRow {
	out := []feedRow{}
	for _, r := range rows {
		f := feedRow{Repository: r.Repository, ID: r.ID, Name: r.Repo + r.ID, Href: r.Href, Title: r.Title, Priority: r.Priority, Pri: r.Pri,
			Step: r.Step, Rest: r.Rest, Attempt: r.Attempt, Held: r.Held, Slice: r.Slice, Since: newFeedTime(r.Time), Ask: r.Ask, You: r.You,
			Position: r.Position, Front: r.Front}
		if r.PR != nil {
			f.PR = &feedPR{Number: r.PR.Number, Href: r.PRHref, CI: r.CI.Class, CIWord: r.CI.Word}
		}
		out = append(out, f)
	}
	return out
}

func newFeedStats(st Stats, text stripView) feedStats {
	out := feedStats{Running: st.Running, RunningLimit: st.RunningLimit, StartQueue: st.StartQueue, MergeQueue: st.MergeQueue,
		Fronts: text.Fronts, MergedToday: st.MergedToday, Day: st.Day.UTC().Format("2006-01-02"),
		Spend:   feedSpend{Attempts: st.Spend.Attempts, Tokens: st.Spend.Tokens, CostUSD: st.Spend.CostUSD, CostMeasure: st.Spend.CostMeasure},
		Machine: feedMachine{Cores: st.Machine.Cores}, Text: text}
	if out.Fronts == nil {
		out.Fronts = []frontLink{}
	}
	m := st.Machine
	if m.LoadOK {
		out.Machine.Load = &m.Load
	}
	if m.MemOK {
		out.Machine.MemUsed, out.Machine.MemTotal = &m.MemUsed, &m.MemTotal
	}
	if m.DiskOK {
		out.Machine.DiskFree, out.Machine.DiskTotal = &m.DiskFree, &m.DiskTotal
	}
	return out
}
