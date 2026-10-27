package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/workflow"
)

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
			b.WriteString("Commit all tracked work. Guildhall takes the final Git HEAD automatically; no file is needed. Leave no uncommitted tracked changes (untracked notes alone are allowed). Do not list source files as outputs. The recorded format is a full 40- or 64-character lowercase hexadecimal commit id, for example:\n\n```text\n" + strings.Repeat("0", 40) + "\n```\n")
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
			outcome := workflow.ReviewOutcome{Schema: workflow.ReviewOutcomeSchema, Reviewer: workflow.AutomatedReviewer, Verdict: workflow.VerdictApproved, Revisions: rev}
			data, _ := json.MarshalIndent(outcome, "", "  ")
			b.WriteString("Format: one strict JSON `guildhall.review-outcome/1` object, not Markdown or a prose verdict. Use reviewer `automated`; verdict is `approved`, `changes_requested`, or `rejected` (the latter two require findings). Findings have `id` and `summary`, and may cite `rule`, `file`, and `line`. A finding `id` is lowercase letters, digits and hyphens, starting with a letter, at most 63 characters (e.g. `f1`, `must-1`); keep a finding's id in later rounds. For every previous finding, include a disposition with `finding` and `state` (`resolved`, `still-open`, or `withdrawn`); still-open findings must also remain in findings.\n\n")
			if revisions == nil {
				b.WriteString("The revision values below are illustrative: replace them with the exact reviewed input head/document digest, accepted spec digest (for code review), and standards revision. Never submit the illustrative zero hashes.\n\n")
			} else {
				b.WriteString("The revisions below are the exact bound revisions for this attempt; preserve them in your outcome.\n\n")
			}
			fmt.Fprintf(&b, "Minimal approval example (choose your actual verdict and findings):\n\n```json\n%s\n```\n", data)
			outcome.Verdict = workflow.VerdictChangesRequested
			outcome.Findings = []workflow.Finding{{ID: "f1", Summary: "What is wrong and what to change."}}
			data, _ = json.MarshalIndent(outcome, "", "  ")
			fmt.Fprintf(&b, "\nExample with a finding:\n\n```json\n%s\n```\n", data)
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
