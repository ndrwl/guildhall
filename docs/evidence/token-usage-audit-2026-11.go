//go:build ignore

// Command token-usage-audit-2026-11 prints the tables quoted by docs/audits/token-usage-2026-11.md.
//
// It is a one-off, read-only analysis: it opens the Director copy's store with
// store.Open(..., ReadOnly: true, RequireStore: true), copies what it needs into memory and closes the store
// before reading any file, so the live Director's single-writer lock is never waited on for long. It then reads
// each attempt's usage.json (only when the store has no spend, as guildhall spend report does), prompt/manifest.json,
// prompt/prompt.md and claude-settings.json from the state directory, the profile table of the copy's config.toml,
// and the per-message usage of the Director's Claude Code transcripts. It prints totals only: no local paths,
// session ids, URLs or prompt and transcript text.
//
//	go run docs/evidence/token-usage-audit-2026-11.go -copy <director copy> -transcripts <dir>[,<dir>...] > docs/evidence/token-usage-audit-2026-11.txt
//
// With -refusals it reads only the store, the same read-only way, and lists each review intake refusal with its
// stored reason (task d192) in place of the tables:
//
//	go run docs/evidence/token-usage-audit-2026-11.go -copy <director copy> -refusals > docs/evidence/token-usage-audit-2026-11-review-refusals.txt
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

type attempt struct {
	Repo, Task, Step, Base string
	Slice, Number          int
	Outcome, Rejection     string
	Director               bool
	Spend                  store.Spend
	Start, Finish          time.Time
	Profile, Role, Guide   string
	Standards, Inputs      int
	PromptBytes            int
	Sections               map[string]int
	Runtime, Model, Effort string
	TaskState              string
	Retry                  bool
	ToolCalls, ToolErrors  int
	ToolResultBytes        int64
	HasWorkbench           bool
}

func (a *attempt) known() bool  { return a.Spend.Tokens != store.SpendUnknown && a.Spend.Tokens != "" }
func (a *attempt) costed() bool { return a.Spend.Cost != store.SpendUnknown && a.Spend.Cost != "" }
func (a *attempt) tokens() int64 {
	s := a.Spend
	return s.InputTokens + s.OutputTokens + s.CacheCreationInputTokens + s.CacheReadInputTokens
}
func (a *attempt) id() string { return fmt.Sprintf("%s/%s/%s-%d", a.Repo, a.Task, a.Step, a.Number) }

var knownSections = map[string]bool{
	"Why this is a retry": true, "Role": true, "Stage guide": true, "Repository policies": true, "Inputs": true,
	"Standards": true, "Skills": true, "Required outputs": true, "Result template": true, "Instructions": true,
	"Step instructions": true, "Design": true, "Feedback": true, "Slice": true, "Slices": true,
}

