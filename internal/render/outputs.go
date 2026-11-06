package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/spec"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// RebaseReportFile is the file a rebase attempt writes besides its head: whether its resolution changes behaviour.
const RebaseReportFile = "rebase.json"

// outputInstructions is part of the generated agent-facing protocol. It lives
// outside baseline prose so even an old copy's overridden guide gets the current
// port formats and the same result template in every runtime.
func outputInstructions(id Identity, revisions *workflow.Revisions, branch string, strict []spec.Example, surface Surface, slice *Slice) string {
	if branch == "" {
		branch = "guildhall/" + id.Task
	}
	kind, _ := workflow.LookupKind(id.Kind, id.KindVersion)
	var b strings.Builder
	b.WriteString("\n## Required outputs\n\nA succeeded result must provide every required output below. A verdict in the result summary is not an output file. Paths are relative to your current working directory, not the directory containing this prompt.\n")
	outputs := []adapter.Output{}
	for _, port := range kind.Outputs {
		fmt.Fprintf(&b, "\n### Output port `%s`\n\n", port.Name)
		if port.Type == workflow.CodeHead {
			fmt.Fprintf(&b, "Commit on the task branch `%s` in your working directory; Guildhall refuses a HEAD that is not that branch's tip or does not descend from the attempt's base. ", branch)
			b.WriteString("Commit all tracked work. Guildhall takes the final Git HEAD automatically; no file is needed. Leave no uncommitted tracked changes (untracked notes alone are allowed). Do not list source files as outputs. The recorded format is a full 40- or 64-character lowercase hexadecimal commit id, for example:\n\n```text\n" + strings.Repeat("0", 40) + "\n```\n")
			if id.Kind == "rebase" {
				outputs = append(outputs, adapter.Output{Path: RebaseReportFile})
				fmt.Fprintf(&b, "\n### Also `%s`\n\nWrite `%s`, untracked, beside your commit and list it in result outputs: one strict JSON object saying whether your resolution changes what either side does, and why, for example:\n\n```json\n{\"behaviour_change\": false, \"reason\": \"Kept both sides' changes to the conflicting hunks.\"}\n```\n", RebaseReportFile, RebaseReportFile)
			}
			continue
		}
		filename := port.Name + ".md"
		if port.Type == workflow.ReviewResult {
			filename = port.Name + ".json"
		}
		outputs = append(outputs, adapter.Output{Path: filename})
		fmt.Fprintf(&b, "Write `%s` and list it in result outputs.\n\n", filename)
		if port.Type == workflow.ReviewResult {
			rev := workflow.Revisions{StandardsRevision: "example-standards-revision"}
			if id.Kind == "document-review" {
				rev.Subject.DocumentDigest = "sha256:" + strings.Repeat("0", 64)
			} else {
				rev.Subject.CodeHead = strings.Repeat("0", 40)
				rev.SpecDigest = "sha256:" + strings.Repeat("0", 64)
			}
			if revisions != nil {
				rev = *revisions
			}
			outcome := workflow.ReviewOutcome{Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.AutomatedReviewer, Verdict: workflow.VerdictApproved,
				Summary:     "The change does what the spec asks and its tests cover it; one optional improvement is suggested.",
				Suggestions: []workflow.Suggestion{{ID: "s1", File: "path/to/file.go", Line: 12, Summary: "An optional improvement; it never blocks."}},
				Revisions:   rev}
			data, _ := json.MarshalIndent(outcome, "", "  ")
			b.WriteString("Format: one strict JSON `guildhall.review-outcome/2` object, not Markdown or a prose verdict. Use reviewer `automated`; verdict is `approved`, `changes_requested`, or `rejected` (the latter two require findings). `summary` is required: your overall verdict in one paragraph, with no blank line. Findings are blocking: each has `id`, `summary` and the `rule` it breaks, and may give `file` and `line`. Anything that cites no rule is a suggestion, in `suggestions`, with `id` and `summary` and optional `file` and `line`; suggestions never block and need no disposition later. An `id` is lowercase letters, digits and hyphens, starting with a letter, at most 63 characters (e.g. `f1`, `must-1`), used once across findings and suggestions; keep a finding's id in later rounds. For every previous finding, include a disposition with `finding` and `state`: `resolved` (no longer listed), `unresolved` (still listed in findings), or `disputed` (the builder argued against it and you still hold it: still listed in findings, with a `reason`).\n\n")
			if revisions == nil {
				b.WriteString("The revision values below are illustrative: replace them with the exact reviewed input head/document digest, accepted spec digest (for code review), and standards revision. Never submit the illustrative zero hashes.\n\n")
			} else {
				b.WriteString("The revisions below are the exact bound revisions for this attempt; preserve them in your outcome.\n\n")
			}
			fmt.Fprintf(&b, "Approval example (choose your actual verdict, summary, findings and suggestions):\n\n```json\n%s\n```\n", data)
			outcome.Verdict = workflow.VerdictChangesRequested
			outcome.Summary = "Two findings from the previous round still stand, one of them disputed by the builder; the third was fixed."
			outcome.Findings = []workflow.Finding{
				{ID: "f1", Rule: "R1.2", File: "path/to/file.go", Line: 30, Summary: "What is wrong and what to change."},
				{ID: "f2", Rule: "S4", Summary: "A finding the builder argued against and you still hold."},
			}
			outcome.Dispositions = []workflow.Disposition{
				{Finding: "f0", State: workflow.Resolved},
				{Finding: "f1", State: workflow.Unresolved},
				{Finding: "f2", State: workflow.Disputed, Reason: "Why the builder's argument does not settle it."},
			}
			data, _ = json.MarshalIndent(outcome, "", "  ")
			fmt.Fprintf(&b, "\nExample requesting changes, with previous findings f0, f1 and f2:\n\n```json\n%s\n```\n", data)
		} else {
			b.WriteString("Format: a UTF-8 Markdown document. Minimal example:\n\n```text\n# Notes\n\nThe requested work is recorded here.\n```\n")
			b.WriteString(strictSections(id.Kind, filename, strict))
		}
		if mockup := MockupFile(surface); id.Kind == designKind && mockup != "" {
			outputs = append(outputs, adapter.Output{Path: mockup})
			b.WriteString(mockupInstructions(surface, filename))
		}
	}
	result := struct {
		Schema  string `json:"schema"`
		Version int    `json:"version"`
		adapter.Result
	}{adapter.ResultSchema, adapter.ResultVersion, adapter.Result{Identity: adapter.Identity{TaskID: id.Task, AttemptID: fmt.Sprintf("%s-%d", id.Step, id.Attempt)}, Status: adapter.StatusSucceeded, Summary: "What was done, in one or more sentences.", Outputs: outputs}}
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Fprintf(&b, "\n## Result template\n\n%s Keep schema, version, task_id and attempt_id unchanged. Use status `succeeded` only when the work and required outputs are complete; otherwise use `failed` and explain why in summary (failed results may use an empty outputs list).\n\n```json\n%s\n```\n", SubmitStep, data)
	if workflow.ReportsOutcome(id.Kind) {
		b.WriteString(outcomeInstructions(slice))
	}
	return b.String()
}

