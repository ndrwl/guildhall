// Package render turns one workflow stage into what its actor reads:
// a self-contained Artisan attempt (see Prompt), or a typed question for a Guildmaster stage.
//
// Rendering is pure over files: it reads the sources it is given, writes only inside the attempt
// directory it is given, runs nothing and records nothing.
// Choosing which role, stage guide, standards and skill roots apply (the task baseline),
// and recording the returned manifest with the attempt, belong to the caller.
// See docs/rendering.md.
package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// ManifestSchema versions the manifest that records what a prompt was rendered from: it is
// ManifestSchemaPrefix followed by ManifestSchemaVersion.
const (
	ManifestSchema        = ManifestSchemaPrefix + "1"
	ManifestSchemaPrefix  = "guildhall.prompt-manifest/"
	ManifestSchemaVersion = 1
)

// PromptFile, SkillsDir and InputsDir are the names Prompt writes inside the attempt directory.
const (
	PromptFile = "prompt.md"
	SkillsDir  = "skills"
	InputsDir  = "inputs"
)

var (
	// ErrUnknownSkill reports a skill name found in none of the skill roots.
	ErrUnknownSkill = errors.New("unknown skill")
	// ErrDigestMismatch reports an input document whose content is not the content its record names.
	ErrDigestMismatch = errors.New("input does not match its recorded digest")
	// ErrNotEmpty reports an attempt directory that already holds files; a rendered attempt is never replaced.
	ErrNotEmpty = errors.New("attempt directory is not empty")
	// ErrWrongActor reports Prompt called for a Guildmaster stage or Question for a stage that is not one.
	ErrWrongActor = errors.New("stage has a different actor")
)

// Stage is one attempt of one workflow step.
type Stage struct {
	Repository string
	Task       string
	// Title is the task's Director-written title. Prompt renders it fenced as text after the header; it is not part of
	// the manifest's Identity. Empty renders no title.
	Title string
	// Detail is the task's Director-written full statement. Prompt renders it fenced as text after the title; it is
	// not part of the manifest's Identity. Empty renders no detail.
	Detail string
	// Step is the step from the task's bound workflow revision; its kind and settings decide the actor,
	// and its "profile", "skills" and "instructions" settings are rendered.
	Step workflow.Step
	// Attempt is the attempt number, starting at 1.
	Attempt int
	// Revisions, when present, supplies the exact binding for review outcome examples.
	// The interpreter owns this value; standalone callers may omit it for illustrative examples.
	Revisions *workflow.Revisions
	// ReviewBase is the recorded code-review range base, not a newly resolved branch tip.
	// Empty means unavailable; it is rendered explicitly for code reviews only.
	ReviewBase string
	// Retry, when present, says why this attempt retries the previous one; it is rendered for every actor.
	Retry *Retry
	// Main, when present, says how far the task branch is behind the default branch and whether it was kept
	// where it was; it is rendered only when the branch is behind or was kept.
	Main *Main
	// Branch is the task branch an Artisan commits a code head on, a multi-PR task's slice branch; empty is
	// guildhall/<task>. It is not part of the manifest's Identity.
	Branch string
	// Dir is the attempt directory Prompt writes into; it must not exist or be empty.
	// Question ignores it.
	Dir string
	// Summary is a plan approval's spec Summary block (spec.Summary), which Question writes after the first line of
	// a guildmaster-approval question. The caller reads the spec; Question reads no files. Prompt ignores it.
	Summary string
	// NoSummary marks a plan approval whose spec has no Summary block, or one that could not be read: Question then
	// writes a line pointing at the spec instead. It is ignored when Summary is set, and by other kinds and Prompt.
	NoSummary bool
}

// Retry is what the Director's retry of the previous attempt tells the next one.
type Retry struct {
	// Attempt is the retried attempt's number; Outcome is how it ended, "failed" or "stopped".
	Attempt int
	Outcome string
	// Reason is the Director's reason for the retry, Rejection the retried attempt's intake rejection, and
	// Summary the summary of its result; each may be empty.
	Reason    string
	Rejection string
	Summary   string
}

