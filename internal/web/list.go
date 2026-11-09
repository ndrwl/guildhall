package web

import (
	"cmp"
	"html/template"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// stageGroups are the task list's stage groups in the order it shows them, each with its heading; Moving says its
// heading's bar is blue, a running stage, rather than grey.
var stageGroups = []struct {
	Group  Group
	Title  string
	Moving bool
}{
	{GroupPlan, "Grill and plan", true},
	{GroupBuild, "Build and review", true},
	{GroupCI, "CI", true},
	{GroupMergeQueue, "Merge queue", true},
	{GroupHeld, "Held", false},
}

// listSorts are the task list's sort keys in the column header's order, each with its header and phone chip label.
var listSorts = []struct {
	Key, Head, Chip string
}{
	{"task", "Task", "Id"},
	{"pri", "Pri", "Priority"},
	{"stage", "Stage · step", "Stage"},
	{"pr", "PR", "PR"},
	{"time", "In state", "In state"},
}

// listQuery is the task list's filter and sort, read from the URL (/?q=&p=&repo=&sort=&dir=), so a URL gives the same
// view with scripts off: Q matches a task's id or title, P its priority ("p1") and Repo its repository; Sort is a
// listSorts key, "" for the default (priority, then oldest in state), and Desc reverses it. A value the list does not
// know is dropped.
type listQuery struct {
	Q, P, Repo, Sort string
	Desc             bool
}

func readListQuery(v url.Values) listQuery {
	q := listQuery{Q: strings.TrimSpace(v.Get("q")), Repo: v.Get("repo")}
	if p := v.Get("p"); len(p) == 2 && p[0] == 'p' && p[1] >= '0' && p[1] <= '5' {
		q.P = p
	}
	if s := v.Get("sort"); slices.ContainsFunc(listSorts, func(k struct{ Key, Head, Chip string }) bool { return k.Key == s }) {
		q.Sort = s
	}
	q.Desc = v.Get("dir") == "desc"
	return q
}

// href is the list's URL with q's values, leaving out the empty ones.
func (q listQuery) href() string {
	v := url.Values{}
	for _, kv := range [][2]string{{"q", q.Q}, {"p", q.P}, {"repo", q.Repo}, {"sort", q.Sort}} {
		if kv[1] != "" {
			v.Set(kv[0], kv[1])
		}
	}
	if q.Desc {
		v.Set("dir", "desc")
	}
	if len(v) == 0 {
		return "/"
	}
	return "/?" + v.Encode()
}

// filtered says whether q hides any task.
func (q listQuery) filtered() bool { return q.Q != "" || q.P != "" || q.Repo != "" }

// matches says whether t passes q's filter.
func (q listQuery) matches(t Listed) bool {
	if q.P != "" && q.P != "p"+strconv.Itoa(t.Priority) {
		return false
	}
	if q.Repo != "" && q.Repo != t.Repository {
		return false
	}
	if q.Q == "" {
		return true
	}
	needle := strings.ToLower(q.Q)
	return strings.Contains(strings.ToLower(t.Repository+"/"+t.ID), needle) || strings.Contains(strings.ToLower(t.Title), needle)
}

// listView is the task list's model, every value formatted for the templates.
type listView struct {
	Style template.CSS
	// Scripts are the own scripts the page inlines.
	Scripts []template.JS
	// Count is the band's first line, such as "3 tasks wait for you", amber when You; Meta its second, such as
	// "25 open · 16 in the merge queue", which the read time follows.
	Count, Meta string
	You         bool
	ReadAt      timeView
	// Strip is the stats strip, full width above Needs you.
	Strip stripView
	// Needs are the tasks waiting for the Guildmaster, oldest ask first, which the filter never hides.
	Needs []listRow
	// Query is the filter and sort in effect, Clear the list's URL without the filter, and the chips and sort links
	// those the filter bar and column header show.
	Query       listQuery
	Clear       string
	Priorities  []chip
	Repos       []chip
	Sorts       []sortLink
	Groups      []listGroup
	Finished    *listGroup
	Open        int
	NoneMatches bool
}

// chip is one filter chip: its label, its count, the list's URL with it toggled, and whether it is in effect.
type chip struct {
	Label string
	Count int
	Href  string
	On    bool
}

// sortLink is one sort of the column header and the phone's sort chips: its labels, its URL (reversing the sort in
// effect), whether it is the sort in effect and whether that is reversed.
type sortLink struct {
	Head, Chip, Href string
	On, Desc         bool
}

// listGroup is one group of the list: its anchor, class, heading, status line and rows, and for the merge queue
// the banners saying what holds each front.
type listGroup struct {
	ID, Class, Title, Status string
	Rows                     []listRow
	Banners                  []queueBanner
}

// queueBanner says what holds a merge queue's front: "Front: d208, PR #371." then "Waiting on CI since", Since.
type queueBanner struct {
	Front, Wait string
	Since       *timeView
}

// listRow is one task's row: its id (Repo, the faint repository part, set when the list spans more than one
// repository) linked to its page, its title, priority, step, slice, pull request and CI, and the time it entered its
// state; a Needs you row adds its ask, and a merge-queue row its place.
type listRow struct {
	Repo, ID, Href, Title string
	Pri                   string
	// Repository, Priority and Attempt are the task's own, which the feed carries for sorting in the browser.
	Repository        string
	Priority, Attempt int
	// Step is the bold part of the stage and step cell and Rest what follows it, such as "build" and " · attempt 2";
	// Held marks a merge-queue entry the Director holds.
	Step, Rest string
	Held       bool
	Slice      string
	PR         *PullRequest
	PRHref     string
	CI         ciMark
	Time       *timeView
	Ask        string
	You        bool
	Position   int
	Front      bool
}

// ciMark is a pull request's CI mark: ✓ passed, ● running, ✗ failed; its zero value shows none.
type ciMark struct {
	Class, Mark, Word string
}

func ciMarkOf(ci string) ciMark {
	switch ci {
	case CIPassed:
		return ciMark{"ok", "✓", "CI passed"}
	case CIPending:
		return ciMark{"run", "●", "CI running"}
	case CIFailed:
		return ciMark{"bad", "✗", "CI failed"}
	}
	return ciMark{}
}

func (s *Server) listView(l TaskList, st Stats, q listQuery) listView {
	v := listView{Style: style, Scripts: scripts, ReadAt: s.timeAt(l.ReadAt, l.ReadAt), Query: q, Strip: newStripView(st)}
	multi := false
	for _, t := range l.Tasks {
		multi = multi || t.Repository != l.Tasks[0].Repository
	}
	byGroup := map[Group][]Listed{}
	queued, priCount, repoCount := 0, map[int]int{}, map[string]int{}
	var repos []string
	for _, t := range l.Tasks {
		g := t.Group
		switch g {
		case GroupYou, GroupPlan, GroupBuild, GroupCI, GroupMergeQueue, GroupFinished:
		default:
			g = GroupHeld
		}
		byGroup[g] = append(byGroup[g], t)
		if g == GroupFinished {
			continue
		}
		v.Open++
		if t.Queue != nil {
			queued++
		}
		priCount[t.Priority]++
		if repoCount[t.Repository] == 0 {
			repos = append(repos, t.Repository)
		}
		repoCount[t.Repository]++
	}
	you := len(byGroup[GroupYou])
	v.Count, v.You = youLine(you), you > 0
	v.Meta = metaLine(v.Open, queued)

	row := func(t Listed) listRow { return s.listRow(t, l, multi) }

	needs := slices.Clone(byGroup[GroupYou])
	slices.SortStableFunc(needs, func(a, b Listed) int { return cmpSince(a, b) })
	for _, t := range needs {
		v.Needs = append(v.Needs, row(t))
	}

	shown := 0
	for _, sg := range stageGroups {
		var ts []Listed
		for _, t := range byGroup[sg.Group] {
			if q.matches(t) {
				ts = append(ts, t)
			}
		}
		if len(ts) == 0 {
			continue
		}
		shown += len(ts)
		g := listGroup{ID: "g-" + string(sg.Group), Class: "sg", Title: sg.Title}
		if sg.Moving {
			g.Class += " mv"
		}
		if sg.Group == GroupMergeQueue {
			slices.SortStableFunc(ts, func(a, b Listed) int {
				return cmp.Or(strings.Compare(a.Repository, b.Repository), cmp.Compare(queuePos(a), queuePos(b)))
			})
			g.Banners = s.queueBanners(l, multi)
		} else {
			sortListed(ts, q)
		}
		for _, t := range ts {
			g.Rows = append(g.Rows, row(t))
		}
		g.Status = groupStatus(sg.Group, ts)
		v.Groups = append(v.Groups, g)
	}
	var fin []Listed
	for _, t := range byGroup[GroupFinished] {
		if q.matches(t) {
			fin = append(fin, t)
		}
	}
	if len(fin) > 0 {
		slices.SortStableFunc(fin, func(a, b Listed) int { return -cmpSince(a, b) })
		g := listGroup{ID: "g-done", Class: "sg fin", Title: "Recently finished", Status: "last 24 hours · " + finishedLine(fin)}
		for _, t := range fin {
			g.Rows = append(g.Rows, row(t))
		}
		v.Finished = &g
	}
	v.NoneMatches = q.filtered() && shown == 0 && len(fin) == 0

	clear := listQuery{Sort: q.Sort, Desc: q.Desc}
	v.Clear = clear.href()
	if v.Open > 0 {
		all := q
		all.P = ""
		v.Priorities = append(v.Priorities, chip{Label: "All", Href: all.href(), On: q.P == ""})
		for p := 0; p <= 5; p++ {
			label := "p" + strconv.Itoa(p)
			if priCount[p] == 0 && q.P != label {
				continue
			}
			c := q
			c.P = label
			if q.P == label {
				c.P = ""
			}
			v.Priorities = append(v.Priorities, chip{Label: label, Count: priCount[p], Href: c.href(), On: q.P == label})
		}
	}
	if multi && v.Open > 0 {
		all := q
		all.Repo = ""
		v.Repos = append(v.Repos, chip{Label: "All", Count: v.Open, Href: all.href(), On: q.Repo == ""})
		for _, r := range repos {
			c := q
			c.Repo = r
			if q.Repo == r {
				c.Repo = ""
			}
			v.Repos = append(v.Repos, chip{Label: r, Count: repoCount[r], Href: c.href(), On: q.Repo == r})
		}
	}
	for _, k := range listSorts {
		c := q
		on := q.Sort == k.Key || (q.Sort == "" && k.Key == "pri")
		c.Sort, c.Desc = k.Key, on && !q.Desc
		if !c.Desc && k.Key == "pri" {
			c.Sort = ""
		}
		v.Sorts = append(v.Sorts, sortLink{Head: k.Head, Chip: k.Chip, Href: c.href(), On: on, Desc: on && q.Desc})
	}
	return v
}

func queuePos(t Listed) int {
	if t.Queue == nil {
		return int(^uint(0) >> 1)
	}
	return t.Queue.Position
}

// cmpSince orders by since, oldest first, an unknown since last, then by repository and id.
func cmpSince(a, b Listed) int {
	switch {
	case a.Since == nil && b.Since != nil:
		return 1
	case a.Since != nil && b.Since == nil:
		return -1
	case a.Since != nil:
		if c := a.Since.Compare(*b.Since); c != 0 {
			return c
		}
	}
	return cmp.Or(strings.Compare(a.Repository, b.Repository), strings.Compare(a.ID, b.ID))
}

// sortListed sorts a stage group's tasks by q's sort: by default priority, then oldest in state. A task with no value
// for the sort, such as no pull request, comes last either way.
func sortListed(ts []Listed, q listQuery) {
	dir := 1
	if q.Desc {
		dir = -1
	}
	slices.SortStableFunc(ts, func(a, b Listed) int {
		switch q.Sort {
		case "task":
			return dir * cmp.Or(strings.Compare(a.Repository, b.Repository), strings.Compare(a.ID, b.ID))
		case "stage":
			return cmp.Or(dir*cmp.Or(strings.Compare(a.Step, b.Step), cmp.Compare(a.Attempt, b.Attempt)), cmpSince(a, b))
		case "pr":
			switch {
			case a.PR == nil && b.PR == nil:
				return cmpSince(a, b)
			case a.PR == nil:
				return 1
			case b.PR == nil:
				return -1
			}
			return cmp.Or(dir*cmp.Compare(a.PR.Number, b.PR.Number), cmpSince(a, b))
		case "time":
			if (a.Since == nil) != (b.Since == nil) {
				return cmpSince(a, b)
			}
			return dir * cmpSince(a, b)
		}
		return cmp.Or(dir*cmp.Compare(a.Priority, b.Priority), cmpSince(a, b))
	})
}

func (s *Server) listRow(t Listed, l TaskList, multi bool) listRow {
	r := listRow{ID: t.ID, Href: taskHref(t.Repository, t.ID), Title: t.Title, PR: t.PR, CI: ciMarkOf(t.CI), Ask: t.Ask, You: t.Group == GroupYou,
		Repository: t.Repository, Priority: t.Priority, Attempt: t.Attempt}
	if multi {
		r.Repo = t.Repository + "/"
	}
	if t.PR != nil {
		r.PRHref = safeURL(t.PR.URL)
	}
	if t.Priority != 3 {
		r.Pri = "p" + strconv.Itoa(t.Priority)
	}
	if t.Slices > 1 {
		r.Slice = strconv.Itoa(t.Slice) + "/" + strconv.Itoa(t.Slices)
	}
	if t.Since != nil {
		tv := s.timeAt(*t.Since, l.ReadAt)
		r.Time = &tv
	}
	attempt := ""
	if t.Attempt > 0 {
		attempt = " · attempt " + strconv.Itoa(t.Attempt)
	}
	switch {
	case t.Group == GroupFinished:
		r.Step = cmp.Or(t.Finished, "done")
		if t.Finished == "cancelled" && t.Where.Rest != "" {
			r.Rest = " · " + strings.TrimSuffix(t.Where.Rest, ".")
		}
		if t.Finished == "merged" {
			r.CI = ciMark{}
		}
	case t.Group == GroupMergeQueue && t.Queue != nil:
		r.Position, r.Front = t.Queue.Position, t.Queue.Front
		switch {
		case t.Queue.Held:
			r.Held, r.Rest = true, "the Director holds its merge"
		case t.Queue.Front:
			r.Step, r.Rest = "front", " · "+waitingOnWords(t.Queue.WaitingOn)
		default:
			r.Rest = "waiting"
			if r.CI.Word != "" {
				r.Rest += " · checks " + strings.TrimPrefix(r.CI.Word, "CI ")
			}
		}
	case t.Group == GroupCI:
		r.Step, r.Rest = "CI", " · checks running"
		if r.CI.Class == "bad" {
			r.Rest = " · checks failed"
		}
	case t.Group == GroupHeld:
		w := t.Where
		if w.Lead == "" && t.Stands != "" {
			w.Lead = upperFirst(t.Stands) + "."
		}
		r.Rest = strings.TrimSpace(w.Lead + " " + w.Rest)
	default:
		r.Step, r.Rest = t.Step, attempt
	}
	return r
}

// waitingOnWords says what the merge queue's front waits on.
func waitingOnWords(on string) string {
	switch on {
	case "ci":
		return "waiting on CI"
	case "rebase":
		return "waiting on a rebase"
	case "decision":
		return "waiting on a decision"
	}
	return "waiting"
}

// queueBanners are the banners of each repository's merge-queue front, in repository order: which task and pull
// request it is, what holds it and since when.
func (s *Server) queueBanners(l TaskList, multi bool) []queueBanner {
	var fronts []Listed
	for _, t := range l.Tasks {
		if t.Queue != nil && t.Queue.Front && t.Group != GroupFinished {
			fronts = append(fronts, t)
		}
	}
	slices.SortStableFunc(fronts, func(a, b Listed) int { return strings.Compare(a.Repository, b.Repository) })
	var out []queueBanner
	for _, t := range fronts {
		name := t.ID
		if multi {
			name = t.Repository + "/" + t.ID
		}
		b := queueBanner{Front: "Front: " + name}
		if t.PR != nil {
			b.Front += ", PR #" + strconv.Itoa(t.PR.Number)
		}
		b.Front += "."
		b.Wait = upperFirst(waitingOnWords(t.Queue.WaitingOn))
		if t.Since != nil {
			tv := s.timeAt(*t.Since, l.ReadAt)
			b.Since = &tv
		}
		out = append(out, b)
	}
	return out
}

// groupStatus is a stage group's status line, such as "2 build, 1 review" or "queue order · 2 held".
func groupStatus(g Group, ts []Listed) string {
	switch g {
	case GroupCI:
		running, failed := 0, 0
		for _, t := range ts {
			if t.CI == CIFailed {
				failed++
			} else {
				running++
			}
		}
		var parts []string
		if running > 0 {
			parts = append(parts, "checks running on "+prCount(running))
		}
		if failed > 0 {
			parts = append(parts, "checks failed on "+prCount(failed))
		}
		return strings.Join(parts, " · ")
	case GroupMergeQueue:
		held := 0
		for _, t := range ts {
			if t.Queue != nil && t.Queue.Held {
				held++
			}
		}
		if held > 0 {
			return "queue order · " + strconv.Itoa(held) + " held"
		}
		return "queue order"
	case GroupHeld:
		return "waiting on other tasks or the Director, paused or not started"
	}
	var steps []string
	n := map[string]int{}
	for _, t := range ts {
		step := cmp.Or(t.Step, "step")
		if n[step] == 0 {
			steps = append(steps, step)
		}
		n[step]++
	}
	var parts []string
	for _, st := range steps {
		parts = append(parts, strconv.Itoa(n[st])+" "+st)
	}
	return strings.Join(parts, ", ")
}

// finishedLine counts the finished tasks by how they ended, such as "5 merged, 1 cancelled".
func finishedLine(ts []Listed) string {
	var parts []string
	for _, end := range []string{"merged", "done", "cancelled"} {
		n := 0
		for _, t := range ts {
			if cmp.Or(t.Finished, "done") == end {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+end)
		}
	}
	return strings.Join(parts, ", ")
}

// prCount says n pull requests: "1 PR", "3 PRs".
func prCount(n int) string {
	return strconv.Itoa(n) + plural(n, " PR", " PRs")
}

// youLine is the band's first line: how many tasks wait for the Guildmaster.
func youLine(you int) string {
	switch you {
	case 0:
		return "Nothing waits for you"
	case 1:
		return "1 task waits for you"
	}
	return strconv.Itoa(you) + " tasks wait for you"
}

// metaLine is the band's second line, before the read time: how many tasks are open and how many of them wait in a
// merge queue, such as "25 open · 16 in the merge queue"; "No open tasks" when none is.
func metaLine(open, queued int) string {
	if open == 0 {
		return "No open tasks"
	}
	line := strconv.Itoa(open) + " open"
	if queued > 0 {
		line += " · " + strconv.Itoa(queued) + " in the merge queue"
	}
	return line
}