func main() {
	copyDir := flag.String("copy", "", "Director copy directory")
	transcripts := flag.String("transcripts", "", "comma-separated Claude Code project directories of the Director's sessions")
	days := flag.Int("days", 14, "days of per-day figures")
	refusals := flag.Bool("refusals", false, "list review intake refusals with their reasons instead of the tables")
	flag.Parse()
	if *copyDir == "" {
		fmt.Fprintln(os.Stderr, "usage: -copy <dir> [-transcripts <dir,...>] [-refusals]")
		os.Exit(2)
	}
	ran := time.Now().UTC()
	if *refusals {
		opened := time.Now()
		atts, _ := readStore(filepath.Join(*copyDir, "director.store"))
		reviewRefusals(atts, ran, time.Since(opened), *copyDir)
		return
	}
	cfg := readConfig(filepath.Join(*copyDir, "config.toml"))
	opened := time.Now()
	atts, tasks := readStore(filepath.Join(*copyDir, "director.store"))
	held := time.Since(opened)
	state := filepath.Join(*copyDir, "state")
	for _, a := range atts {
		readFiles(state, a, cfg)
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	p := func(f string, v ...any) { fmt.Fprintf(out, f, v...) }

	p("# token-usage-audit-2026-11 evidence\n\n")
	p("ran_at: %s\nstore_read_only_held: %s\n", ran.Format(time.RFC3339), held.Round(time.Millisecond))
	var first, last time.Time
	for _, a := range atts {
		if a.Start.IsZero() {
			continue
		}
		if first.IsZero() || a.Start.Before(first) {
			first = a.Start
		}
		if a.Start.After(last) {
			last = a.Start
		}
	}
	p("attempts: %d (first start %s, last start %s)\ntasks: %d\n\n", len(atts), first.Format(time.RFC3339), last.Format(time.RFC3339), len(tasks))

	section(out, "Profiles in config.toml today (model and effort are inferred from these)")
	tw := table(out, "PROFILE", "ADAPTER", "MODEL", "EFFORT")
	var names []string
	for n := range cfg.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		pr := cfg.Profiles[n]
		row(tw, n, pr.Adapter, orDefault(pr.Model, "adapter default"), cfg.EffectiveEffort(pr.Effort))
	}
	if d := cfg.Director; d != nil {
		row(tw, "(director session)", orDefault(d.Adapter, "claude"), orDefault(d.Model, "adapter default"), cfg.EffectiveEffort(d.Effort))
	}
	tw.Flush()

	// Totals and reconciliation.
	section(out, "Totals (all attempts in the store)")
	tot := sum(atts)
	writeSum(out, tot)

	// Price fit.
	section(out, "Prices implied by Claude Code's total_cost_usd per attempt ($ per million tokens)")
	var claude []*attempt
	for _, a := range atts {
		if a.Runtime == "claude" && a.known() && a.costed() && a.Spend.CostUSD > 0 {
			claude = append(claude, a)
		}
	}
	// Implied input rate per attempt under a fixed Anthropic price ratio (input : output : cache write : cache read).
	ratios := []struct {
		name string
		r    [4]float64
	}{{"1 : 5 : 1.25 : 0.1 (5-minute cache writes)", [4]float64{1, 5, 1.25, 0.1}}, {"1 : 5 : 2 : 0.1 (1-hour cache writes)", [4]float64{1, 5, 2, 0.1}}}
	rate := func(a *attempt, r [4]float64) float64 {
		return a.Spend.CostUSD / priced(a.Spend, r)
	}
	for _, r := range ratios {
		var v []float64
		for _, a := range claude {
			v = append(v, rate(a, r.r))
		}
		sort.Float64s(v)
		p("implied input $/MTok at ratio %s: p05 %.3f  p25 %.3f  median %.3f  p75 %.3f  p95 %.3f\n", r.name, q(v, .05), q(v, .25), q(v, .5), q(v, .75), q(v, .95))
	}
	ratio := ratios[1].r
	var rates []float64
	for _, a := range claude {
		rates = append(rates, rate(a, ratio))
	}
	sort.Float64s(rates)
	med := q(rates, .5)
	var consistent, under []*attempt
	var underCost float64
	for _, a := range claude {
		switch x := rate(a, ratio) / med; {
		case x > 1.5:
			under = append(under, a)
			underCost += a.Spend.CostUSD
		case x > 0.8 && x < 1.25:
			consistent = append(consistent, a)
		}
	}
	p("attempts within -20%%/+25%% of the median rate: %d of %d\n", len(consistent), len(claude))
	p("attempts whose cost is over 1.5x what their recorded tokens price at (tokens under-recorded): %d, cost $%.2f (%.1f%% of Claude cost)\n", len(under), underCost, 100*underCost/sum(claude).cost)
	byStepUnder := map[string]int{}
	for _, a := range under {
		byStepUnder[a.Base]++
	}
	p("under-recorded attempts by step: %s\n", counts(byStepUnder))
	var wCost, wTok float64
	for _, a := range claude {
		if rate(a, ratio)/med <= 1.5 {
			wCost += a.Spend.CostUSD
			wTok += priced(a.Spend, ratio)
		}
	}
	r := wCost / wTok
	price := [4]float64{r * ratio[0], r * ratio[1], r * ratio[2], r * ratio[3]}
	p("price table used below (cost-weighted rate at the 1-hour ratio over attempts that are not under-recorded): input %.3f  output %.3f  cache_creation %.3f  cache_read %.3f\n", price[0], price[1], price[2], price[3])
	var resid []float64
	var predTot, costTot float64
	for _, a := range claude {
		pr := priced(a.Spend, price)
		predTot += pr
		costTot += a.Spend.CostUSD
		if a.Spend.CostUSD > 0.01 {
			resid = append(resid, (pr-a.Spend.CostUSD)/a.Spend.CostUSD)
		}
	}
	sort.Float64s(resid)
	p("per-attempt relative residual: p05 %+.4f  median %+.4f  p95 %+.4f  min %+.4f  max %+.4f\n", q(resid, .05), q(resid, .5), q(resid, .95), resid[0], resid[len(resid)-1])
	p("price table on summed tokens: $%.2f; summed cost_usd: $%.2f; gap %+.2f (%+.3f%%)\n", predTot, costTot, predTot-costTot, 100*(predTot-costTot)/costTot)
	var predOK, costOK float64
	for _, a := range claude {
		if rate(a, ratio)/med <= 1.5 {
			predOK += priced(a.Spend, price)
			costOK += a.Spend.CostUSD
		}
	}
	p("same, without the under-recorded attempts: $%.2f vs $%.2f (%+.2f%%)\n", predOK, costOK, 100*(predOK-costOK)/costOK)
	for _, ref := range []struct {
		name string
		p    [4]float64
	}{
		{"ratio 1:5:1.25:0.1 at $5 input", [4]float64{5, 25, 6.25, 0.5}},
		{"ratio 1:5:1.25:0.1 at $3 input", [4]float64{3, 15, 3.75, 0.3}},
	} {
		var t float64
		for _, a := range claude {
			t += priced(a.Spend, ref.p)
		}
		p("reference %s on summed tokens: $%.2f (%+.1f%% vs cost_usd)\n", ref.name, t, 100*(t-costTot)/costTot)
	}
	var share [4]float64
	for _, a := range claude {
		s := a.Spend
		share[0] += float64(s.InputTokens) * price[0] / 1e6
		share[1] += float64(s.OutputTokens) * price[1] / 1e6
		share[2] += float64(s.CacheCreationInputTokens) * price[2] / 1e6
		share[3] += float64(s.CacheReadInputTokens) * price[3] / 1e6
	}
	p("dollar share by token type (price table): input %.1f%%  output %.1f%%  cache_creation %.1f%%  cache_read %.1f%%\n",
		100*share[0]/predTot, 100*share[1]/predTot, 100*share[2]/predTot, 100*share[3]/predTot)

	// Cuts.
	cuts := []struct {
		title string
		key   func(*attempt) string
	}{
		{"By step (slice suffix removed)", func(a *attempt) string { return a.Base }},
		{"By profile", func(a *attempt) string { return orDefault(a.Profile, "unavailable") }},
		{"By role", func(a *attempt) string { return orDefault(a.Role, "unavailable") }},
		{"By runtime (claude-settings.json present = claude; Director stage = director; else the profile's adapter)", func(a *attempt) string { return a.Runtime }},
		{"By inferred model and effort (today's profile)", func(a *attempt) string { return a.Model + " / " + a.Effort }},
		{"By repository", func(a *attempt) string { return a.Repo }},
		{"By outcome", func(a *attempt) string { return outcome(a) }},
		{"By task state", func(a *attempt) string { return a.TaskState }},
		{"By attempt number", func(a *attempt) string {
			if a.Number >= 4 {
				return "4+"
			}
			return fmt.Sprint(a.Number)
		}},
	}
	for _, c := range cuts {
		section(out, c.title)
		cut(out, atts, c.key, tot.cost)
	}

	section(out, fmt.Sprintf("By day (UTC start receipt), last %d days", *days))
	end := last.Truncate(24 * time.Hour)
	startDay := end.AddDate(0, 0, -(*days - 1))
	tw = table(out, "DAY", "ATTEMPTS", "UNKNOWN", "COST_USD", "TOKENS", "OUTPUT", "CACHE_CREATE", "CACHE_READ")
	byDay := map[string][]*attempt{}
	noStart := 0
	for _, a := range atts {
		if a.Start.IsZero() {
			noStart++
			continue
		}
		byDay[a.Start.Format("2006-01-02")] = append(byDay[a.Start.Format("2006-01-02")], a)
	}
	var recent []float64
	for d := startDay; !d.After(end); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		s := sum(byDay[k])
		recent = append(recent, s.cost)
		row(tw, k, s.n, s.unknown, money(s.cost), s.tokens, s.sp.OutputTokens, s.sp.CacheCreationInputTokens, s.sp.CacheReadInputTokens)
	}
	tw.Flush()
	var earlier []*attempt
	for k, v := range byDay {
		if k < startDay.Format("2006-01-02") {
			earlier = append(earlier, v...)
		}
	}
	e := sum(earlier)
	p("before %s: %d attempts, $%.2f; without a start receipt: %d\n", startDay.Format("2006-01-02"), e.n, e.cost, noStart)
	last7 := recent[len(recent)-7:]
	p("mean $/day over the last 7 days: %.2f; last 14 days: %.2f\n", mean(last7), mean(recent))

	// Distribution.
	section(out, "Cost per attempt (attempts with a known cost)")
	var costs []float64
	var ranked []*attempt
	for _, a := range atts {
		if a.costed() && a.Runtime != "director" {
			costs = append(costs, a.Spend.CostUSD)
			ranked = append(ranked, a)
		}
	}
	sort.Float64s(costs)
	p("n %d  mean %.3f  median %.3f  p90 %.3f  p99 %.3f  max %.3f\n", len(costs), mean(costs), q(costs, .5), q(costs, .9), q(costs, .99), costs[len(costs)-1])
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].Spend.CostUSD > ranked[j].Spend.CostUSD })
	top := 0.0
	tw = table(out, "RANK", "ATTEMPT", "OUTCOME", "COST_USD", "TOKENS", "OUTPUT", "CACHE_CREATE", "CACHE_READ")
	for i, a := range ranked[:min(10, len(ranked))] {
		top += a.Spend.CostUSD
		row(tw, i+1, a.Task+"/"+a.Step+"-"+fmt.Sprint(a.Number), outcome(a), money(a.Spend.CostUSD), a.tokens(), a.Spend.OutputTokens, a.Spend.CacheCreationInputTokens, a.Spend.CacheReadInputTokens)
	}
	tw.Flush()
	p("top 10 share of total cost: %.1f%%\n", 100*top/tot.cost)
	section(out, "Cost per attempt by step: median and p90")
	tw = table(out, "STEP", "N", "MEDIAN", "P90", "MAX", "MEDIAN_OUTPUT", "MEDIAN_CACHE_READ")
	for _, k := range keysBy(atts, func(a *attempt) string { return a.Base }, tot.cost) {
		var c, o, r []float64
		for _, a := range atts {
			if a.Base == k && a.costed() {
				c = append(c, a.Spend.CostUSD)
				o = append(o, float64(a.Spend.OutputTokens))
				r = append(r, float64(a.Spend.CacheReadInputTokens))
			}
		}
		if len(c) == 0 {
			continue
		}
		sort.Float64s(c)
		sort.Float64s(o)
		sort.Float64s(r)
		row(tw, k, len(c), money(q(c, .5)), money(q(c, .9)), money(c[len(c)-1]), int(q(o, .5)), int(q(r, .5)))
	}
	tw.Flush()

	// Per task and slice.
	section(out, "Per task and per delivered slice")
	byTask := map[string][]*attempt{}
	for _, a := range atts {
		byTask[a.Repo+"/"+a.Task] = append(byTask[a.Repo+"/"+a.Task], a)
	}
	var doneCosts []float64
	var doneSpend, cancelledSpend, openSpend float64
	stateCount := map[string]int{}
	for _, t := range tasks {
		as := byTask[t.Repository+"/"+t.ID]
		s := sum(as)
		stateCount[string(t.State)]++
		switch t.State {
		case store.TaskDone:
			doneCosts = append(doneCosts, s.cost)
			doneSpend += s.cost
		case store.TaskCancelled:
			cancelledSpend += s.cost
		default:
			openSpend += s.cost
		}
	}
	sort.Float64s(doneCosts)
	p("tasks: done %d, open %d, cancelled %d\n", stateCount["done"], stateCount["open"], stateCount["cancelled"])
	p("spend on done tasks $%.2f, open $%.2f, cancelled $%.2f\n", doneSpend, openSpend, cancelledSpend)
	p("cost per done task: mean %.2f  median %.2f  p90 %.2f  max %.2f\n", mean(doneCosts), q(doneCosts, .5), q(doneCosts, .9), doneCosts[len(doneCosts)-1])
	var sliceCounts []float64
	doneSlices, doneWithBuild := 0, 0
	var doneBuildCosts []float64
	for _, t := range tasks {
		mx := 0
		built := false
		for _, a := range byTask[t.Repository+"/"+t.ID] {
			if a.Base == "build" {
				built = true
				mx = max(mx, a.Slice)
			}
		}
		if t.State == store.TaskDone && built {
			sliceCounts = append(sliceCounts, float64(mx))
			doneSlices += mx
			doneWithBuild++
			doneBuildCosts = append(doneBuildCosts, sum(byTask[t.Repository+"/"+t.ID]).cost)
		}
	}
	sort.Float64s(sliceCounts)
	sort.Float64s(doneBuildCosts)
	var doneBuildSpend float64
	for _, c := range doneBuildCosts {
		doneBuildSpend += c
	}
	p("done tasks with a build: %d, slices built %d (mean %.2f, max %.0f per task)\n", doneWithBuild, doneSlices, mean(sliceCounts), sliceCounts[len(sliceCounts)-1])
	p("cost per done task with a build: mean %.2f  median %.2f  p90 %.2f; per delivered slice $%.2f\n", mean(doneBuildCosts), q(doneBuildCosts, .5), q(doneBuildCosts, .9), doneBuildSpend/float64(doneSlices))
	p("all spend (including open, cancelled and no-build tasks) per delivered slice: $%.2f\n", tot.cost/float64(doneSlices))

	section(out, "Tool calls per attempt by step (workbench.jsonl; attempts that have one)")
	tw = table(out, "STEP", "N", "MEDIAN_CALLS", "P90_CALLS", "MEDIAN_RESULT_KB", "ERROR_SHARE", "MEDIAN_$/CALL")
	for _, k := range keysBy(atts, func(a *attempt) string { return a.Base }, 0) {
		var calls, kb, per []float64
		var errs, all int
		for _, a := range atts {
			if a.Base != k || !a.HasWorkbench || !a.costed() {
				continue
			}
			calls = append(calls, float64(a.ToolCalls))
			kb = append(kb, float64(a.ToolResultBytes)/1024)
			errs += a.ToolErrors
			all += a.ToolCalls
			if a.ToolCalls > 0 {
				per = append(per, a.Spend.CostUSD/float64(a.ToolCalls))
			}
		}
		if len(calls) == 0 {
			continue
		}
		sort.Float64s(calls)
		sort.Float64s(kb)
		sort.Float64s(per)
		row(tw, k, len(calls), int(q(calls, .5)), int(q(calls, .9)), int(q(kb, .5)), fmt.Sprintf("%.1f%%", 100*float64(errs)/float64(max(all, 1))), fmt.Sprintf("%.3f", q(per, .5)))
	}
	tw.Flush()
	var bwCalls, bwCost float64
	var bwN int
	for _, a := range atts {
		if a.HasWorkbench && a.costed() && a.Runtime == "claude" {
			bwN++
			bwCalls += float64(a.ToolCalls)
			bwCost += a.Spend.CostUSD
		}
	}
	p("attempts with a workbench log: %d; tool calls %d; cost $%.2f; mean $/tool call %.3f\n", bwN, int(bwCalls), bwCost, bwCost/math.Max(bwCalls, 1))
	var bwRead, bwCreate, bwOut float64
	for _, a := range atts {
		if a.HasWorkbench && a.costed() && a.Runtime == "claude" {
			bwRead += float64(a.Spend.CacheReadInputTokens)
			bwCreate += float64(a.Spend.CacheCreationInputTokens)
			bwOut += float64(a.Spend.OutputTokens)
		}
	}
	p("same attempts: mean tool calls %.1f, cache read per tool call %.0f tokens (about the context each API call re-reads), cache creation per attempt %.0f, output per attempt %.0f\n",
		bwCalls/float64(max(bwN, 1)), bwRead/math.Max(bwCalls, 1), bwCreate/float64(max(bwN, 1)), bwOut/float64(max(bwN, 1)))

	// Cache.
	section(out, "Cache behaviour by step (measured Claude attempts)")
	tw = table(out, "STEP", "N", "CACHE_CREATE", "CACHE_READ", "READ/CREATE", "CREATE_SHARE_OF_INPUT", "MEDIAN_CREATE", "MIN_CREATE")
	for _, k := range keysBy(claude, func(a *attempt) string { return a.Base }, 0) {
		var cc, cr, in int64
		var per []float64
		n := 0
		for _, a := range claude {
			if a.Base != k {
				continue
			}
			n++
			cc += a.Spend.CacheCreationInputTokens
			cr += a.Spend.CacheReadInputTokens
			in += a.Spend.InputTokens + a.Spend.CacheCreationInputTokens + a.Spend.CacheReadInputTokens
			per = append(per, float64(a.Spend.CacheCreationInputTokens))
		}
		sort.Float64s(per)
		row(tw, k, n, cc, cr, fmt.Sprintf("%.1f", float64(cr)/float64(max(cc, 1))), fmt.Sprintf("%.1f%%", 100*float64(cc)/float64(max(in, 1))), int(q(per, .5)), int(per[0]))
	}
	tw.Flush()
	var ccAll, shortCC, crShort []float64
	for _, a := range claude {
		ccAll = append(ccAll, float64(a.Spend.CacheCreationInputTokens))
		if a.Spend.OutputTokens < 3000 {
			shortCC = append(shortCC, float64(a.Spend.CacheCreationInputTokens))
			crShort = append(crShort, float64(a.Spend.CacheReadInputTokens))
		}
	}
	sort.Float64s(ccAll)
	sort.Float64s(shortCC)
	sort.Float64s(crShort)
	p("cache creation per attempt: min %d  p05 %d  p10 %d  median %d  p90 %d\n", int(ccAll[0]), int(q(ccAll, .05)), int(q(ccAll, .1)), int(q(ccAll, .5)), int(q(ccAll, .9)))
	if len(shortCC) > 0 {
		p("short attempts (output < 3000 tokens, n=%d): cache creation min %d  median %d; cache read min %d  median %d\n", len(shortCC), int(shortCC[0]), int(q(shortCC, .5)), int(crShort[0]), int(q(crShort, .5)))
	}
	var input float64
	for _, a := range claude {
		input += float64(a.Spend.InputTokens + a.Spend.CacheCreationInputTokens + a.Spend.CacheReadInputTokens)
	}
	p("fixed-overhead model: every measured Claude attempt (n=%d) pays its fixed context once as cache creation; at 10k / 20k / 30k fixed tokens that is $%.2f / $%.2f / $%.2f over the store at the price table's cache-creation price\n",
		len(claude), float64(len(claude))*10e3*price[2]/1e6, float64(len(claude))*20e3*price[2]/1e6, float64(len(claude))*30e3*price[2]/1e6)
	p("each fixed token is also re-read on every later API call: cache read / cache creation over all Claude attempts = %.1f\n", float64(tot.sp.CacheReadInputTokens)/float64(tot.sp.CacheCreationInputTokens))

	// Prompt composition.
	section(out, "Rendered prompt size by step (prompt/prompt.md; median bytes per section, ~4 bytes per token)")
	secNames := []string{"(preamble)", "Why this is a retry", "Role", "Stage guide", "Repository policies", "Inputs", "Standards", "Skills", "Required outputs", "Result template", "other"}
	hdr := append([]string{"STEP", "N", "TOTAL_BYTES", "~TOKENS"}, secNames...)
	tw = table(out, hdr...)
	for _, k := range keysBy(atts, func(a *attempt) string { return a.Base }, 0) {
		var tot []float64
		per := map[string][]float64{}
		for _, a := range atts {
			if a.Base != k || a.PromptBytes == 0 {
				continue
			}
			tot = append(tot, float64(a.PromptBytes))
			for _, s := range secNames {
				per[s] = append(per[s], float64(a.Sections[s]))
			}
		}
		if len(tot) == 0 {
			continue
		}
		sort.Float64s(tot)
		cells := []any{k, len(tot), int(q(tot, .5)), int(q(tot, .5) / 4)}
		for _, s := range secNames {
			v := per[s]
			sort.Float64s(v)
			cells = append(cells, int(q(v, .5)))
		}
		row(tw, cells...)
	}
	tw.Flush()
	var withStd, retries int
	for _, a := range atts {
		if a.Standards > 0 {
			withStd++
		}
		if a.Retry {
			retries++
		}
	}
	p("attempts whose manifest lists standards: %d; prompts with a retry section: %d\n", withStd, retries)

	// Waste.
	section(out, "Wasted spend")
	type bucket struct {
		n, unknown int
		cost       float64
	}
	w := map[string]*bucket{}
	add := func(k string, a *attempt) {
		b := w[k]
		if b == nil {
			b = &bucket{}
			w[k] = b
		}
		b.n++
		if !a.costed() {
			b.unknown++
		} else {
			b.cost += a.Spend.CostUSD
		}
	}
	lastOf := map[string]int{}
	for _, a := range atts {
		k := a.Repo + "/" + a.Task + "/" + a.Step
		lastOf[k] = max(lastOf[k], a.Number)
	}
	byKey := map[string]*attempt{}
	for _, a := range atts {
		byKey[fmt.Sprintf("%s/%s/%s/%d", a.Repo, a.Task, a.Step, a.Number)] = a
	}
	for _, a := range atts {
		k := a.Repo + "/" + a.Task + "/" + a.Step
		switch {
		case a.Rejection != "":
			add("A refused at intake", a)
		case a.Outcome == "failed":
			add("A failed", a)
		case a.Outcome == "stopped":
			add("A stopped", a)
		case a.Outcome == "succeeded" && a.Number < lastOf[k]:
			add("B succeeded, then the step ran again", a)
		}
		if prev := byKey[fmt.Sprintf("%s/%d", k, a.Number-1)]; prev != nil {
			if prev.Rejection != "" || prev.Outcome != "succeeded" {
				add("C rerun after a failed, stopped or refused attempt", a)
			} else {
				add("D rerun after a succeeded attempt (send-back round)", a)
			}
		}
		if a.TaskState == "cancelled" {
			add("E any attempt of a cancelled task", a)
		}
		if a.Outcome == "" {
			add("F still running (excluded)", a)
		}
	}
	tw = table(out, "CATEGORY", "ATTEMPTS", "COST_UNKNOWN", "COST_USD", "SHARE")
	for _, k := range []string{"A refused at intake", "A failed", "A stopped", "B succeeded, then the step ran again", "C rerun after a failed, stopped or refused attempt", "D rerun after a succeeded attempt (send-back round)", "E any attempt of a cancelled task", "F still running (excluded)"} {
		b := w[k]
		if b == nil {
			b = &bucket{}
		}
		row(tw, k, b.n, b.unknown, money(b.cost), fmt.Sprintf("%.1f%%", 100*b.cost/tot.cost))
	}
	tw.Flush()
	thrown := 0.0
	for _, k := range []string{"A refused at intake", "A failed", "A stopped"} {
		if w[k] != nil {
			thrown += w[k].cost
		}
	}
	p("A rows (work thrown away outright, no overlap): $%.2f (%.1f%% of all spend)\n", thrown, 100*thrown/tot.cost)
	sendBack := map[string]float64{}
	for _, a := range atts {
		prev := byKey[fmt.Sprintf("%s/%s/%s/%d", a.Repo, a.Task, a.Step, a.Number-1)]
		if prev != nil && prev.Rejection == "" && prev.Outcome == "succeeded" {
			sendBack[a.Base] += a.Spend.CostUSD
		}
	}
	p("D by step:")
	for _, k := range []string{"build", "review", "plan", "research", "designer"} {
		p(" %s $%.2f;", k, sendBack[k])
	}
	p("\n")
	var later float64
	for _, a := range atts {
		if a.Number > 1 && a.costed() {
			later += a.Spend.CostUSD
		}
	}
	p("spend on attempt numbers above 1: $%.2f (%.1f%%)\n", later, 100*later/tot.cost)
	rej := map[string]int{}
	for _, a := range atts {
		if a.Rejection != "" {
			rej[a.Base]++
		}
	}
	p("intake refusals by step: %s\n", counts(rej))

	section(out, "How often each loop fires: attempts per (task, slice) for each step")
	tw = table(out, "STEP", "INSTANCES", "1", "2", "3", "4+", "MEAN", "EXTRA_ATTEMPT_COST")
	for _, k := range keysBy(atts, func(a *attempt) string { return a.Base }, tot.cost) {
		dist := [4]int{}
		var n, total int
		var extra float64
		for key, mx := range lastOf {
			parts := strings.SplitN(key, "/", 3)
			base, _ := workflow.ParseSliceStep(parts[2])
			if base != k {
				continue
			}
			n++
			total += mx
			dist[min(mx, 4)-1]++
		}
		for _, a := range atts {
			if a.Base == k && a.Number > 1 && a.costed() {
				extra += a.Spend.CostUSD
			}
		}
		if n == 0 {
			continue
		}
		row(tw, k, n, dist[0], dist[1], dist[2], dist[3], fmt.Sprintf("%.2f", float64(total)/float64(n)), money(extra))
	}
	tw.Flush()
	p("example: d165 plan attempts:")
	for _, a := range atts {
		if a.Task == "d165" && a.Base == "plan" {
			p(" plan-%d %s $%.2f;", a.Number, outcome(a), a.Spend.CostUSD)
		}
	}
	p("\n")

	// Director transcripts.
	if *transcripts != "" {
		director(out, strings.Split(*transcripts, ","), price, startDay, end)
	}
}