// Main is where a code-head attempt's task branch stands against the default branch as the attempt launches.
type Main struct {
	// Behind is how many default-branch commits the task branch does not contain.
	Behind int
	// Kept, when not empty, is why the untouched task branch was not moved to the current main.
	Kept string
}

// Input is one input document wired to the stage.
type Input struct {
	// Port is the step input port the document is wired to, such as "spec".
	Port string
	// Name is the document's recorded path (store.DocumentRef.Path), relative and slash-separated.
	Name string
	// SHA256 is the recorded lowercase hex sha256 of the document's exact bytes.
	SHA256 string
	// Path is the local file. Prompt verifies SHA256 and copies its exact bytes to
	// inputs/<Name> beside the prompt; Question does not read it.
	Path string
}

// Source is one Markdown file of the task baseline.
type Source struct {
	// Name is the file's portable name recorded in the manifest, such as "stages/build.md";
	// backslashes are read as separators, and the result must be a relative path without dot segments.
	Name string
	// Path is the local file to read.
	Path string
	// Root names the baseline root the file was resolved from, such as "copy" or "release", and is recorded in the manifest.
	Root string
}

// Baseline is the pinned prose a prompt is built from.
type Baseline struct {
	// Role is the role prompt: the profile's role, or the release's Artisan role.
	Role Source
	// Guide is the stage guide for the step's kind.
	Guide Source
	// Standards are rendered in order, in full, because a reviewer measures against these exact words.
	Standards []Source
	// Policies are the repository's policies (docs/configuration.md#repository-policies), rendered in one text fence
	// under "Repository policies" after the instructions; none renders no section.
	Policies []string
	// PolicyError, when not empty, says why the repository's policy file could not be read; the section then
	// reports it instead of the policies.
	PolicyError string
}

// SkillRoot is one directory of skill directories, named for the manifest.
type SkillRoot struct {
	// Name says where the root comes from, such as "release", "managed" or "director".
	Name string
	Dir  string
}

// Rendered is the result of Prompt.
type Rendered struct {
	// PromptPath and SkillsPath are the local paths written inside Stage.Dir.
	PromptPath string
	SkillsPath string
	Manifest   Manifest
}

// Manifest lists every file a prompt used and its digest, so an attempt's prompt can be reproduced and checked.
// Every path is slash-separated; every digest is "sha256:<64 lowercase hex>".
type Manifest struct {
	Schema    string      `json:"schema"`
	Identity  Identity    `json:"identity"`
	Prompt    File        `json:"prompt"`
	Role      File        `json:"role"`
	Guide     File        `json:"guide"`
	Standards []File      `json:"standards"`
	Inputs    []InputFile `json:"inputs"`
	Skills    []Skill     `json:"skills"`
}

// Identity names the attempt a prompt was rendered for.
type Identity struct {
	Repository  string `json:"repository"`
	Task        string `json:"task"`
	Step        string `json:"step"`
	Kind        string `json:"kind"`
	KindVersion int    `json:"kind_version"`
	Attempt     int    `json:"attempt"`
	Profile     string `json:"profile,omitempty"`
}

// File is one file and its digest.
// For the prompt, role, guide and standards the digest is of the text after line endings are normalised to LF,
// so a CRLF checkout of the same text has the same digest; for inputs and skill files it is of the exact bytes.
type File struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	// Root is the baseline root a role, guide or standard came from (Source.Root); empty for every other file.
	Root string `json:"root,omitempty"`
}

