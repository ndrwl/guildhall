# Token usage audit, November 2026

Where Guildhall's tokens and dollars go, what can be cut, and which spend metrics Guildhall should start recording.
Task d170. Every figure comes from [`docs/evidence/token-usage-audit-2026-11.txt`](../evidence/token-usage-audit-2026-11.txt), the saved output of [`docs/evidence/token-usage-audit-2026-11.go`](../evidence/token-usage-audit-2026-11.go), unless it is marked otherwise.

**Headlines**

- The store holds **1,296 attempts** on 171 tasks, from 2026-10-23 to 2026-11-03. Their recorded cost is **$1,414.59**, all estimated. 162 attempts have an unknown cost.
- Over the last 7 days, Artisans spent **$190.92 a day** (estimated). On 2026-11-03 the figure was $223.01 across 193 attempts. The task detail's "about 110 attempts and $143" was an earlier reading of the same day.
- The Director's own session, which Guildhall does not meter, adds about **$42.43 a day** over the same 7 days. This is inferred from its transcripts.
- **Builds take 59.6% of the cost.** Review and research take about 15% each.
- **Cache reads are 96.6% of all tokens and about 53% of the dollars.** Every API call re-reads the whole context. Cost therefore grows with the number of calls times the context size, not with the prompt Guildhall renders: that prompt is only about 1–2k tokens.
- **The token counts are incomplete.** 66 attempts (23.1% of the cost) cost far more than their recorded tokens would price at. The recorded tokens price at 21.6% less than the summed `cost_usd`.
- **Plain wins come to about $31 a day**, about 13% of the $233 a day Artisans and Director spend together. Most of it comes from carrying less fixed context in every Artisan call. The trade-offs are listed separately and are not added to that total.

## 1. Scope and method

**Window.** Totals and rankings cover every attempt in the store (Q2 option c), from 2026-10-23T20:30:10Z to 2026-11-03T20:58:32Z. Per-day figures cover the 14 UTC days ending 2026-11-03. The store has no attempts before 2026-10-23, so the first two of those days are empty. "Per day" savings are measured against the **last 7 days' mean** ($190.92 Artisan, $42.43 Director). That mean includes two quiet days (2026-11-01 and 2026-11-02) and the busiest day (2026-10-31, $425.33).

**Data sources** (all read-only, through the task grant g1):

| Source | What it gives | How it was read |
|---|---|---|
| The copy's store | Attempts, outcomes, intake refusals, spend, task states, start receipts | `store.Open(..., ReadOnly: true, RequireStore: true)`. The handle was held for 61 ms, then closed before any file was read. |
| The attempt state directory | `usage.json` (only where the store has no spend, as `spend report` does), `prompt/manifest.json`, `prompt/prompt.md`, `claude-settings.json`, `workbench.jsonl` | Plain file reads |
| The copy's `config.toml` | Profiles: adapter, model and effort | `config.Load` |
| The Director's Claude Code transcripts (two project directories, 39 files) | Per-API-message `usage` and model | One record per message id (the last line for each id). Totals only. |

**Measures.** Measured, estimated and unknown amounts are kept apart, as [`docs/spend.md`](../spend.md) does.

- **Tokens** in the store are *measured*: Claude Code reports them.
- **Dollars** in the store are *estimated*: Claude Code's `total_cost_usd`, not a bill ([attempt spend record](../decisions/attempt-spend.md)). Measured cost is $0.00.
- **Unknown:** 162 attempts have no cost. 135 of them are grill stages, which run inside the Director's session. The other 27 are Artisan attempts that were stopped (a stopped Claude session prints no result message) or are still running. **No Codex attempts are recorded**, so Codex judge runs add no unknowns yet.
- **Director figures** are *inferred from transcripts*. They are not measured by Guildhall.

**Inferences, marked as such wherever they are used:**

- **Model.** Every profile in today's `config.toml` sets no model, so each runs the adapter's default. Every profile runs at effort `medium`, except designer at `high`. The Director's transcripts name `claude-opus-5-5` on all 6,005 API messages. This build attempt (a builder Artisan) also runs `claude-opus-5-5`. So the report assumes every Artisan attempt in the window ran `claude-opus-5-5`. The profiles may have changed during the window. The store does not record the model ([section 8](#8-proposed-spend-reporting-tickets), R1).
- **Price table.** The build sandbox could not fetch Anthropic's published price list. The audit therefore takes the prices Claude Code itself applied. For each attempt it divides `cost_usd` by its tokens weighted at a fixed ratio of input 1 : output 5 : cache write 2 : cache read 0.1, which is the 1-hour cache-write ratio. That ratio fits better than the 5-minute ratio (1.25): the interquartile spread of the implied rate is ±5% against ±8%. The Director's transcripts also show every cache write as a 1-hour write. The cost-weighted rate over the attempts whose tokens look complete gives this table, **dated 2026-11-03, implied, $ per million tokens**:

  | Input | Output | Cache write | Cache read |
  |---|---|---|---|
  | 2.961 | 14.804 | 5.922 | 0.296 |

  The table prices the Director's transcripts and the token-type shares. Every dollar figure for the store itself is Claude Code's own `cost_usd`.
