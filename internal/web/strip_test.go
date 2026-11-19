package web

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixtureStats is the strip as Layout A of d222's mock-up draws it: 3 of 4 running, 2 queued to start, the merge
// queue's 3 entries with d208 at the front, 4 merged today, $38.20 estimated over 12 attempts and 4.1M tokens on
// 5 Nov, a machine with its load, 8 cores, 12.1 of 16 GB used and 120 of 500 GB free, and the charts' 14 days.
func fixtureStats() Stats {
	return Stats{
		ReadAt: at("2026-11-05 14:52").Add(10 * time.Second), Running: 3, RunningLimit: 4, StartQueue: 2, MergeQueue: 3,
		Fronts: []TaskRef{{Repository: "guildhall", ID: "d208"}}, Day: *at("2026-11-05 00:00"), MergedToday: 4,
		Spend:   Spend{Attempts: 12, Tokens: 4_118_000, CostUSD: 38.2, CostMeasure: "estimated"},
		Machine: Vitals{Load: 1.52, LoadOK: true, Cores: 8, MemUsed: 12_100_000_000, MemTotal: 16_000_000_000, MemOK: true, DiskFree: 120_000_000_000, DiskTotal: 500_000_000_000, DiskOK: true},
		Days:    fixtureDays(),
	}
}

// fixtureDays are the 14 UTC days to 5 Nov 2026: quiet days with no attempt, a day whose attempts reported no cost (31 Oct),
// and today, 5 Nov, with the strip's own 4 merges and $38.20.
func fixtureDays() []DayTotal {
	merges := []int{2, 3, 0, 0, 1, 4, 2, 5, 3, 0, 0, 2, 6, 4}
	costs := []float64{21.4, 30.05, 0, 0, 4.2, 44.9, 18, 52.6, 0, 0, 0, 12.75, 61.3, 38.2}
	out := make([]DayTotal, len(merges))
	for i := range out {
		d := DayTotal{Day: at("2026-10-23 00:00").AddDate(0, 0, i), Merges: merges[i], Spend: Spend{Attempts: 3, CostUSD: costs[i], CostMeasure: "measured"}}
		switch {
		case costs[i] == 0 && i == 8:
			d.Spend.CostMeasure = "unknown"
		case costs[i] == 0:
			d.Spend = Spend{CostMeasure: "unknown"}
		}
		out[i] = d
	}
	return out
}

// stripOf is the stats strip's markup in body.
func stripOf(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `<details class="strip"`)
	if i < 0 {
		t.Fatal("no stats strip")
	}
	j := strings.Index(body[i:], "</details>")
	if j < 0 {
		t.Fatal("the stats strip does not end")
	}
	return body[i : i+j+len("</details>")]
}

// TestStatsStrip: the strip is plain text in the page, above Needs you and the filter, rendered open as a <details>
// whose summary is the phone's one line; Work holds the running count against the cap, the start queue, the merge
// queue linking its front and today's merges, Spend its UTC day, cost, attempts and tokens, and Machine the load,
// cores, memory and disk; testdata/strip.golden.html, which needs no script.
func TestStatsStrip(t *testing.T) {
	s, _ := listServer(fixtureList())
	body := get(t, s, "/").Body.String()
	inOrder(t, body, `<details class="strip" id="stats" open>`, `id="g-you"`, `<form class="fb"`)
	strip := stripOf(t, body)
	for _, want := range []string{
		`<summary>3/4 running · 3 to merge · 4 merged · $38.20 today</summary>`,
		`<b>3 of 4 running</b>`, `<li>2 queued to start</li>`, `<li>3 in the merge queue · front <a href="/guildhall/d208">d208</a></li>`, `<li>4 PRs merged today</li>`,
		`Spend <span class="sd">5 Nov, UTC</span>`, `<b>$38.20</b> <span class="sd">estimated</span>`, `<li>12 attempts</li>`, `<li>4.1M tokens</li>`,
		`<b>load 1.52</b> · 8 cores`, `<li>12.1 GB of 16.0 GB memory used</li>`, `<li>120 GB of 500 GB disk free</li>`,
	} {
		if !strings.Contains(strip, want) {
			t.Errorf("strip lacks %s", want)
		}
	}
	if strings.Contains(strip, "<script") || strings.Contains(strip, "<time") {
		t.Error("the strip holds a script or a time")
	}
	checkGolden(t, "strip.golden.html", strip+"\n")
}