// InputFile is an input document and the port it was wired to.
type InputFile struct {
	Port   string `json:"port"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// Skill is one skill copied into the attempt's skills directory.
type Skill struct {
	Name string `json:"name"`
	// Root is the SkillRoot.Name it was resolved from.
	Root string `json:"root"`
	// Path is the copy's path relative to the attempt directory, "skills/<name>".
	Path string `json:"path"`
	// Digest is the tree digest: the sha256 of one "<hex>  <path>\n" line per file, sorted by path,
	// where hex is the file's sha256 and path is relative to the skill directory.
	// File modes are not part of it, so the same skill digests the same on every platform.
	Digest string `json:"digest"`
	Files  []File `json:"files"`
}

// Prompt renders an Artisan attempt into stage.Dir: one LF-only prompt file holding the attempt identity,
// the role prompt, the stage guide, the step's instructions, the input documents by relative path and digest
// and the standards in full; an inputs directory holding exact verified copies under their recorded names;
// and a skills directory holding a copy of every skill the step names.
// Input paths in the manifest remain the recorded names, without the inputs/ prefix.
//
// Each skill name is resolved in roots in order, the first root holding a directory of that name winning;
// a name found in none is refused with ErrUnknownSkill.
// A skill is copied with every file, scripts included, byte for byte and keeping the permission bits the
// platform supports; a symbolic link or other non-regular file inside a skill is refused.
// An input whose file does not match its recorded digest is refused with ErrDigestMismatch.
// A Guildmaster stage is refused with ErrWrongActor, and a non-empty stage.Dir with ErrNotEmpty.
// On error nothing Prompt wrote is left behind; a crash mid-render can leave files the caller removes
// before rendering that attempt again.
func Prompt(stage Stage, inputs []Input, baseline Baseline, roots []SkillRoot) (Rendered, error) {
	kind, err := stageKind(stage, false)
	if err != nil {
		return Rendered{}, err
	}
	m := Manifest{Schema: ManifestSchema, Identity: identity(stage, kind), Inputs: []InputFile{}, Standards: []File{}, Skills: []Skill{}}
	var role, guide string
	if m.Role, role, err = readSource("role", baseline.Role); err != nil {
		return Rendered{}, err
	}
	if m.Guide, guide, err = readSource("stage guide", baseline.Guide); err != nil {
		return Rendered{}, err
	}
	standards := make([]string, len(baseline.Standards))
	for i, src := range baseline.Standards {
		f, text, err := readSource("standard", src)
		if err != nil {
			return Rendered{}, err
		}
		m.Standards, standards[i] = append(m.Standards, f), text
	}
	inputData := make([][]byte, 0, len(inputs))
	for _, in := range inputs {
		f, data, err := checkInput(in)
		if err != nil {
			return Rendered{}, err
		}
		m.Inputs = append(m.Inputs, f)
		inputData = append(inputData, data)
	}
	found, err := resolveSkills(stage.Step.Skills(), roots)
	if err != nil {
		return Rendered{}, err
	}

	r := Rendered{PromptPath: filepath.Join(stage.Dir, PromptFile), SkillsPath: filepath.Join(stage.Dir, SkillsDir)}
	created, err := prepareDir(stage.Dir)
	if err != nil {
		return Rendered{}, err
	}
	if err := r.write(&m, inputData, found, stage.Title, stage.Detail, stage.Step.Instructions(), role, guide, standards, stage.Step.Actor(), stage.Revisions, stage.ReviewBase, stage.Retry, stage.Main, stage.Branch, baseline); err != nil {
		_ = os.RemoveAll(r.PromptPath)
		_ = os.RemoveAll(r.SkillsPath)
		_ = os.RemoveAll(filepath.Join(stage.Dir, InputsDir))
		if created {
			_ = os.Remove(stage.Dir)
		}
		return Rendered{}, err
	}
	r.Manifest = m
	return r, nil
}

func (r Rendered) write(m *Manifest, inputData [][]byte, found []foundSkill, title, detail, instructions, role, guide string, standards []string, actor workflow.Actor, revisions *workflow.Revisions, reviewBase string, retry *Retry, main *Main, branch string, baseline Baseline) error {
	inputsPath := filepath.Join(filepath.Dir(r.PromptPath), InputsDir)
	if err := os.Mkdir(inputsPath, 0o755); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	for i, in := range m.Inputs {
		target := filepath.Join(inputsPath, filepath.FromSlash(in.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("render: input %s: %w", in.Path, err)
		}
		if existing, err := os.ReadFile(target); err == nil {
			if !bytes.Equal(existing, inputData[i]) {
				return fmt.Errorf("render: conflicting input copies at %s", in.Path)
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("render: input %s: %w", in.Path, err)
		}
		if err := os.WriteFile(target, inputData[i], 0o644); err != nil {
			return fmt.Errorf("render: input %s: %w", in.Path, err)
		}
	}
	if err := os.Mkdir(r.SkillsPath, 0o755); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	for _, sk := range found {
		skill, err := copySkill(sk, filepath.Join(r.SkillsPath, sk.name))
		if err != nil {
			return err
		}
		m.Skills = append(m.Skills, skill)
	}
	text := composePrompt(m, title, detail, instructions, role, guide, standards, reviewBase, retry, main, baseline.Policies, baseline.PolicyError)
	if actor == workflow.ArtisanActor {
		text += outputInstructions(m.Identity, revisions, branch)
	}
	prompt := []byte(text)
	if err := os.WriteFile(r.PromptPath, prompt, 0o644); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	m.Prompt = File{Path: PromptFile, Digest: string(digest.Of(prompt))}
	return nil
}

func composePrompt(m *Manifest, title, detail, instructions, role, guide string, standards []string, reviewBase string, retry *Retry, main *Main, policies []string, policyError string) string {
	var b strings.Builder
	id := m.Identity
	fmt.Fprintf(&b, "# Guildhall attempt\n\nRepository: %s\nTask: %s\nStep: %s (%s version %d)\nAttempt: %d\n",
		id.Repository, id.Task, id.Step, id.Kind, id.KindVersion, id.Attempt)
	if id.Kind == "review" {
		if reviewBase == "" {
			reviewBase = "unavailable"
		}
		fmt.Fprintf(&b, "Review base: %s\n", reviewBase)
	}
	if id.Profile != "" {
		fmt.Fprintf(&b, "Profile: %s\n", id.Profile)
	}
	// The title and detail are Director-written, so they are fenced as text and cannot open a section of their own.
	if strings.TrimSpace(title) != "" {
		fmt.Fprintf(&b, "\nTask title:\n\n%s\n", fenced(title))
	}
	if strings.TrimSpace(detail) != "" {
		fmt.Fprintf(&b, "\nTask detail:\n\n%s\n", fenced(detail))
	}
	section := func(title, body string) { fmt.Fprintf(&b, "\n## %s\n\n%s\n", title, strings.TrimRight(body, "\n")) }
	if retry != nil {
		section("Why this is a retry", retrySection(retry))
	}
	if main != nil && (main.Behind > 0 || main.Kept != "") {
		section("Behind main", mainSection(main))
	}
	section("Role", role)
	section("Stage guide", guide)
	if instructions = normalise(instructions); strings.TrimSpace(instructions) != "" {
		section("Instructions", instructions)
	}
	if len(policies) > 0 || strings.TrimSpace(policyError) != "" {
		section("Repository policies", policySection(policies, policyError))
	}
	var list strings.Builder
	if len(m.Inputs) > 0 {
		list.WriteString("The inputs directory beside this prompt holds a verified copy of each input document.\n\n")
	}
	for _, in := range m.Inputs {
		fmt.Fprintf(&list, "- %s: %s/%s (%s)\n", in.Port, InputsDir, in.Path, in.Digest)
	}
	section("Inputs", orNone(list.String()))
	var std strings.Builder
	for i, f := range m.Standards {
		if i > 0 {
			std.WriteString("\n")
		}
		fmt.Fprintf(&std, "### %s\n\n%s\n", f.Path, strings.TrimRight(standards[i], "\n"))
	}
	section("Standards", orNone(std.String()))
	var skills strings.Builder
	if len(m.Skills) > 0 {
		skills.WriteString("The skills directory beside this prompt holds a copy of each skill.\n\n")
	}
	for _, s := range m.Skills {
		fmt.Fprintf(&skills, "- %s: %s (%s)\n", s.Name, s.Path, s.Digest)
	}
	section("Skills", orNone(skills.String()))
	return b.String()
}

// retrySection says which attempt this one retries and quotes what the Director and intake said about it. Each
// quoted text is agent- or Director-written, so it is fenced as text, longer than any backtick run inside it.
func retrySection(retry *Retry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This is a retry of attempt %d, which %s.\n\n", retry.Attempt, retry.Outcome)
	quote := func(label, text, none string) {
		if strings.TrimSpace(text) == "" {
			fmt.Fprintf(&b, "%s %s\n\n", label, none)
			return
		}
		fmt.Fprintf(&b, "%s\n\n%s\n\n", label, fenced(text))
	}
	quote("Director's reason:", retry.Reason, "None given.")
	if strings.TrimSpace(retry.Rejection) != "" {
		quote("Intake rejection:", retry.Rejection, "")
	}
	quote("Previous summary:", retry.Summary, "None recorded.")
	b.WriteString("Address this in your attempt; the stage guide and required outputs still apply.\n")
	return b.String()
}

// policySection introduces the repository's policies and lists them in one text fence, so policy text cannot open
// a section or override the result template; a file that could not be read is reported instead, with its reason
// on one line, and does not stop rendering.
func policySection(policies []string, policyError string) string {
	if reason := strings.TrimSpace(policyError); reason != "" {
		return fmt.Sprintf("The repository policies could not be read: %s. Tell the Director in your result.\n", strings.Join(strings.Fields(reason), " "))
	}
	var items strings.Builder
	for _, p := range policies {
		fmt.Fprintf(&items, "- %s\n", p)
	}
	return "These are the Guildmaster's standing rules for this repository, read from its default branch; they are guidance this stage must follow.\n\n" + fenced(items.String()) + "\n"
}

// Fenced fences text as a text code block, with a fence longer than any backtick run inside it, so a reader
// takes it as data: the engine fences Director-written documents with it too.
func Fenced(text string) string { return fenced(text) }

// fenced fences text as a text code block, with a fence longer than any backtick run inside it.
func fenced(text string) string {
	fence := strings.Repeat("`", max(3, longestRun(text, '`')+1))
	return fmt.Sprintf("%stext\n%s\n%s", fence, strings.TrimRight(normalise(text), "\n"), fence)
}