// outcomeInstructions is the Result template's part on the optional structured outcome (docs/adapters.md#result-file),
// which only research, plan and build prompts carry (workflow.ReportsOutcome). It offers by_slice only for a slice
// after the first of a multi-PR task, the only place intake accepts it.
func outcomeInstructions(slice *Slice) string {
	var b strings.Builder
	b.WriteString("\n### Already delivered or nothing to do\n\n" +
		"When a merged pull request already shipped everything this stage asks for, or the task needs no change at all, say so with an `outcome` field beside the fields above: " +
		"use status `failed` and `outputs: []`, commit nothing, start the summary as the stage guide says and give the evidence there. " +
		"Guildhall reads the outcome rather than your summary; nothing closes until the Director or the Guildmaster confirms it. Leave the field out otherwise. " +
		"For a change a merged pull request of this repository already shipped:\n\n" +
		"```json\n\"outcome\": {\"kind\": \"already-delivered\", \"by_pr\": \"https://github.com/owner/repo/pull/123\"}\n```\n")
	if slice != nil && len(slice.Entries) > 1 && slice.Current > 1 {
		fmt.Fprintf(&b, "\nFor a change an earlier slice of this task already delivered, its number, from 1 to %d, in place of `by_pr`:\n\n"+
			"```json\n\"outcome\": {\"kind\": \"already-delivered\", \"by_slice\": 1}\n```\n", slice.Current-1)
	}
	b.WriteString("\nFor a task that needs no change, with why in `reason`:\n\n" +
		"```json\n\"outcome\": {\"kind\": \"nothing-to-do\", \"reason\": \"Why the task needs no change.\"}\n```\n")
	return b.String()
}