// The audit window's last start, and the merges that moved review's intake checks into the session (UTC).
var (
	auditLastStart = time.Date(2026, 11, 3, 20, 58, 32, 0, time.UTC)
	reviewCheckAt  = time.Date(2026, 10, 30, 7, 22, 0, 0, time.UTC) // #191 guildhall review check
	resultSubmitAt = time.Date(2026, 11, 4, 2, 44, 0, 0, time.UTC)  // #306 guildhall result check and submit
	submitPromptAt = time.Date(2026, 11, 4, 8, 21, 0, 0, time.UTC)  // #320 prompts require result submit
)

// reviewRefusals lists every attempt of a review or review--s<n> step that intake refused, with its stored reason.
// Rebase-review steps are left out, as the audit counts them apart. Local paths in a reason are masked.
func reviewRefusals(atts []*attempt, ran time.Time, held time.Duration, copyDir string) {
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	fmt.Fprintf(out, "# token-usage-audit-2026-11 review intake refusals\n\n")
	fmt.Fprintf(out, "ran_at: %s\nstore_read_only_held: %s\n", ran.Format(time.RFC3339), held.Round(time.Millisecond))
	fmt.Fprintf(out, "audit window: starts up to %s\n", auditLastStart.Format(time.RFC3339))
	var rs []*attempt
	for _, a := range atts {
		if a.Base == "review" && strings.TrimSpace(a.Rejection) != "" {
			rs = append(rs, a)
		}
	}
	sort.Slice(rs, func(i, j int) bool {
		if !rs[i].Start.Equal(rs[j].Start) {
			return rs[i].Start.Before(rs[j].Start)
		}
		return rs[i].id() < rs[j].id()
	})
	pairs := []string{}
	if abs, err := filepath.Abs(copyDir); err == nil {
		pairs = append(pairs, abs, "<copy>")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		pairs = append(pairs, home, "~")
	}
	mask := strings.NewReplacer(pairs...)
	groups := []struct {
		title string
		in    func(time.Time) bool
	}{
		{"In the audit window", func(t time.Time) bool { return !t.After(auditLastStart) }},
		{"After the audit window, before #320 (2026-11-04T08:21:00Z)", func(t time.Time) bool {
			return t.After(auditLastStart) && t.Before(submitPromptAt)
		}},
		{"Since #320 (started at or after 2026-11-04T08:21:00Z)", func(t time.Time) bool { return !t.Before(submitPromptAt) }},
	}
	for _, g := range groups {
		section(out, g.title)
		n := 0
		tw := table(out, "TASK", "STEP", "ATTEMPT", "STARTED_AT", "TOOLS_AT_START")
		var reasons []string
		for _, a := range rs {
			if !g.in(a.Start) {
				continue
			}
			n++
			row(tw, a.Repo+"/"+a.Task, a.Step, a.Number, a.Start.Format(time.RFC3339), toolsAt(a.Start))
			reasons = append(reasons, fmt.Sprintf("%s: %q", a.id(), mask.Replace(a.Rejection)))
		}
		tw.Flush()
		fmt.Fprintf(out, "refusals: %d\nreasons, as stored:\n", n)
		for _, r := range reasons {
			fmt.Fprintf(out, "- %s\n", r)
		}
	}
	section(out, "Rows for the audit doc (family and verdict are a first reading; check each by hand)")
	fmt.Fprintf(out, "| Attempt | Started (UTC) | Tools at start | Reason (as stored) | Family | Verdict |\n|---|---|---|---|---|---|\n")
	cell := strings.NewReplacer("|", `\|`, "`", "'")
	for _, a := range rs {
		fam, caught := refusalFamily(a.Rejection)
		if !a.Start.Before(submitPromptAt) {
			caught = "still live"
		}
		fmt.Fprintf(out, "| %s | %s | %s | `%s` | %s | %s |\n", a.id(), a.Start.Format("2006-01-02 15:04"), toolsAt(a.Start),
			cell.Replace(strings.Join(strings.Fields(mask.Replace(a.Rejection)), " ")), fam, caught)
	}
}