// mainSection says how far the task branch is behind the default branch, why it was not moved when it was kept,
// and how to take main's changes. The kept reason is git's or the engine's text, so it is fenced as text.
func mainSection(main *Main) string {
	var b strings.Builder
	commits := "commits"
	if main.Behind == 1 {
		commits = "commit"
	}
	fmt.Fprintf(&b, "The task branch is %d %s behind the default branch.\n\n", main.Behind, commits)
	if strings.TrimSpace(main.Kept) != "" {
		fmt.Fprintf(&b, "The branch was not moved to the current main, because:\n\n%s\n\n", fenced(main.Kept))
	}
	b.WriteString("When you need main's changes, run `git fetch` and then `git rebase origin/main`; never use `git merge`.\n")
	return b.String()
}

// longestRun is the length of the longest run of c in s.
func longestRun(s string, c byte) int {
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] != c {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	return longest
}

func orNone(s string) string {
	if s == "" {
		return "None."
	}
	return s
}

// Ask is a typed question for a Guildmaster stage: the text and the options an answer must match.
type Ask struct {
	Question string
	Options  []string
}

// Question renders the decision a Guildmaster stage opens: approval offers "approved" or "changes_requested",
// human review offers every review verdict, and a handoff wait offers "received".
// The text names the task, step and attempt, carries the step's instructions and lists the inputs
// by name and recorded digest; it reads no files and has LF line endings. An approval's question also carries the
// stage's Summary after its first line or, with NoSummary, a line saying the spec has none.
// A stage that is not a Guildmaster stage is refused with ErrWrongActor.
func Question(stage Stage, inputs []Input) (Ask, error) {
	kind, err := stageKind(stage, true)
	if err != nil {
		return Ask{}, err
	}
	var b strings.Builder
	switch kind.Type {
	case "guildmaster-approval":
		b.WriteString("Approval needed")
	case "review", "document-review":
		b.WriteString("Review needed")
	case "handoff-wait":
		var from string
		_ = json.Unmarshal(stage.Step.Settings["from"], &from)
		fmt.Fprintf(&b, "Waiting for the handoff from %s", from)
	default:
		return Ask{}, fmt.Errorf("render: no question for step kind %q", kind.Type)
	}
	fmt.Fprintf(&b, " for %s/%s, step %s (%s version %d), attempt %d.\n",
		stage.Repository, stage.Task, stage.Step.ID, kind.Type, kind.Version, stage.Attempt)
	if kind.Type == "guildmaster-approval" {
		if summary := strings.TrimSpace(normalise(stage.Summary)); summary != "" {
			fmt.Fprintf(&b, "\nPlan summary:\n%s\n", summary)
		} else if stage.NoSummary {
			name := "the spec"
			if i := slices.IndexFunc(inputs, func(in Input) bool { return in.Port == "spec" }); i >= 0 {
				name = slashName(inputs[i].Name)
			}
			fmt.Fprintf(&b, "\nNo summary in this spec; read %s.\n", name)
		}
	}
	if instructions := strings.TrimRight(normalise(stage.Step.Instructions()), "\n"); strings.TrimSpace(instructions) != "" {
		fmt.Fprintf(&b, "\nInstructions:\n%s\n", instructions)
	}
	if len(inputs) > 0 {
		b.WriteString("\nInputs:\n")
		for _, in := range inputs {
			fmt.Fprintf(&b, "- %s: %s (sha256:%s)\n", in.Port, slashName(in.Name), in.SHA256)
		}
	}
	options := questionOptions[kind.Type]
	fmt.Fprintf(&b, "\nAnswer with one of: %s.\n", strings.Join(options, ", "))
	return Ask{Question: normalise(b.String()), Options: slices.Clone(options)}, nil
}

