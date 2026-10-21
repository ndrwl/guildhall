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
	// Step is the step from the task's bound workflow revision; its kind and settings decide the actor,
	// and its "profile", "skills" and "instructions" settings are rendered.
	Step workflow.Step
	// Attempt is the attempt number, starting at 1.
	Attempt int
	// Dir is the attempt directory Prompt writes into; it must not exist or be empty.
	// Question ignores it.
	Dir string
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
}

// Baseline is the pinned prose a prompt is built from.
type Baseline struct {
	// Role is the role prompt: the profile's role, or the release's Artisan role.
	Role Source
	// Guide is the stage guide for the step's kind.
	Guide Source
	// Standards are rendered in order, in full, because a reviewer measures against these exact words.
	Standards []Source
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
	if err := r.write(&m, inputData, found, stage.Step.Instructions(), role, guide, standards); err != nil {
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

func (r Rendered) write(m *Manifest, inputData [][]byte, found []foundSkill, instructions, role, guide string, standards []string) error {
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
	prompt := []byte(composePrompt(m, instructions, role, guide, standards))
	if err := os.WriteFile(r.PromptPath, prompt, 0o644); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	m.Prompt = File{Path: PromptFile, Digest: string(digest.Of(prompt))}
	return nil
}

func composePrompt(m *Manifest, instructions, role, guide string, standards []string) string {
	var b strings.Builder
	id := m.Identity
	fmt.Fprintf(&b, "# Guildhall attempt\n\nRepository: %s\nTask: %s\nStep: %s (%s version %d)\nAttempt: %d\n",
		id.Repository, id.Task, id.Step, id.Kind, id.KindVersion, id.Attempt)
	if id.Profile != "" {
		fmt.Fprintf(&b, "Profile: %s\n", id.Profile)
	}
	section := func(title, body string) { fmt.Fprintf(&b, "\n## %s\n\n%s\n", title, strings.TrimRight(body, "\n")) }
	section("Role", role)
	section("Stage guide", guide)
	if instructions = normalise(instructions); strings.TrimSpace(instructions) != "" {
		section("Instructions", instructions)
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
// by name and recorded digest; it reads no files and has LF line endings.
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

var questionOptions = map[string][]string{
	"guildmaster-approval": {string(workflow.VerdictApproved), string(workflow.VerdictChangesRequested)},
	"review":               {string(workflow.VerdictApproved), string(workflow.VerdictChangesRequested), string(workflow.VerdictRejected)},
	"document-review":      {string(workflow.VerdictApproved), string(workflow.VerdictChangesRequested), string(workflow.VerdictRejected)},
	"handoff-wait":         {"received"},
}