// refusalFamily names the family of a review refusal reason (docs/audits/token-usage-2026-11.md section 10) and
// which in-session check now catches it. A reason it does not know is "still live" until read by hand.
func refusalFamily(reason string) (family, caught string) {
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(reason, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("unacknowledged steering"):
		return "steering", "caught by #306+#320"
	case has("recorded no revisions"):
		return "binding (engine)", "still live"
	case has("is stale"):
		return "binding", "caught by #306+#320"
	case has("previous finding"):
		return "coverage", "caught by #191"
	case has("malformed artisan result", "for another task attempt", "lists no output", "output path", "read output"):
		return "generic output or identity", "caught by #306+#320"
	case has("review outcome", "summary", "verdict", "finding", "disposition", "subject", "code_head", "document_digest",
		"spec_digest", "standards_revision", " id "):
		return "outcome form", "caught by #191"
	}
	return "unknown", "still live"
}

// toolsAt names the in-session checks that had merged when an attempt started.
func toolsAt(t time.Time) string {
	switch {
	case t.IsZero():
		return "unknown"
	case t.Before(reviewCheckAt):
		return "before #191"
	case t.Before(resultSubmitAt):
		return "#191"
	case t.Before(submitPromptAt):
		return "#191+#306"
	}
	return "#191+#306+#320"
}