- **Approximate tokens from bytes.** Prompt and skill sizes are converted at about 4 bytes per token.

**Cross-check with `spend report`.**

- The script's total is **$1,414.593350** over 1,296 attempts. It was run at 2026-11-03T20:59:40Z.
- `guildhall spend report --copy <copy> --json`, run straight after, gave **$1,414.5933498** over 1,296 attempts. The two **agree to the cent**, and their token totals are identical.
- Pricing the summed tokens with the table above gives $1,109.14, which is **$305.45 (21.6%) less** than the summed `cost_usd`. Outside the 66 under-recorded attempts the gap is 0.00% (this is how the rate was chosen). Inside them it is the whole gap: see [section 3](#3-cache-behaviour).

## 2. Where the spend goes

All dollars are estimated `cost_usd`. "Unknown" counts the attempts with no cost in that row.

**By step** (slice suffix removed; the rebase steps grouped here as one row):

| Step | Attempts | Unknown | $ | Share | $/attempt | Tokens (M) | Output (M) | Cache read (M) |
|---|---|---|---|---|---|---|---|---|
| build | 367 | 18 | 843.08 | 59.6% | 2.42 | 1,355.1 | 6.70 | 1,326.0 |
| review | 334 | 2 | 206.42 | 14.6% | 0.62 | 259.0 | 1.80 | 243.7 |
| research | 148 | 1 | 193.52 | 13.7% | 1.32 | 266.8 | 2.18 | 254.3 |
| plan | 190 | 5 | 84.63 | 6.0% | 0.46 | 70.0 | 1.34 | 63.0 |
| hallmark | 7 | 0 | 27.58 | 1.9% | 3.94 | 17.1 | 0.16 | 16.5 |
| designer | 9 | 0 | 20.93 | 1.5% | 2.33 | 26.8 | 0.44 | 25.5 |
| rebase and rebase-review (all) | 106 | 1 | 38.45 | 2.7% | 0.37 | 54.1 | 0.40 | 51.1 |
| grill (Director stage) | 135 | 135 | unknown | – | – | – | – | – |

**By profile:**

| Profile | $ | Share |
|---|---|---|
| builder | 843.08 | 59.6% |
| reviewer (review and rebase-review) | 224.09 | 15.8% |
| researcher (research and hallmark) | 221.09 | 15.6% |
| planner | 84.63 | 6.0% |
| designer | 20.93 | 1.5% |
| rebaser | 20.77 | 1.5% |

**Other cuts:**

- **By role:** `roles/artisan.md` covers all of the $1,414.59. `roles/director.md` (grill) is entirely unknown (135 attempts).
- **By runtime:** claude, $1,414.59 over 1,161 attempts (27 unknown). The Director runs 135 attempts, all unknown. Codex has no attempts.
- **By model and effort** (inferred): adapter default at `medium`, $1,393.67 (98.5%). Adapter default at `high` (designer), $20.93.
- **By repository:** guildhall, 100%.
- **By outcome:** succeeded $1,378.78, failed $25.54, refused at intake $10.27. Stopped (28) and running (5) attempts are unknown.

**By day** (Artisan attempts by UTC start receipt; the Director column is from [section 6](#6-the-directors-own-session)):

| Day | Attempts | Unknown | Artisan $ | Artisan tokens (M) | Artisan cache read (M) | Director $ (inferred) |
|---|---|---|---|---|---|---|
| 2026-10-23 | 1 | 1 | 0.00 | 0.0 | 0.0 | 0.64 |
| 2026-10-24 | 2 | 0 | 1.33 | 1.2 | 1.1 | 0.56 |
| 2026-10-25 | 2 | 0 | 2.58 | 4.4 | 4.2 | 1.40 |
| 2026-10-26 | 8 | 1 | 3.72 | 4.8 | 4.5 | 3.37 |
| 2026-10-27 | 67 | 13 | 70.56 | 62.0 | 58.9 | 22.66 |
| 2026-10-28 | 147 | 23 | 140.29 | 218.6 | 211.4 | 30.70 |
| 2026-10-29 | 189 | 16 | 231.20 | 436.8 | 424.5 | 42.66 |
| 2026-10-30 | 294 | 31 | 301.20 | 451.4 | 435.5 | 76.07 |
| 2026-10-31 | 375 | 48 | 425.33 | 555.7 | 537.1 | 95.28 |
| 2026-11-01 | 18 | 1 | 15.38 | 28.5 | 27.5 | 7.57 |
| 2026-11-02 | 0 | 0 | 0.00 | 0.0 | 0.0 | 0.00 |
| 2026-11-03 | 193 | 28 | 223.01 | 285.5 | 275.4 | 44.71 |

**Cost per attempt** (1,134 attempts with a known cost): mean $1.25, median $0.59, p90 $2.40, p99 $10.22, maximum $25.62.

| Step | Median | p90 | Max |
|---|---|---|---|
| build | 0.98 | 7.02 | 25.62 |
| review | 0.55 | 0.97 | 5.37 |
| research | 1.19 | 1.80 | 6.52 |
| plan | 0.43 | 0.64 | 1.50 |
| hallmark | 4.50 | 5.70 | 6.25 |

**Top 10 attempts** (11.3% of all cost; all are builds):

| Rank | Attempt | $ | Recorded tokens | Recorded output |
|---|---|---|---|---|
| 1 | d87/build-1 | 25.62 | 521,827 | 258 |
| 2 | d88/build-1 | 24.63 | 84,394,052 | 195,173 |
| 3 | d23/build-5 | 20.10 | 47,122,122 | 147,987 |
| 4 | d63/build-1 | 14.82 | 381,188 | 112 |
| 5 | d122/build-1 | 14.72 | 2,376,721 | 2,344 |
| 6 | d124/build--s2-1 | 13.70 | 1,124,231 | 2,388 |
| 7 | d96/build-1 | 13.04 | 376,413 | 311 |
| 8 | d127/build--s2-1 | 11.76 | 949,094 | 1,707 |
| 9 | d25/build-1 | 11.36 | 31,020,453 | 133,286 |
| 10 | d74/build-1 | 10.63 | 32,442,205 | 92,001 |

Six of the ten (ranks 1 and 4–8) cost $11–26 but recorded under 2.4M tokens and almost no output. Their tokens are under-recorded (section 3).

**Per task and per slice:**

- 130 tasks are done, 21 open and 20 cancelled.
- A done task cost a mean of $9.72 (median $6.93, p90 $18.65, maximum $46.63).
- The 123 done tasks that built anything built 137 slices, 1.11 per task. That is **$9.02 per delivered slice** from done-task spend alone, or $10.33 if all spend is divided by delivered slices.

## 3. Cache behaviour

**Token mix:**

- Of the 2.049 billion recorded tokens, 1.980 billion (96.6%) are cache reads, 55.8M are cache writes, 13.0M are output and 52k are uncached input.
- At the price table, dollars split as **cache read 52.9%, cache write 29.8%, output 17.4%** and input 0.0%.

**Cache read against cache write, by step:**

| Step | Cache write (M) | Cache read (M) | Read/write | Median write per attempt |
|---|---|---|---|---|
| build | 22.35 | 1,326.03 | 59.3 | 36,230 |
| review | 13.46 | 243.71 | 18.1 | 36,453 |
| research | 10.36 | 254.27 | 24.5 | 68,429 |
| plan | 5.66 | 63.00 | 11.1 | 28,918 |
| rebase steps (all eight, slice-suffixed included) | 0.02–0.72 each | 0.5–15.4 each | 15–25 | 16–33k |

- Across all Claude attempts, each token written to the cache is read back 35.5 times.
- The 138 recent attempts that have a `workbench.jsonl` made a mean of **39.5 tool calls**. They read about **43,230 cached tokens per tool call**, which is roughly the context each API call re-sends.
- A median build makes 68 tool calls (p90 174) and gets back 114 KB of tool results.

**Fixed per-attempt overhead.**

- Cache writes per attempt: minimum 352, p05 9,299, p10 16,230, median 34,596.
- Short attempts (under 3,000 output tokens, n=173) have a median of **15,241** cache-write tokens and at least 48,602 cache-read tokens.
- The minimum of 352 shows that Claude Code's system prompt and tool prefix is often already cached from an earlier session, so the floor is a lower bound. What a short attempt reads and writes is mostly fixed context:

| Part | Size | Measure |
|---|---|---|
| Guildhall's rendered prompt (role, stage guide, policies, inputs by path, outputs, template) | 3.4–7.6 KB, about 0.9–1.9k tokens (table in [section 4](#4-prompt-composition)) | Measured |
| Claude Code's own system prompt and built-in tool schemas | About 10–13k tokens: the short-attempt floor minus the prompt and the parts below | Estimated |
| Of which: orchestration tools an Artisan never needs (`Workflow`, `ScheduleWakeup`, `ReportFindings`, `ListAgents`) | About 23 KB, about 6k tokens | Estimated from this attempt's own context |
| Inherited user-level hooks (three SessionStart hooks; Claude Code keeps a 2 KB preview of the largest), one claude.ai MCP server's instructions and tool schemas, and the user skill and agent listings | About 9 KB, about 2.25k tokens | Estimated from this attempt's own context |

- A fixed token costs about **$17.6 per million per attempt**. It is written once ($5.922 per million) and then read on each of about 39.5 calls ($0.296 per million each).
- So every 1,000 fixed tokens cost about $0.018 per attempt, or **$2.70 a day** at the 153 costed Artisan attempts a day of the last 7 days.

**Under-recorded tokens.**

- **66 attempts** (46 build, 11 review, 5 hallmark, 4 research) cost more than 1.5 times what their recorded tokens price at. Together they cost $326.32, **23.1% of all Claude cost**.
- d87/build-1 is the clearest case: $25.62 against 258 output tokens and 0.52M cache reads.
- The other 1,064 of 1,134 attempts lie within −20%/+25% of the median rate.
- So `usage` in Claude Code's result message sometimes covers only part of the session. Possible reasons are context compaction, sub-agents, helper-model calls, or a usage figure that resets mid-session. `total_cost_usd` still covers the whole session.
- **Token figures in this report undercount spend by about a fifth. The dollar figures do not.** Ticket R1 fixes this.

**Cache rebuilds.**

- Artisans: every attempt is a fresh session, so the retry and send-back attempts in [section 5](#5-wasted-spend) pay for their context again.
- Director: its cache writes are all 1-hour writes. Only 13 main-thread calls came after a gap of an hour or more, at $5.95 in total ([section 6](#6-the-directors-own-session)). **Cache expiry between wakes costs almost nothing.**

## 4. Prompt composition

**Rendered prompt** (`prompt/prompt.md`), median bytes per section:

| Step | Total bytes | ~Tokens | Role | Stage guide | Policies | Inputs | Required outputs | Result template |
|---|---|---|---|---|---|---|---|---|
| build | 4,308 | 1,077 | 742 | 892 | 341 | 357 | 783 | 621 |
| review | 7,363 | 1,840 | 742 | 1,212 | 341 | 480 | 3,444 | 666 |
| research | 3,423 | 855 | 742 | 421 | 341 | 18 | 420 | 664 |
| plan | 4,273 | 1,068 | 742 | 664 | 341 | 362 | 418 | 659 |
| hallmark | 4,220 | 1,055 | 742 | 776 | 341 | 374 | 422 | 666 |
| designer | 5,001 | 1,250 | 742 | 628 | 341 | 619 | 422 | 666 |
| rebase-review | 7,487–7,580 | about 1,880 | 742 | 1,212 | 341 | about 510 | 3,444 | about 680 |
| grill (Director) | 6,397 | 1,599 | 3,733 | 879 | 341 | 18 | – | – |

- No attempt's manifest lists standards, so none are inlined. Inputs are passed by path. 53 prompts carry a retry section.
- **The rendered prompt is 3–5% of a median attempt's cache writes and is not where the money goes.** Review's 3.4 KB outputs section is the largest single part.

**Director role and skills** (bytes in the repository today; brings research §2 up to date):

| File | Bytes | ~Tokens |
|---|---|---|
| `skills/guildhall-director-tasks/SKILL.md` | 26,144 | 6,500 |
| `skills/guildhall-director-inbox/SKILL.md` | 11,792 | 2,900 |
| `baseline/roles/director.md` | 11,133 | 2,800 |
| `skills/guildhall-hallmark/SKILL.md` | 9,021 | 2,300 |
| `skills/guildhall-review/SKILL.md` | 8,804 | 2,200 |
| `skills/guildhall-self-review/SKILL.md` | 8,468 | 2,100 |
| `skills/guildhall-maintenance/SKILL.md` | 8,447 | 2,100 |
| `skills/guildhall-director-startup/SKILL.md` | 6,704 | 1,700 |
| All roles, skills and stage guides | 142,712 | 35,700 |

Once a skill is loaded, it is re-read on every later API call of that Director session (section 6).

## 5. Wasted spend

| Category | Attempts | Unknown | $ | Share |
|---|---|---|---|---|
| A. Refused at intake | 16 | 0 | 10.27 | 0.7% |
| A. Failed | 33 | 6 | 25.54 | 1.8% |
| A. Stopped | 28 | 28 | unknown | – |
| **A total: thrown away outright** | 77 | 34 | **35.81** | **2.5%** |
| B. Succeeded, then the same step ran again | 402 | 3 | 706.88 | 50.0% |
| C. Rerun after a failed, stopped or refused attempt | 66 | 9 | 80.29 | 5.7% |
| D. Rerun after a succeeded attempt (a send-back round) | 402 | 12 | 247.17 | 17.5% |
| E. Any attempt of a cancelled task | 42 | 15 | 33.41 | 2.4% |

**How to read the table:**

- A is waste in the plain sense.
- B is not waste. Its attempts include the reviews whose findings caused the next round, and the builds whose branch the next round kept working on.
- C and D are the price of the loops. D splits as build $123.94, review $87.51, plan $19.68 and designer $10.42 (design rounds).
- Altogether, attempts numbered above 1 cost **$327.47 (23.1%)**.
- The 28 stopped attempts spent tokens that nobody recorded, so A undercounts (ticket R7).
- Intake refusals: review 10 and plan 6. For example, d165 plan-1 was refused ($0.34) and its rerun, plan-2, succeeded ($0.20). d166 targets that cause.

**How often each loop fires** (attempts per task and slice):

| Step | Instances | 1 | 2 | 3 | 4+ | Mean | Cost of attempts above 1 |
|---|---|---|---|---|---|---|---|
| build | 156 | 71 | 34 | 19 | 32 | 2.35 | 188.49 |
| review | 151 | 70 | 36 | 22 | 23 | 2.21 | 93.44 |
| plan | 144 | 115 | 19 | 6 | 4 | 1.32 | 24.80 |
| research | 144 | 140 | 4 | 0 | 0 | 1.03 | 4.70 |
| designer | 6 | 4 | 1 | 1 | 0 | 1.50 | 10.42 |
| validate-rebase | 23 | 17 | 6 | 0 | 0 | 1.26 | 2.16 |
| grill | 129 | 124 | 4 | 1 | 0 | 1.05 | unknown |

- More than half of build/review instances go round at least once.
- 32 build instances ran four or more times. Retries after failure and send-backs both count here: the review loop allows 3 send-backs, and validate allows another 3.

## 6. The Director's own session

*Read from Claude Code transcripts, not measured by Guildhall. Priced with the implied table in section 1, assuming the same model, which the transcripts confirm is `claude-opus-5-5`.*

| Day | API calls | Wakes | Output (k) | Cache write (k) | Cache read (M) | $ (inferred) |
|---|---|---|---|---|---|---|
| 2026-10-27 | 445 | 154 | 173 | 552 | 56.9 | 22.66 |
| 2026-10-28 | 622 | 179 | 194 | 684 | 80.3 | 30.70 |
| 2026-10-29 | 803 | 234 | 283 | 766 | 114.6 | 42.66 |
| 2026-10-30 | 1,237 | 376 | 397 | 1,436 | 208.3 | 76.07 |
| 2026-10-31 | 1,697 | 519 | 563 | 1,494 | 263.7 | 95.28 |
| 2026-11-01 | 93 | 44 | 28 | 45 | 23.3 | 7.57 |
| 2026-11-02 | 0 | 0 | 0 | 0 | 0 | 0.00 |
| 2026-11-03 | 926 | 261 | 280 | 672 | 123.6 | 44.71 |
| All (from 2026-10-23) | 6,005 | | 1,970 | 5,973 | 881.6 | 325.62 |

**What the totals show:**

- A Director API call re-reads a mean of **146,817 cached tokens**, writes 994 and outputs 328, at about $0.054 per call.
- **Cache reads are about $261 of the $325.62, or 80%.** The Director's cost is driven by how big its context is and how often it calls, not by its output.
- "Wakes" count user turns that start with text (a hook-delivered status or a Guildmaster message), at 2–4 API calls per wake.

**Cache rebuilds between wakes**, by the gap since the session's previous main-thread call:

| Gap | Calls | Cache write | Cache read | Mean write per call | $ (inferred) |
|---|---|---|---|---|---|
| Under 5 min | 5,758 | 4.56M | 846.7M | 791 | 306.14 |
| 5 min to 1 h | 165 | 58k | 31.8M | 353 | 10.27 |
| 1 h or more | 13 | 975k | 0.4M | 74,964 | 5.95 |
| Session start | 38 | 308k | 1.3M | 8,107 | 2.26 |

- With 1-hour cache writes, the cache survives gaps of 5–60 minutes (353 tokens written per call).
- Only gaps over an hour rebuild it, and they cost $5.95 in total.
- 31 sidechain (sub-agent) calls appear, all on 2026-10-27.

The [token attribution record](../decisions/token-attribution-report.md) stands. It rejected transcript inspection for the `spend report` command. This one-off audit reads transcripts for totals only and quotes no transcript text.

## 7. Ranked savings

**How the estimates work:**

- Savings are per day against the last 7 days' mean.
- The fixed-context savings use the measured 153 costed Artisan attempts a day, 39.5 calls per attempt and the implied price table. Each fixed token costs $17.6 per million per attempt (section 3).
- Each entry is one pull request.

### Plain wins

These lose nothing in quality, if the risk named is handled. **Total: about $31 a day (estimated, about 13% of $233 a day).**

| # | What changes | Saving per day | Tokens per day | Evidence | PR size | Risk |
|---|---|---|---|---|---|---|
| W1 | Start Artisan Claude sessions without the orchestration tools they never use (`Workflow`, `ScheduleWakeup`, `ReportFindings`, `ListAgents` and similar), for example with `--disallowedTools` in the adapter arguments. | **$16** ($11–22 for 4–8k tokens) | about 37M (cache read and write) | About 6k tokens of tool schemas in every Artisan context (section 3), re-read on about 39.5 calls | Small: `internal/adapter/claude` arguments, a settings test, an adapter doc line | Some tools may be wanted later. First check with a probe that the flag removes the schema and does not just deny the call: compare cache writes on a trivial attempt before and after. |
| W2 | Do not inherit user-level hooks, MCP servers or user skills in Artisan sessions (`--strict-mcp-config` with an empty config, `disableAllHooks`, setting sources limited to the generated settings). | **$6** ($4–9) | about 14M | About 2.25k tokens per context (section 3) | Small: the same adapter and skill-policy code | Inherited settings may hold permission grants someone relies on (`claude.go:56-58`). Keep the attempt's own generated settings. |
| W3 | Remove the causes of intake refusals and failures, one cause per PR, starting with review's 10 refusals (d166 already covers plan's format cause). | **up to $5** | about 8M | Category A: $35.81, 2.5% of spend. That share of the 7-day mean Artisan spend ($190.92 a day) is $4.8 a day. Stopped attempts unknown | Small for each cause | None. The saving is bounded by A, and C reruns would still have to happen. |
| W4 | Keep check output short in build sessions: the stage guide asks for failures only (for example `go test` without `-v`, and the tail of long logs), and the regenerate and CI-log commands print summaries. | **about $4** (low confidence) | about 12M | A median build's 114 KB of tool results stays in context for every later call. Cutting a quarter of it lowers the mean context by about 3.5k of the 43k tokens, over 68 calls in each of about 49 builds a day. | Small: a stage guide line and command flags | An Artisan that needs full output must ask for it. Measure with R4 before and after. |

**Not worth a PR:** the Director's cache expiry (1-hour cache writes are already used; rebuilds cost $5.95 in total) and inlined standards (none are inlined).

### Trade-offs for the Guildmaster to decide

These could lower quality. **They are never added to the plain-win total.** The model-price savings assume a model at one third of the current price. That is an assumption: the published price list was not available to this audit.

| # | What changes | Saving per day | Evidence | PR size | Quality risk |
|---|---|---|---|---|---|
| T1 | Run the reviewer profile on a cheaper model | about $20 | Reviewer is 15.8% of spend, $30.2 a day | Config only (`[profile.reviewer] model`), plus a decision record | Fewer or weaker findings. Review is the main quality gate. |
| T2 | Run the researcher profile (research and hallmark) on a cheaper model | about $20 | 15.6%, $29.8 a day | Config only | Thinner research notes and hallmark reports |
| T3 | Run the planner profile on a cheaper model | about $8 | 6.0%, $11.5 a day | Config only | Weaker specs. Plan problems multiply into builds. |
| T4 | Run the rebaser on haiku at low effort (as `examples/director.toml` suggests) | about $2 | 1.5%, $2.9 a day | Config only | Wrong conflict resolutions. Rebase reviews still check them. |
| T5 | Lower the builder's effort from medium to low | not estimable from this data | Output is 17.4% of the dollars. Effort mainly changes thinking output and turn count. | Config only, run as a measured A/B over about 20 builds | Weaker builds, and possibly more review rounds that cost more than the saving |
| T6 | Cap send-back rounds at 2 instead of 3 | at most $24 | Attempts numbered 3 and above cost $177.73, 12.6% | Workflow JSON plus a decision record | Defects that review would catch in round 3 ship, or the task fails |
| T7 | Hand the Director session off earlier (`handoff_context_percent` from 70 to about 50) | about $8 | Cache reads are 80% of the Director's $42.43 a day. The mean context is 146.8k tokens. | Config only | More handoffs lose in-session context, and each new session pays about 8k tokens of start-up writes. |
| T8 | Trim the Director's role and task skill text | about $2.70 a day per 10k tokens removed | 926 calls a day re-read whatever is loaded | Docs only, one skill per PR | Rules the Director then forgets |

A cap on slices is not proposed. Done tasks average 1.11 slices, so there is little to save.

## 8. Proposed spend-reporting tickets

Metrics this audit had to infer or work around, each sized as one pull request (Guildmaster context 1).

| # | Ticket | What it fixes in this audit | PR size |
|---|---|---|---|
| R1 | `usage.json` version 2: record the model, `num_turns`, `duration_ms` and `modelUsage` from the result message. Sum the token counts across `modelUsage`, so the tokens cover the whole session. | Model was inferred from today's config. 66 attempts (23.1% of the cost) have under-recorded tokens. | Medium: adapter, usage schema, store spend, tests |
| R2 | `spend report`: model and outcome columns (succeeded, failed, stopped, refused) | The outcome cuts in sections 2 and 5 needed a store pass | Small |
| R3 | `spend report --by day`, bucketed by start receipt | Section 2's per-day table | Small |
| R4 | Tokens per prompt part: record the rendered prompt's bytes per section in the manifest, and add a probe attempt that measures the fixed context (Claude Code's system prompt, tools, inherited hooks) as cache-write tokens | Section 3's overhead split had to be estimated | Medium |
| R5 | Cache misses by cause: keep per-API-call usage from the `stream-json` stream (an attempt `usage-events.jsonl`), so cache writes can be split into first call, later growth and rebuild | Section 3 only has per-attempt totals | Medium |
| R6 | Spend attributed to retries and send-backs: a `spend report` column that labels attempt N>1 by what came before (failure, refusal or send-back round) | Section 5's C and D rows | Small |
| R7 | Partial usage for stopped sessions: sum the per-message usage seen before a stop, marked estimated | 28 stopped attempts are unknown | Small, after R5 |
| R8 | Meter the Director session: a Director spend record built from its transcripts' per-message usage, or from a Stop-hook capture, marked as from transcripts | Section 6 needed a one-off transcript read. Grill's 135 attempts are unknown. | Medium, plus an amendment to the token attribution record |
| R9 | Codex usage capture: the Codex adapter writes `usage.json` | No Codex attempts yet. Judge runs would otherwise be unknown from the start. | Small |

## 9. Follow-ups

The Director can copy each of these into a task as it stands. Items 1–4 are plain wins, 5–12 are trade-offs that need the Guildmaster's choice, and 13–21 are reporting.

1. **Drop unused orchestration tools from Artisan sessions** (W1). Start Artisan Claude sessions without `Workflow`, `ScheduleWakeup`, `ReportFindings`, `ListAgents` and similar tools. First prove with a probe that the schemas leave the context. Estimated $16 a day. One small PR.
2. **Stop inheriting user-level hooks, MCP servers and user skills in Artisan sessions** (W2). Keep the generated attempt settings and their grants. Estimated $6 a day. One small PR.
3. **Remove the cause of review's intake refusals** (W3). 10 of the 16 refusals; plan's cause is d166. Up to $5 a day together with the failures. One small PR per cause.
4. **Keep check output short in builds** (W4). A stage guide line and summary flags on the regenerate and CI-log commands. About $4 a day, low confidence, to be measured with item 16. One small PR.
5. **Trade-off: run the reviewer on a cheaper model** (T1). About $20 a day. Config and a decision record.
6. **Trade-off: run the researcher on a cheaper model** (T2). About $20 a day. Config and a decision record.
7. **Trade-off: run the planner on a cheaper model** (T3). About $8 a day. Config.
8. **Trade-off: run the rebaser on haiku at low effort** (T4). About $2 a day. Config.
9. **Trade-off: A/B the builder at low effort** (T5). Saving to be measured. Config and an evidence note.
10. **Trade-off: cap send-back rounds at 2** (T6). At most $24 a day. Workflow JSON and a decision record.
11. **Trade-off: hand the Director off at about 50% context** (T7). About $8 a day. Config.
12. **Trade-off: trim the Director's role and task skill text** (T8). About $2.70 a day per 10k tokens. One skill per PR.
13. **`usage.json` version 2 with model, turns, duration and `modelUsage`, with tokens summed across models** (R1). Medium PR.
14. **Model and outcome columns in `spend report`** (R2). Small PR.
15. **`spend report --by day`** (R3). Small PR.
16. **Per-section prompt bytes in the manifest, and a fixed-context probe** (R4). Medium PR.
17. **Per-API-call usage events for cache misses by cause** (R5). Medium PR.
18. **Retry and send-back attribution in `spend report`** (R6). Small PR.
19. **Partial usage for stopped sessions** (R7). Small PR, after item 17.
20. **Meter the Director session** (R8). Medium PR, and amend the token attribution record.
21. **Codex usage capture** (R9). Small PR.

## 10. Review intake refusals (d192)

Follow-up 3 (W3) needs the reason for each of review's 10 intake refusals. The tables above count them but never print why. `-refusals` lists each one from the same read-only store, with its reason quoted as stored and local paths masked. The output goes to `docs/evidence/token-usage-audit-2026-11-review-refusals.txt`. It covers `review` and `review--s<n>` steps; rebase-review steps are left out, as above. It groups the refusals three ways: in the audit window (starts up to 2026-11-03T20:58:32Z), after it but before #320, and since #320 (2026-11-04T08:21Z).

**Which check now catches each family.** These are merge times in UTC: #191 `review check` at 2026-10-30 07:22, #306 `result check`/`result submit` at 2026-11-04 02:44, and #320 prompts requiring `submit` at 2026-11-04 08:21.

| Family | Reason text (start) | Caught in-session by |
|---|---|---|
| Outcome form: schema, summary, verdict, ids, findings, dispositions, revisions form | `review outcome: …`, `summary …`, `verdict …`, `finding …`, `disposition …`, `subject …` | #191 (`review check`), and #306+#320 |
| Coverage | `previous finding %q has no disposition` | #191 (`review check --previous`), and #306+#320 |
| Binding | `review outcome is stale: … differ` | #306+#320 only; `review check` cannot see the binding |
| Generic output or identity | `a succeeded result lists no output …`, `output path …`, `read output …`, `malformed artisan result`, `… for another task attempt` | #306+#320 |
| Steering (added by #321, after the window) | `unacknowledged steering message(s) to this attempt: …` | #306+#320, except a message that arrives after the last `submit` |

**Verdicts.**

- A refusal in the window, or before #320, gets the verdict of its family's row: "caught by #191" or "caught by #306+#320".
- A refusal that started after #320 had every tool and was still refused, so it is "still live".
- A reason that fits no family is also "still live" until someone reads it.
- A reason that intake no longer gives at all is "removed by #<n>", naming the PR that dropped the check.

The program prints a first reading of each row, and every row is then checked by hand against the stored reason. Each "still live" row becomes one slice of d192's slice 2, with a red-first regression test. If no row is still live, slice 2 is dropped.

Three paths stay open even after the new tools: a steering message that arrives after the last `submit`, a third rejected submission (it becomes the draft by design), and a result written without `submit`. A refusal through any of them would show up in the "since #320" group.

**Refusals.** The rows below come from the evidence file, which was regenerated under the read-only store grant g1 on 2026-11-04 at 13:54 UTC. The audit window holds 10 review refusals, which matches the "intake refusals by step" count. The two later groups are empty: no review attempt has been refused since the window closed. Each row was checked by hand against the validator on `main`:

- The id refusals (d4, d8) and the "resolved finding … still listed" refusals (d12, d27) come from `ReviewOutcome.Validate` in `internal/workflow/review.go`, which `review check` runs. `TestReviewCheckRefusesWhatIntakeRefuses` replays d12 review-3 and d27 review-5. The id wording now ends "…, starting with a letter".
- The d3 review-1 refusal (`a succeeded result lists no output for port outcome`) comes from the engine's output check (`internal/interp/engine.go`). `review check` does not see it, but `result submit` does.
- The d3 review-2 refusal (`standards_revision is required`) no longer exists. #149 (merged 2026-10-26 18:39 UTC, about 1h40m after that attempt started) made `standards_revision` optional. Now only a whitespace-only value is refused.

| Attempt | Started (UTC) | Tools at start | Reason (as stored) | Family | Verdict |
|---|---|---|---|---|---|
| guildhall/d3/review-1 | 2026-10-25 21:37 | before #191 | `a succeeded result lists no output for port outcome` | generic output or identity | caught by #306+#320 |
| guildhall/d3/review-2 | 2026-10-26 16:58 | before #191 | `invalid review outcome: standards_revision is required` | outcome form | removed by #149 |
| guildhall/d4/review-1 | 2026-10-27 09:27 | before #191 | `invalid review outcome: finding id "S1" must be lowercase letters, digits and hyphens; …` (S1–S4) | outcome form | caught by #191 |
| guildhall/d4/review-2 | 2026-10-27 09:29 | before #191 | `invalid review outcome: finding id "R1" must be lowercase letters, digits and hyphens; …` (R1, S1–S3) | outcome form | caught by #191 |
| guildhall/d4/review-3 | 2026-10-27 09:32 | before #191 | `invalid review outcome: finding id "R1" must be lowercase letters, digits and hyphens; …` (R1, S1–S3) | outcome form | caught by #191 |
| guildhall/d8/review-1 | 2026-10-27 22:16 | before #191 | `invalid review outcome: finding id "F1" must be lowercase letters, digits and hyphens; …` (F1, F2) | outcome form | caught by #191 |
| guildhall/d8/review-2 | 2026-10-27 22:19 | before #191 | `invalid review outcome: finding id "F1" must be lowercase letters, digits and hyphens; …` (F1, F2) | outcome form | caught by #191 |
| guildhall/d8/review-3 | 2026-10-27 22:32 | before #191 | `invalid review outcome: finding id "S1" must be lowercase letters, digits and hyphens; …` (S1, S2) | outcome form | caught by #191 |
| guildhall/d27/review-5 | 2026-10-29 21:10 | before #191 | `invalid review outcome: resolved finding "relaunch-base" is still listed in findings or suggestions` | outcome form | caught by #191 |
| guildhall/d12/review-3 | 2026-10-29 21:45 | before #191 | `invalid review outcome: resolved finding "suggest-4" is still listed in findings or suggestions; resolved finding "suggest-5" is still listed in findings or suggestions` | outcome form | caught by #191 |

Full reasons are in the evidence file. No row is still live, and neither later group has a refusal, so by Q2 slice 2 has nothing to fix and can be dropped.
## Reproducing

```text
go run docs/evidence/token-usage-audit-2026-11.go -copy <director copy> -transcripts <director project dir>[,<dir>...] > docs/evidence/token-usage-audit-2026-11.txt
go run docs/evidence/token-usage-audit-2026-11.go -copy <director copy> -refusals > docs/evidence/token-usage-audit-2026-11-review-refusals.txt
guildhall spend report --copy <director copy> --json   # total.estimated.cost_usd must match the script's cost_usd to the cent
```

The script carries a `//go:build ignore` tag, so `go build ./...` and `go test ./...` never compile it. It opens the store read-only and prints totals only: no local paths, session ids, URLs, or prompt or transcript text. The live store keeps changing, so a rerun will show newer figures.