// TestStripCharts: the strip carries the two 14-day charts' section, hidden so that with scripts off it is left
// out, with the days as escaped JSON in data-days for charts.js, whose words for today are "5 Nov · 4 merges · $38";
// the page inlines uPlot and charts.js before list.js; with no days the section is not served at all.
func TestStripCharts(t *testing.T) {
	s, src := listServer(fixtureList())
	body := get(t, s, "/").Body.String()
	strip := stripOf(t, body)
	for _, want := range []string{
		`<section class="stch" id="charts" hidden aria-label="Last 14 days" data-days="[{&#34;day&#34;:&#34;2026-10-23&#34;,&#34;label&#34;:&#34;23 Oct&#34;,&#34;merges&#34;:2,&#34;cost&#34;:21.4,&#34;text&#34;:&#34;23 Oct · 2 merges · $21&#34;}`,
		`&#34;text&#34;:&#34;5 Nov · 4 merges · $38&#34;}]"`,
		`&#34;day&#34;:&#34;2026-10-31&#34;,&#34;label&#34;:&#34;31 Oct&#34;,&#34;merges&#34;:3,&#34;cost&#34;:null,&#34;text&#34;:&#34;31 Oct · 3 merges · spend unknown&#34;`,
		`<div class="chp" id="ch-merges"></div>`, `<div class="chp" id="ch-spend"></div>`, `<p class="chr" aria-live="polite"></p>`,
	} {
		if !strings.Contains(strip, want) {
			t.Errorf("strip lacks %s", want)
		}
	}
	inOrder(t, body, "/*! https://github.com/leeoniya/uPlot (v1.6.32) */", "// charts.js:", "// list.js:", "</head>", `id="charts"`)
	if !strings.Contains(body, "<style>"+string(vendor.Style)+"</style>") {
		t.Error("the task list does not inline uPlot's stylesheet")
	}

	src.stats.Days = nil
	body = get(t, s, "/").Body.String()
	if strings.Contains(stripOf(t, body), `id="charts"`) {
		t.Error("with no days the strip still serves the charts")
	}
}

// TestDayWords: a day's words name its date, its merges, singular for one, and its spend in short: whole dollars
// from $10, cents under them, "$0" for a day with no attempt, and "spend unknown", null in the chart, for a day
// whose attempts reported no cost.
func TestDayWords(t *testing.T) {
	day := *at("2026-11-05 00:00")
	for _, c := range []struct {
		d    DayTotal
		text string
		cost string
	}{
		{DayTotal{Day: day, Merges: 4, Spend: Spend{Attempts: 9, CostUSD: 38.2, CostMeasure: "estimated"}}, "5 Nov · 4 merges · $38", "38.2"},
		{DayTotal{Day: day, Merges: 1, Spend: Spend{Attempts: 1, CostUSD: 9.996, CostMeasure: "measured"}}, "5 Nov · 1 merge · $10", "9.996"},
		{DayTotal{Day: day, Spend: Spend{Attempts: 1, CostUSD: 4.2, CostMeasure: "measured"}}, "5 Nov · 0 merges · $4.20", "4.2"},
		{DayTotal{Day: day, Spend: Spend{CostMeasure: "unknown"}}, "5 Nov · 0 merges · $0", "0"},
		{DayTotal{Day: day, Merges: 2, Spend: Spend{Attempts: 2, CostMeasure: "unknown"}}, "5 Nov · 2 merges · spend unknown", "null"},
	} {
		v := newDayViews([]DayTotal{c.d})[0]
		cost := "null"
		if v.Cost != nil {
			cost = strconv.FormatFloat(*v.Cost, 'f', -1, 64)
		}
		if v.Text != c.text || cost != c.cost || v.Day != "2026-11-05" || v.Label != "5 Nov" {
			t.Errorf("%+v: %q, cost %s; want %q, cost %s", c.d, v.Text, cost, c.text, c.cost)
		}
	}
	if v := newStripView(Stats{}); v.Days == nil || len(v.Days) != 0 || v.DaysJSON != "" {
		t.Errorf("no days: %#v, %q", v.Days, v.DaysJSON)
	}
}