type summary struct {
	n, unknown, tokensUnknown int
	cost                      float64
	tokens                    int64
	sp                        store.Spend
	measured, estimated       float64
}

func sum(as []*attempt) summary {
	var s summary
	for _, a := range as {
		s.n++
		if !a.known() {
			s.tokensUnknown++
		} else {
			s.sp.InputTokens += a.Spend.InputTokens
			s.sp.OutputTokens += a.Spend.OutputTokens
			s.sp.CacheCreationInputTokens += a.Spend.CacheCreationInputTokens
			s.sp.CacheReadInputTokens += a.Spend.CacheReadInputTokens
			s.tokens += a.tokens()
		}
		if !a.costed() {
			s.unknown++
		} else {
			s.cost += a.Spend.CostUSD
			if a.Spend.Cost == store.SpendMeasured {
				s.measured += a.Spend.CostUSD
			} else {
				s.estimated += a.Spend.CostUSD
			}
		}
	}
	return s
}

func writeSum(out *bufio.Writer, s summary) {
	fmt.Fprintf(out, "attempts %d; cost known %d, cost unknown %d; tokens unknown %d\n", s.n, s.n-s.unknown, s.unknown, s.tokensUnknown)
	fmt.Fprintf(out, "cost_usd %.6f (measured %.6f, estimated %.6f)\n", s.cost, s.measured, s.estimated)
	fmt.Fprintf(out, "tokens %d: input %d, output %d, cache_creation %d, cache_read %d\n", s.tokens, s.sp.InputTokens, s.sp.OutputTokens, s.sp.CacheCreationInputTokens, s.sp.CacheReadInputTokens)
}