// SubmitStep is how every prompt's Result template says to hand in the result: write it to the draft file, submit it
// with `guildhall result submit`, which runs intake's checks, and fix and resubmit until it prints ok. A runtime that
// names only a result file gets it written directly, as before.
const SubmitStep = "After writing the required files (or committing the code head), write this JSON shape to the draft file your runtime names and submit it with `guildhall result submit <draft file>`, which runs the checks Guildhall runs on your result when you finish. " +
	"If it prints `rejected: <reason>`, fix what the reason names, in your files or the draft, and submit again; you are done when it prints `ok`, or when it says no resubmissions are left. " +
	"If your runtime names a result file and no draft file, write this JSON shape to that result file instead, once, as your last action."

// strictKinds are the strict sections a kind's document port has that Guildhall parses itself but intake does not
// refuse: a hallmark report's questions are only read for the follow-ups ask. Which sections intake refuses is not
// here: interp.StrictSections owns that list, and Stage.Strict carries it.
var strictKinds = map[string][]spec.Example{
	"hallmark": {spec.ReportQuestionsExample},
}

// strictSections is the Strict sections block of kind's document port, written as file: refused, the sections intake
// refuses (Stage.Strict), exactly as given, or else kind's parsed-only sections (strictKinds); for each its rule, a
// valid and an invalid example and the error the invalid one gets, all from internal/spec's examples, which the
// parser tests run. It is empty when there are none.
func strictSections(kind, file string, refused []spec.Example) string {
	var b strings.Builder
	examples := refused
	if len(refused) > 0 {
		fmt.Fprintf(&b, "\nStrict sections: Guildhall parses these sections of `%s` itself, and `guildhall result submit` and intake refuse a result that breaks one with the error shown, so copy the valid shape exactly.\n", file)
	} else if examples = strictKinds[kind]; len(examples) > 0 {
		fmt.Fprintf(&b, "\nStrict sections: Guildhall parses these sections of `%s` itself for the ask it shows the Guildmaster, and leaves out a section that breaks one, with the error shown, so copy the valid shape exactly.\n", file)
	}
	for _, ex := range examples {
		fmt.Fprintf(&b, "\n**`%s`.** %s\n\nValid:\n\n```text\n%s\n```\n\nInvalid:\n\n```text\n%s\n```\n\nIt gets: `%s`\n", ex.Heading, ex.Rule, ex.Valid, ex.Invalid, ex.Error)
	}
	return b.String()
}

// mockupInstructions is a designer prompt's "Also" section for the mock-up its brief's surface asks for, written
// beside design (the design port's file) and listed second; intake (docs/interpreter.md#design-rounds) refuses a
// result that breaks it.
func mockupInstructions(surface Surface, design string) string {
	mockup := MockupFile(surface)
	var b strings.Builder
	fmt.Fprintf(&b, "\n### Also `%s`\n\nThe brief's surface is `%s`. Write `%s`, untracked, beside `%s` and list it in result outputs second, after `%s`. ", mockup, surface, mockup, design, design)
	if surface == SurfaceWebPage {
		b.WriteString("It shows the changed web page as it will really look, with sample data. " +
			"Build it from the real task page: copy `internal/web/testdata/task.golden.html`, or `internal/web/testdata/task.answering.golden.html` when the change touches the Guildmaster's forms " +
			"(the real page template and CSS rendered with a fixture, the CSS inlined), and edit its markup and sample data. " +
			"Lay it out as the stage guide says: two `<iframe srcdoc>` frames, desktop 1280px wide and phone 390px wide, side by side and wrapping on a narrow screen, each holding the edited page. " +
			"It must be UTF-8 with no `<script`, no `on…=` event attribute and no `http:`, `https:` or `//` URL in `src`, `href`, `url(` or `@import`: only inline styles and `data:` images, so remove the golden page's own `<script>` elements and point each external link it has, such as a pull request link, at `#`. " +
			"Intake fails a result that lists no mock-up, another file, or a mock-up that breaks these rules.\n")
		return b.String()
	}
	what := "command-line output"
	if surface == SurfaceChat {
		what = "chat reply"
	}
	fmt.Fprintf(&b, "It shows the exact sample %s the design changes, with sample data, as preformatted text in at least one fenced code block, a short heading or sentence before each saying what it shows. Minimal example:\n\n````text\n# Mock-up\n\nThe new reply:\n\n```text\nThe exact sample %s, line for line.\n```\n````\n\n", what, what)
	b.WriteString("Intake fails a result that lists no mock-up, another file, or a mock-up with no fenced code block.\n")
	return b.String()
}