// TestStatsStripWords: no cap reads "3 running"; an empty queue and no merge say so; one attempt and one PR are
// singular; an unknown cost is "cost unknown" on the strip and "spend unknown" on the phone's line; a measured cost
// has no note; and a vital the OS does not give reads unavailable.
func TestStatsStripWords(t *testing.T) {
	st := Stats{Running: 3, MergedToday: 1, Day: *at("2026-01-04 00:00"), Spend: Spend{Attempts: 1, CostMeasure: "unknown"}, Machine: Vitals{Cores: 1}}
	v := newStripView(st)
	for _, c := range [][2]string{
		{v.Running, "3 running"}, {v.StartQueue, "none queued to start"}, {v.MergeQueue, "none in the merge queue"}, {v.Merged, "1 PR merged today"},
		{v.Day, "4 Jan, UTC"}, {v.Cost, "cost unknown"}, {v.Attempts, "1 attempt"}, {v.Tokens, "0 tokens"}, {v.Cores, "1 core"},
		{v.Load, "load unavailable"}, {v.Memory, "memory unavailable"}, {v.Disk, "disk unavailable"},
		{v.Summary, "3 running · 0 to merge · 1 merged · spend unknown"},
	} {
		if c[0] != c[1] {
			t.Errorf("%q, want %q", c[0], c[1])
		}
	}
	if v.CostNote != "" || len(v.Fronts) != 0 {
		t.Errorf("note %q, fronts %v", v.CostNote, v.Fronts)
	}
	if v := newStripView(Stats{Spend: Spend{CostMeasure: "unknown"}}); v.Cost != "$0.00" || v.Merged != "no PR merged today" {
		t.Errorf("no attempts: cost %q, merged %q", v.Cost, v.Merged)
	}
	if v := newStripView(Stats{Spend: Spend{Attempts: 2, CostUSD: 1.5, CostMeasure: "measured"}}); v.Cost != "$1.50" || v.CostNote != "" || v.Summary != "0 running · 0 to merge · 0 merged · $1.50 today" {
		t.Errorf("measured: cost %q, note %q", v.Cost, v.CostNote)
	}
	two := newStripView(Stats{MergeQueue: 2, Fronts: []TaskRef{{Repository: "app", ID: "a1"}, {Repository: "lib", ID: "l1"}}})
	if len(two.Fronts) != 2 || two.Fronts[0].Name != "app/a1" || two.Fronts[1].Href != "/lib/l1" {
		t.Errorf("two fronts = %+v", two.Fronts)
	}
}

// TestShortCounts pins the token and byte counts' rounding.
func TestShortCounts(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{{0, "0"}, {512, "512"}, {999, "999"}, {1000, "1.0k"}, {8_340, "8.3k"}, {830_000, "830k"}, {999_600, "1.0M"}, {99_960, "100k"}, {4_118_000, "4.1M"}, {1_200_000_000, "1.2B"}, {5_000_000_000_000, "5000B"}} {
		if got := tokenCount(c.n); got != c.want {
			t.Errorf("tokenCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	for _, c := range []struct {
		n    uint64
		want string
	}{{512, "512 B"}, {830_000_000, "830 MB"}, {12_100_000_000, "12.1 GB"}, {1_500_000_000_000, "1.5 TB"}} {
		if got := byteCount(c.n); got != c.want {
			t.Errorf("byteCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