func cut(out *bufio.Writer, as []*attempt, key func(*attempt) string, total float64) {
	tw := table(out, "KEY", "ATTEMPTS", "COST_UNKNOWN", "COST_USD", "SHARE", "$/ATTEMPT", "TOKENS", "OUTPUT", "CACHE_CREATE", "CACHE_READ")
	for _, k := range keysBy(as, key, total) {
		var g []*attempt
		for _, a := range as {
			if key(a) == k {
				g = append(g, a)
			}
		}
		s := sum(g)
		per := 0.0
		if s.n-s.unknown > 0 {
			per = s.cost / float64(s.n-s.unknown)
		}
		row(tw, k, s.n, s.unknown, money(s.cost), fmt.Sprintf("%.1f%%", 100*s.cost/total), money(per), s.tokens, s.sp.OutputTokens, s.sp.CacheCreationInputTokens, s.sp.CacheReadInputTokens)
	}
	tw.Flush()
}

// keysBy returns the distinct keys, most expensive first.
func keysBy(as []*attempt, key func(*attempt) string, _ float64) []string {
	c := map[string]float64{}
	n := map[string]int{}
	for _, a := range as {
		c[key(a)] += a.Spend.CostUSD
		n[key(a)]++
	}
	var ks []string
	for k := range n {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		if c[ks[i]] != c[ks[j]] {
			return c[ks[i]] > c[ks[j]]
		}
		return ks[i] < ks[j]
	})
	return ks
}

