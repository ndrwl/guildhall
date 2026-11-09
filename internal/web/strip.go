package web

import (
	"math"
	"strconv"
)

// stripView is the task list's stats strip as text, which the page serves and the feed carries: its phone summary
// line, then its three blocks, Work, Spend and Machine, each value one phrase.
type stripView struct {
	// Summary is the phone's one line, such as "3/4 running · 16 to merge · 4 merged · $38.20 today".
	Summary string `json:"summary"`
	// Running is "3 of 4 running", StartQueue "2 queued to start", MergeQueue "16 in the merge queue" with Fronts its
	// fronts' links, and Merged "4 PRs merged today".
	Running    string      `json:"running"`
	StartQueue string      `json:"start_queue"`
	MergeQueue string      `json:"merge_queue"`
	Fronts     []frontLink `json:"fronts"`
	Merged     string      `json:"merged"`
	// Day labels the spend's UTC day, "5 Nov, UTC"; Cost is "$38.20" with CostNote its measure when not measured,
	// Attempts "12 attempts" and Tokens "4.1M tokens".
	Day      string `json:"day"`
	Cost     string `json:"cost"`
	CostNote string `json:"cost_note,omitempty"`
	Attempts string `json:"attempts"`
	Tokens   string `json:"tokens"`
	// Load is "load 1.52", Cores "8 cores", Memory "12.1 GB of 16.0 GB used" and Disk "120 GB of 500 GB free", each
	// "… unavailable" when the OS gives no value.
	Load   string `json:"load"`
	Cores  string `json:"cores"`
	Memory string `json:"memory"`
	Disk   string `json:"disk"`
}

// frontLink is a merge queue's front task, named and linked to its page.
type frontLink struct {
	Name string `json:"name"`
	Href string `json:"href"`
}

// newStripView is st as the strip says it.
func newStripView(st Stats) stripView {
	v := stripView{
		Running:    strconv.Itoa(st.Running) + " running",
		Fronts:     []frontLink{},
		StartQueue: countOr(st.StartQueue, "queued to start"),
		MergeQueue: countOr(st.MergeQueue, "in the merge queue"),
		Merged:     countPRs(st.MergedToday) + " merged today",
		Day:        st.Day.UTC().Format("2 Jan") + ", UTC",
		Attempts:   strconv.Itoa(st.Spend.Attempts) + plural(st.Spend.Attempts, " attempt", " attempts"),
		Tokens:     tokenCount(st.Spend.Tokens) + " tokens",
		Cores:      strconv.Itoa(st.Machine.Cores) + plural(st.Machine.Cores, " core", " cores"),
		Load:       "load unavailable",
		Memory:     "memory unavailable",
		Disk:       "disk unavailable",
	}
	running := strconv.Itoa(st.Running)
	if st.RunningLimit > 0 {
		v.Running = strconv.Itoa(st.Running) + " of " + strconv.Itoa(st.RunningLimit) + " running"
		running += "/" + strconv.Itoa(st.RunningLimit)
	}
	multi := false
	for _, f := range st.Fronts {
		multi = multi || f.Repository != st.Fronts[0].Repository
	}
	for _, f := range st.Fronts {
		name := f.ID
		if multi {
			name = f.Repository + "/" + f.ID
		}
		v.Fronts = append(v.Fronts, frontLink{Name: name, Href: taskHref(f.Repository, f.ID)})
	}
	cost := "$" + strconv.FormatFloat(st.Spend.CostUSD, 'f', 2, 64)
	switch {
	case st.Spend.Attempts > 0 && (st.Spend.CostMeasure == "unknown" || st.Spend.CostMeasure == ""):
		v.Cost, cost = "cost unknown", "spend unknown"
	case st.Spend.CostMeasure == "estimated":
		v.Cost, v.CostNote = cost, "estimated"
		cost += " today"
	default:
		v.Cost = cost
		cost += " today"
	}
	m := st.Machine
	if m.LoadOK {
		v.Load = "load " + strconv.FormatFloat(m.Load, 'f', 2, 64)
	}
	if m.MemOK {
		v.Memory = byteCount(m.MemUsed) + " of " + byteCount(m.MemTotal) + " memory used"
	}
	if m.DiskOK {
		v.Disk = byteCount(m.DiskFree) + " of " + byteCount(m.DiskTotal) + " disk free"
	}
	v.Summary = running + " running · " + strconv.Itoa(st.MergeQueue) + " to merge · " + strconv.Itoa(st.MergedToday) + " merged · " + cost
	return v
}

// countOr is "2 <what>", or "none <what>" for 0.
func countOr(n int, what string) string {
	if n == 0 {
		return "none " + what
	}
	return strconv.Itoa(n) + " " + what
}

// countPRs is "no PR", "1 PR" or "4 PRs".
func countPRs(n int) string {
	if n == 0 {
		return "no PR"
	}
	return prCount(n)
}

// tokenCount is n tokens in short: "512", "8.3k", "830k", "4.1M", "1.2B".
func tokenCount(n int64) string {
	return shortCount(float64(n), []string{"", "k", "M", "B"}, 1000)
}

// byteCount is n bytes in decimal units: "512 B", "830 MB", "12.1 GB", "1.5 TB".
func byteCount(n uint64) string {
	return shortCount(float64(n), []string{" B", " kB", " MB", " GB", " TB", " PB"}, 1000)
}

// shortCount is n in the largest unit of units, each step times larger, that keeps it at least 1: one decimal under
// 100 and none from 100, and none at all in the first unit.
func shortCount(n float64, units []string, step float64) string {
	i := 0
	for math.Round(n) >= step && i < len(units)-1 {
		n /= step
		i++
	}
	if i == 0 || n >= 100 {
		return strconv.FormatFloat(n, 'f', 0, 64) + units[i]
	}
	// 99.96 would round to "100.0"; it reads as "100".
	if s := strconv.FormatFloat(n, 'f', 1, 64); s != "100.0" {
		return s + units[i]
	}
	return "100" + units[i]
}