// DesignOptions are the answers a design round's decision offers.
var DesignOptions = []string{string(workflow.VerdictApproved), string(workflow.VerdictChangesRequested)}

// DesignQuestion renders the decision a design round opens, decision id, round n of the task's design rounds, once its
// designer attempt wrote the design: it says what each answer does and lists the inputs, the brief and the design,
// by name and recorded digest. It reads no files and has LF line endings.
func DesignQuestion(repository, task, id string, n int, inputs []Input) Ask {
	var b strings.Builder
	fmt.Fprintf(&b, "Design decision needed for %s/%s, design round %d, decision %s.\n", repository, task, n, id)
	b.WriteString("\napproved makes the design an input to plan, which runs again if it already ran; changes_requested reopens the grill with your answer.\n")
	if len(inputs) > 0 {
		b.WriteString("\nInputs:\n")
		for _, in := range inputs {
			fmt.Fprintf(&b, "- %s: %s (sha256:%s)\n", in.Port, slashName(in.Name), in.SHA256)
		}
	}
	fmt.Fprintf(&b, "\nAnswer with one of: %s.\n", strings.Join(DesignOptions, ", "))
	return Ask{Question: normalise(b.String()), Options: slices.Clone(DesignOptions)}
}

var questionOptions = map[string][]string{
	"guildmaster-approval": {string(workflow.VerdictApproved), string(workflow.VerdictChangesRequested)},
	"review":               {string(workflow.VerdictApproved), string(workflow.VerdictChangesRequested), string(workflow.VerdictRejected)},
	"document-review":      {string(workflow.VerdictApproved), string(workflow.VerdictChangesRequested), string(workflow.VerdictRejected)},
	"handoff-wait":         {"received"},
}

// DecisionOptions returns the option sets of the decisions render's questions open, keyed by the step kind that opens
// each ("guildmaster-approval", "review", "document-review", "handoff-wait") and "design" for a design round's, so
// a reader such as the task page's option-meanings table can check it covers every one (docs/contracts/web.md).
// Each slice is a copy.
func DecisionOptions() map[string][]string {
	out := map[string][]string{"design": slices.Clone(DesignOptions)}
	for kind, opts := range questionOptions {
		out[kind] = slices.Clone(opts)
	}
	return out
}