func outcome(a *attempt) string {
	switch {
	case a.Rejection != "":
		return "refused"
	case a.Outcome == "":
		return "running"
	}
	return a.Outcome
}

func readConfig(path string) *config.Config {
	c, err := config.Load(path)
	if err != nil {
		fail(fmt.Errorf("config: %w", err))
	}
	return c
}

func readStore(path string) ([]*attempt, []store.Task) {
	s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		fail(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			fail(err)
		}
	}()
	receipts, err := s.Receipts()
	if err != nil {
		fail(err)
	}
	starts, finishes := map[string]time.Time{}, map[string]time.Time{}
	for _, r := range receipts {
		k := r.Repository + "/" + r.TaskID + "/" + r.Record
		switch r.Operation {
		case "start-attempt", "start-owned-attempt":
			starts[k] = r.CommittedAt
		case "finish-attempt":
			finishes[k] = r.CommittedAt
		}
	}
	repos, err := s.Repositories()
	if err != nil {
		fail(err)
	}
	var out []*attempt
	var all []store.Task
	for _, repo := range repos {
		tasks, err := s.Tasks(repo)
		if err != nil {
			fail(err)
		}
		all = append(all, tasks...)
		for _, t := range tasks {
			ats, err := s.Attempts(repo, t.ID)
			if err != nil {
				fail(err)
			}
			for _, at := range ats {
				base, slice := workflow.ParseSliceStep(at.Step)
				a := &attempt{Repo: repo, Task: t.ID, Step: at.Step, Base: base, Slice: slice, Number: at.Number,
					Outcome: string(at.Outcome), Rejection: at.IntakeRejection, Director: at.Director, TaskState: string(t.State)}
				if at.Spend != nil {
					a.Spend = *at.Spend
				}
				rec := fmt.Sprintf("%s/%s/attempt/%s/%d", repo, t.ID, at.Step, at.Number)
				a.Start, a.Finish = starts[rec], finishes[rec]
				out = append(out, a)
			}
		}
	}
	return out, all
}

func readFiles(state string, a *attempt, cfg *config.Config) {
	dir := (artisan.AttemptKey{Repository: a.Repo, TaskID: a.Task, Step: a.Step, Attempt: a.Number}).Dir(state)
	if a.Spend.Tokens == "" {
		u, err := adapter.ReadUsage(filepath.Join(dir, artisan.ResultFile))
		if err != nil {
			fail(fmt.Errorf("usage %s: %w", a.id(), err))
		}
		a.Spend = store.Spend{Tokens: store.Measure(u.Tokens), InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
			CacheCreationInputTokens: u.CacheCreationInputTokens, CacheReadInputTokens: u.CacheReadInputTokens, Cost: store.Measure(u.Cost), CostUSD: u.CostUSD}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "prompt", render.ManifestFile)); err == nil {
		var m render.Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			fail(fmt.Errorf("manifest %s: %w", a.id(), err))
		}
		a.Profile, a.Role, a.Guide = m.Identity.Profile, m.Role.Path, m.Guide.Path
		a.Standards, a.Inputs = len(m.Standards), len(m.Inputs)
	} else if !errors.Is(err, fs.ErrNotExist) {
		fail(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "prompt", "prompt.md")); err == nil {
		a.PromptBytes = len(data)
		a.Sections = map[string]int{}
		cur := "(preamble)"
		for _, line := range strings.SplitAfter(string(data), "\n") {
			if h, ok := strings.CutPrefix(line, "## "); ok {
				h = strings.TrimSpace(h)
				switch {
				case knownSections[h]:
					cur = h
				case cur == "(preamble)":
					cur = "other"
				}
			}
			if cur == "Why this is a retry" {
				a.Retry = true
			}
			if _, ok := map[string]bool{"(preamble)": true, "Why this is a retry": true, "Role": true, "Stage guide": true, "Repository policies": true, "Inputs": true, "Standards": true, "Skills": true, "Required outputs": true, "Result template": true}[cur]; ok {
				a.Sections[cur] += len(line)
			} else {
				a.Sections["other"] += len(line)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "workbench.jsonl")); err == nil {
		a.HasWorkbench = true
		for _, line := range strings.Split(string(data), "\n") {
			var ev struct {
				Kind  string `json:"kind"`
				Error bool   `json:"error"`
				Bytes int64  `json:"bytes"`
			}
			if json.Unmarshal([]byte(line), &ev) != nil {
				continue
			}
			switch ev.Kind {
			case "tool_call":
				a.ToolCalls++
			case "tool_result":
				a.ToolResultBytes += ev.Bytes
				if ev.Error {
					a.ToolErrors++
				}
			}
		}
	}
	pr, ok := cfg.Profiles[a.Profile]
	a.Model, a.Effort = "unavailable", "unavailable"
	if ok {
		a.Model = orDefault(pr.Model, "adapter default")
		a.Effort = cfg.EffectiveEffort(pr.Effort)
	}
	switch {
	case a.Director:
		a.Runtime = "director"
		a.Model, a.Effort = "director session", "-"
	case exists(filepath.Join(dir, "claude-settings.json")):
		a.Runtime = "claude"
	case ok && pr.Adapter != "":
		a.Runtime = pr.Adapter
	case a.Profile == "":
		a.Runtime = "director"
		a.Model, a.Effort = "director session", "-"
	default:
		a.Runtime = "unknown"
	}
}

