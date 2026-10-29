package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// RebaseReportFile is the file a rebase attempt writes besides its head: whether its resolution changes behaviour.
const RebaseReportFile = "rebase.json"

// outputInstructions is part of the generated agent-facing protocol. It lives
// outside baseline prose so even an old copy's overridden guide gets the current
// port formats and the same result template in every runtime.
func outputInstructions(id Identity, revisions *workflow.Revisions) string {
	kind, _ := workflow.LookupKind(id.Kind, id.KindVersion)
	var b strings.Builder
	b.WriteString("\n## Required outputs\n\nA succeeded result must provide every required output below. A verdict in the result summary is not an output file. Paths are relative to your current working directory, not the directory containing this prompt.\n")
	outputs := []adapter.Output{}
	for _, port := range kind.Outputs {
		fmt.Fprintf(&b, "\n### Output port `%s`\n\n", port.Name)
		if port.Type == workflow.CodeHead {
			fmt.Fprintf(&b, "Commit on the task branch `guildhall/%s` in your working directory; Guildhall refuses a HEAD that is not that branch's tip or does not descend from the attempt's base. ", id.Task)
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
		}
	}
	result := struct {
		Schema  string `json:"schema"`
		Version int    `json:"version"`
		adapter.Result
	}{adapter.ResultSchema, adapter.ResultVersion, adapter.Result{Identity: adapter.Identity{TaskID: id.Task, AttemptID: fmt.Sprintf("%s-%d", id.Step, id.Attempt)}, Status: adapter.StatusSucceeded, Summary: "What was done, in one or more sentences.", Outputs: outputs}}
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Fprintf(&b, "\n## Result template\n\nAfter writing the required files (or committing the code head), write this JSON shape to the result file your runtime names, once, as your last action. Keep schema, version, task_id and attempt_id unchanged. Use status `succeeded` only when the work and required outputs are complete; otherwise use `failed` and explain why in summary (failed results may use an empty outputs list).\n\n```json\n%s\n```\n", data)
	return b.String()
}