// director reads the Director's Claude Code transcripts: one usage per API message id, the last line of each.
func director(out *bufio.Writer, dirs []string, price [4]float64, startDay, end time.Time) {
	type msg struct {
		at              time.Time
		session, model  string
		in, out, cc, cr int64
		cc1h            int64
		sidechain       bool
	}
	msgs := map[string]*msg{}
	wakes := map[string]int{}
	files := 0
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(path string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			files++
			f, err := os.Open(path)
			if err != nil {
				fail(err)
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 64<<20)
			for sc.Scan() {
				var rec struct {
					Type        string    `json:"type"`
					Timestamp   time.Time `json:"timestamp"`
					SessionID   string    `json:"sessionId"`
					IsSidechain bool      `json:"isSidechain"`
					Message     struct {
						ID      string          `json:"id"`
						Model   string          `json:"model"`
						Role    string          `json:"role"`
						Content json.RawMessage `json:"content"`
						Usage   *struct {
							In            int64 `json:"input_tokens"`
							Out           int64 `json:"output_tokens"`
							CC            int64 `json:"cache_creation_input_tokens"`
							CR            int64 `json:"cache_read_input_tokens"`
							CacheCreation struct {
								H1 int64 `json:"ephemeral_1h_input_tokens"`
							} `json:"cache_creation"`
						} `json:"usage"`
					} `json:"message"`
				}
				if json.Unmarshal(sc.Bytes(), &rec) != nil {
					continue
				}
				if rec.Type == "user" && !rec.IsSidechain && len(rec.Message.Content) > 0 && rec.Message.Content[0] == '"' {
					wakes[rec.Timestamp.UTC().Format("2006-01-02")]++
				}
				if rec.Type != "assistant" || rec.Message.Usage == nil || rec.Message.ID == "" || rec.Message.Model == "<synthetic>" {
					continue
				}
				u := rec.Message.Usage
				msgs[rec.Message.ID] = &msg{rec.Timestamp.UTC(), rec.SessionID, rec.Message.Model, u.In, u.Out, u.CC, u.CR, u.CacheCreation.H1, rec.IsSidechain}
			}
			if err := sc.Err(); err != nil {
				fail(err)
			}
			return nil
		})
	}
	section(out, "The Director's own session (read from Claude Code transcripts, not measured by Guildhall)")
	fmt.Fprintf(out, "transcript files %d; API messages %d\n", files, len(msgs))
	var list []*msg
	for _, m := range msgs {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	models := map[string]int{}
	for _, m := range list {
		models[m.model]++
	}
	fmt.Fprintf(out, "models (API messages): %s\n", counts(models))
	cost := func(m *msg) float64 {
		return priced(store.Spend{InputTokens: m.in, OutputTokens: m.out, CacheCreationInputTokens: m.cc, CacheReadInputTokens: m.cr}, price)
	}
	tw := table(out, "DAY", "API_CALLS", "WAKES", "SIDECHAIN_CALLS", "INPUT", "OUTPUT", "CACHE_CREATE", "CACHE_READ", "CC_1H", "EST_USD")
	type agg struct {
		n, side             int
		in, o, cc, cr, cc1h int64
		usd                 float64
	}
	days := map[string]*agg{}
	var all agg
	for _, m := range list {
		k := m.at.Format("2006-01-02")
		g := days[k]
		if g == nil {
			g = &agg{}
			days[k] = g
		}
		for _, x := range []*agg{g, &all} {
			x.n++
			if m.sidechain {
				x.side++
			}
			x.in += m.in
			x.o += m.out
			x.cc += m.cc
			x.cr += m.cr
			x.cc1h += m.cc1h
			x.usd += cost(m)
		}
	}
	var recent []float64
	for d := startDay; !d.After(end); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		g := days[k]
		if g == nil {
			g = &agg{}
		}
		recent = append(recent, g.usd)
		row(tw, k, g.n, wakes[k], g.side, g.in, g.o, g.cc, g.cr, g.cc1h, money(g.usd))
	}
	row(tw, "ALL", all.n, "", all.side, all.in, all.o, all.cc, all.cr, all.cc1h, money(all.usd))
	tw.Flush()
	fmt.Fprintf(out, "estimated $ uses the price table above (an inference: the Director and Artisans run the same default model)\n")
	fmt.Fprintf(out, "mean est $/day over the last 7 days: %.2f; last 14 days: %.2f\n", mean(recent[len(recent)-7:]), mean(recent))
	if all.n > 0 {
		fmt.Fprintf(out, "per API call: mean est $%.4f; mean cache read %d; mean cache creation %d; mean output %d\n", all.usd/float64(all.n), all.cr/int64(all.n), all.cc/int64(all.n), all.o/int64(all.n))
	}
	// Cache rebuilds: the first main-thread call of a session after a gap since that session's previous call.
	prev := map[string]time.Time{}
	type gapAgg struct {
		n      int
		cc, cr int64
		usd    float64
	}
	gaps := map[string]*gapAgg{}
	order := []string{"< 5m", "5m-1h", ">= 1h", "session start"}
	for _, k := range order {
		gaps[k] = &gapAgg{}
	}
	for _, m := range list {
		if m.sidechain {
			continue
		}
		p, ok := prev[m.session]
		prev[m.session] = m.at
		var k string
		switch {
		case !ok:
			k = "session start"
		case m.at.Sub(p) < 5*time.Minute:
			k = "< 5m"
		case m.at.Sub(p) < time.Hour:
			k = "5m-1h"
		default:
			k = ">= 1h"
		}
		g := gaps[k]
		g.n++
		g.cc += m.cc
		g.cr += m.cr
		g.usd += cost(m)
	}
	fmt.Fprintf(out, "main-thread API calls by gap since the session's previous call:\n")
	tw = table(out, "GAP", "CALLS", "CACHE_CREATE", "CACHE_READ", "MEAN_CREATE", "EST_USD")
	for _, k := range order {
		g := gaps[k]
		mc := int64(0)
		if g.n > 0 {
			mc = g.cc / int64(g.n)
		}
		row(tw, k, g.n, g.cc, g.cr, mc, money(g.usd))
	}
	tw.Flush()
}

func priced(s store.Spend, p [4]float64) float64 {
	return (float64(s.InputTokens)*p[0] + float64(s.OutputTokens)*p[1] + float64(s.CacheCreationInputTokens)*p[2] + float64(s.CacheReadInputTokens)*p[3]) / 1e6
}

func q(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	i := p * float64(len(v)-1)
	lo := int(math.Floor(i))
	hi := int(math.Ceil(i))
	return v[lo] + (v[hi]-v[lo])*(i-float64(lo))
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	t := 0.0
	for _, x := range v {
		t += x
	}
	return t / float64(len(v))
}

func counts(m map[string]int) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		if m[ks[i]] != m[ks[j]] {
			return m[ks[i]] > m[ks[j]]
		}
		return ks[i] < ks[j]
	})
	var parts []string
	for _, k := range ks {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func section(out *bufio.Writer, title string) { fmt.Fprintf(out, "\n## %s\n\n", title) }

func table(out *bufio.Writer, cols ...string) *tabwriter.Writer {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(cols, "\t"))
	return tw
}

func row(tw *tabwriter.Writer, cells ...any) {
	s := make([]string, len(cells))
	for i, c := range cells {
		s[i] = fmt.Sprint(c)
	}
	fmt.Fprintln(tw, strings.Join(s, "\t"))
}

func money(v float64) string { return fmt.Sprintf("%.2f", v) }

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "token-usage-audit:", err)
	os.Exit(1)
}
