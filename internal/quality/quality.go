// Package quality runs the judge session of the quality rubric for a delivered task and measures its agreement with
// the Guildmaster's audit (docs/quality.md#running-it, docs/decisions/quality-judge.md); docs/contracts/quality.md
// owns its contract.
package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/strictjson"
	"github.com/ndrwl/guildhall/internal/workflow"
)

var (
	// ErrSelfReview reports a self-review document the judge cannot be given.
	ErrSelfReview = errors.New("self-review document refused")
	// ErrAnswer reports a judge's judgement.json that breaks its contract.
	ErrAnswer = errors.New("judgement refused")
	// ErrJudgement reports a recorded judgement that cannot be read.
	ErrJudgement = errors.New("recorded judgement refused")
	// ErrNotDelivered reports a task that is not done, which no judge attempt starts on.
	ErrNotDelivered = errors.New("task is not delivered")
	// ErrJudgeRunning reports a judge attempt of the task that has not finished.
	ErrJudgeRunning = errors.New("a judge attempt is running")
	// ErrNotJudge reports an attempt that is not a judge attempt of the task.
	ErrNotJudge = errors.New("no such judge attempt")
)

// AnswerFile is the file the judge writes in its working directory and names as its result's first output.
const AnswerFile = "judgement.json"

// Criterion kinds a judge scores or passes.
const (
	KindScore = "score"
	KindGate  = "gate"
)

// SelfReview is what the judge is given of a self-review document.
type SelfReview struct {
	Repository string
	Task       string
	Number     int
	Head       string
	// Pending maps each criterion whose judge is pending to its kind, KindScore or KindGate.
	Pending map[string]string
}

// selfReviewSchema is the self-review document's schema (skills/guildhall-self-review/SKILL.md).
const selfReviewSchema = "guildhall.self-review/1"

// criterionID is a rubric criterion id such as B2 (docs/quality.md).
var criterionID = regexp.MustCompile(`^[A-H][1-9]$`)

// ReadSelfReview parses a guildhall.self-review/1 document, which a model writes, reading only the fields the judge
// needs. See docs/contracts/quality.md.
func ReadSelfReview(data []byte) (SelfReview, error) {
	var doc struct {
		Schema     string `json:"schema"`
		Repository string `json:"repository"`
		Task       string `json:"task"`
		Number     int    `json:"number"`
		Head       string `json:"head"`
		Criteria   []struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			Judge string `json:"judge"`
		} `json:"criteria"`
	}
	refuse := func(format string, args ...any) (SelfReview, error) {
		return SelfReview{}, fmt.Errorf("%w: %s", ErrSelfReview, fmt.Sprintf(format, args...))
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return refuse("%v", err)
	}
	switch {
	case doc.Schema != selfReviewSchema:
		return refuse("schema %q, want %q", doc.Schema, selfReviewSchema)
	case !workflow.ValidID(doc.Repository) || !workflow.ValidID(doc.Task):
		return refuse("repository %q and task %q must be ids", doc.Repository, doc.Task)
	case doc.Number < 1:
		return refuse("number %d must be positive", doc.Number)
	case !digest.ValidHead(doc.Head):
		return refuse("head %q is not a 40 or 64 lowercase hex object id", doc.Head)
	}
	sr := SelfReview{Repository: doc.Repository, Task: doc.Task, Number: doc.Number, Head: doc.Head, Pending: map[string]string{}}
	for _, c := range doc.Criteria {
		if c.Judge != "pending" {
			continue
		}
		if !criterionID.MatchString(c.ID) || (c.Kind != KindScore && c.Kind != KindGate) {
			return refuse("pending criterion %q of kind %q is not a rubric score or gate", c.ID, c.Kind)
		}
		sr.Pending[c.ID] = c.Kind
	}
	return sr, nil
}

// pendingIDs are the pending criteria in order.
func (sr SelfReview) pendingIDs() []string {
	ids := make([]string, 0, len(sr.Pending))
	for id := range sr.Pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Answer is the judge's judgement.json.
type Answer struct {
	// MergeAsIs answers "would you merge this as is": "yes" or "no".
	MergeAsIs string     `json:"merge_as_is"`
	Criteria  []Judgment `json:"criteria"`
}

// Judgment is the judge's score or pass for one criterion, with the evidence it quotes.
type Judgment struct {
	ID       string `json:"id"`
	Score    *int   `json:"score,omitempty"`
	Pass     *bool  `json:"pass,omitempty"`
	Evidence string `json:"evidence"`
}

// maxAnswer bounds a judge's answer, as a result file is bounded.
const maxAnswer = 1 << 20

// ParseAnswer parses a judge's judgement.json against the pending criteria of its self-review.
func ParseAnswer(data []byte, pending map[string]string) (Answer, error) {
	refuse := func(format string, args ...any) (Answer, error) {
		return Answer{}, fmt.Errorf("%w: %s", ErrAnswer, fmt.Sprintf(format, args...))
	}
	var a Answer
	if err := strictjson.Decode(data, &a, strictjson.Options{MaxBytes: maxAnswer}); err != nil {
		return refuse("%v", err)
	}
	if a.MergeAsIs != "yes" && a.MergeAsIs != "no" {
		return refuse(`merge_as_is %q must be "yes" or "no"`, a.MergeAsIs)
	}
	seen := map[string]bool{}
	for _, c := range a.Criteria {
		kind, ok := pending[c.ID]
		switch {
		case !ok:
			return refuse("criterion %q is not pending in the self-review", c.ID)
		case seen[c.ID]:
			return refuse("criterion %s is judged twice", c.ID)
		case kind == KindScore && (c.Score == nil || c.Pass != nil || *c.Score < 0 || *c.Score > 3):
			return refuse("criterion %s is a score and needs a score from 0 to 3 and no pass", c.ID)
		case kind == KindGate && (c.Pass == nil || c.Score != nil):
			return refuse("criterion %s is a gate and needs a pass and no score", c.ID)
		case strings.TrimSpace(c.Evidence) == "":
			return refuse("criterion %s quotes no evidence", c.ID)
		}
		seen[c.ID] = true
	}
	for _, id := range (SelfReview{Pending: pending}).pendingIDs() {
		if !seen[id] {
			return refuse("criterion %s is not judged", id)
		}
	}
	return a, nil
}

// JudgementSchema is the schema of the record Guildhall writes from an accepted answer.
const (
	JudgementSchema  = judgementPrefix + "1"
	judgementPrefix  = "guildhall.judgement/"
	judgementVersion = 1
)

// Judgement is the recorded document quality-judge-<n>-judgement.
type Judgement struct {
	Schema     string             `json:"schema"`
	Repository string             `json:"repository"`
	Task       string             `json:"task"`
	Review     int                `json:"review"`
	Head       string             `json:"head"`
	Attempt    int                `json:"attempt"`
	Judge      config.JudgeChoice `json:"judge"`
	MergeAsIs  string             `json:"merge_as_is"`
	Criteria   []Judgment         `json:"criteria"`
}

// ParseJudgement parses a recorded judgement.
func ParseJudgement(data []byte) (Judgement, error) {
	var j Judgement
	if err := strictjson.Decode(data, &j, strictjson.Options{Schema: judgementPrefix, Version: judgementVersion, MaxBytes: maxAnswer * 2}); err != nil {
		return Judgement{}, fmt.Errorf("%w: %w", ErrJudgement, err)
	}
	return j, nil
}

// Launcher starts a judge attempt from its files; launch.Process implements it.
type Launcher interface {
	LaunchFiles(ctx context.Context, k artisan.AttemptKey, adapterName, promptFile, workDir string) (interp.Handle, error)
}

// Judge starts and takes the judge attempts of delivered tasks.
type Judge struct {
	Records  interp.Records
	Outbox   interp.Outbox
	Launcher Launcher
	// Config gives the quality settings and the profiles.
	Config *config.Config
	// DefaultAdapter runs a judge when no judge profile is listed, and ran a build attempt rendered for no profile.
	DefaultAdapter string
	// StateDir holds attempt directories, <repo>/<task>/<step>-<n>, and DocumentDir the recorded documents.
	StateDir, DocumentDir string
	// QualityDir holds the self-review documents, <repo>/<task>/self-review-<n>.json, and their brief, diff and facts.
	QualityDir string
	// Role is the judge's role prompt file, which a judge profile's own role replaces.
	Role string
	// ProfileRole returns the absolute role file of a profile that names one, "" for none; nil is none.
	ProfileRole func(profile string) string
	// Caps are the attempt caps the judge attempt starts under.
	Caps store.AttemptCaps
}

// Started is the judge attempt Start launched.
type Started struct {
	Attempt int                `json:"attempt"`
	Review  int                `json:"review"`
	Judge   config.JudgeChoice `json:"judge"`
}

// The files of a judge attempt's directory (docs/contracts/quality.md).
const (
	promptDir    = "prompt"
	partialDir   = "prompt.partial"
	choiceFile   = "judge.json"
	workDir      = "work"
	launchedFile = "launched.json"
)

// inputs are the self-review files a judge is given: each <self-review-n><suffix> in the quality directory is copied
// to inputs/<name>; the document and brief are required.
var inputs = []struct {
	suffix, name string
	required     bool
}{
	{".json", "self-review.json", true},
	{"-judge.md", "judge.md", true},
	{".diff", "change.diff", false},
	{".facts", "facts.txt", false},
}

// prepared is a published judge preparation's judge.json.
type prepared struct {
	Review int                `json:"review"`
	Judge  config.JudgeChoice `json:"judge"`
}

// Start starts the judge of self-review review of the delivered task. See docs/contracts/quality.md.
func (j *Judge) Start(ctx context.Context, repository, task string, review int) (Started, error) {
	t, err := j.Records.Task(repository, task)
	if err != nil {
		return Started{}, err
	}
	if t.State != store.TaskDone {
		return Started{}, fmt.Errorf("%w: %s/%s is %s", ErrNotDelivered, repository, task, t.State)
	}
	attempts, err := j.Records.Attempts(repository, task)
	if err != nil {
		return Started{}, err
	}
	judges := slices.DeleteFunc(slices.Clone(attempts), func(a store.StageAttempt) bool { return a.Step != workflow.JudgeStep })
	if n := len(judges); n > 0 && judges[n-1].Outcome == "" {
		k := j.key(repository, task, judges[n-1].Number)
		if _, err := os.Stat(filepath.Join(k.Dir(j.StateDir), launchedFile)); err == nil {
			return Started{}, fmt.Errorf("%w: attempt %d of %s/%s", ErrJudgeRunning, k.Attempt, repository, task)
		}
		// Recorded but never marked launched: the start stopped between the two, so it launches the same attempt.
		p, err := j.prepare(k, review, func() (config.JudgeChoice, error) { return j.choose(repository, task, review, attempts) })
		if err != nil {
			return Started{}, err
		}
		return j.launch(ctx, k, p)
	}
	if _, err := j.selfReview(repository, task, review); err != nil {
		return Started{}, err
	}
	choice, err := j.choose(repository, task, review, attempts)
	if err != nil {
		return Started{}, err
	}
	n := 1
	if len(judges) > 0 {
		n = judges[len(judges)-1].Number + 1
	}
	rc, err := j.Records.StartCappedAttempt(mutation("quality-judge-attempt", repository, task, strconv.Itoa(n)), repository, task, workflow.JudgeStep, j.Caps)
	if err != nil {
		return Started{}, err
	}
	if rc.Attempt != n {
		return Started{}, fmt.Errorf("recorded judge attempt %d, expected %d: another writer started one", rc.Attempt, n)
	}
	k := j.key(repository, task, n)
	p, err := j.prepare(k, review, func() (config.JudgeChoice, error) { return choice, nil })
	if err != nil {
		return Started{}, err
	}
	return j.launch(ctx, k, p)
}

func (j *Judge) key(repository, task string, n int) artisan.AttemptKey {
	return artisan.AttemptKey{Repository: repository, TaskID: task, Step: workflow.JudgeStep, Attempt: n}
}

// selfReview reads self-review review of the task from the quality directory and checks it is that one and has
// something pending.
func (j *Judge) selfReview(repository, task string, review int) (SelfReview, error) {
	data, err := os.ReadFile(j.reviewFile(repository, task, review, ".json"))
	if err != nil {
		return SelfReview{}, fmt.Errorf("%w: %w", ErrSelfReview, err)
	}
	sr, err := ReadSelfReview(data)
	switch {
	case err != nil:
		return SelfReview{}, err
	case sr.Repository != repository || sr.Task != task || sr.Number != review:
		return SelfReview{}, fmt.Errorf("%w: self-review-%d.json is self-review %d of %s/%s", ErrSelfReview, review, sr.Number, sr.Repository, sr.Task)
	case len(sr.Pending) == 0:
		return SelfReview{}, fmt.Errorf("%w: self-review %d of %s/%s has no pending criterion", ErrSelfReview, review, repository, task)
	}
	return sr, nil
}

func (j *Judge) reviewFile(repository, task string, review int, suffix string) string {
	return filepath.Join(j.QualityDir, repository, task, "self-review-"+strconv.Itoa(review)+suffix)
}

// choose chooses the judge of the review, passing over each profile an earlier judge attempt of the same review
// failed under.
func (j *Judge) choose(repository, task string, review int, attempts []store.StageAttempt) (config.JudgeChoice, error) {
	q, err := j.Config.Quality()
	if err != nil {
		return config.JudgeChoice{}, err
	}
	var failed []string
	for _, a := range attempts {
		if a.Step != workflow.JudgeStep || a.Outcome != store.AttemptFailed {
			continue
		}
		if p, err := readPrepared(j.key(repository, task, a.Number).Dir(j.StateDir)); err == nil && p.Review == review && p.Judge.Profile != "" {
			failed = append(failed, p.Judge.Profile)
		}
	}
	return q.Judge(j.Config.Profiles, j.builder(repository, task, attempts), j.DefaultAdapter, failed)
}

// builder is the adapter of the task's latest succeeded build attempt, from the profile its prompt manifest names,
// or "" when no such attempt or profile is found.
func (j *Judge) builder(repository, task string, attempts []store.StageAttempt) string {
	for _, a := range slices.Backward(attempts) {
		if a.Outcome != store.AttemptSucceeded || a.Step == workflow.JudgeStep {
			continue
		}
		k := artisan.AttemptKey{Repository: repository, TaskID: task, Step: a.Step, Attempt: a.Number}
		data, err := os.ReadFile(filepath.Join(k.Dir(j.StateDir), promptDir, "manifest.json"))
		if err != nil {
			continue
		}
		var m render.Manifest
		if json.Unmarshal(data, &m) != nil || m.Identity.Kind != "build" {
			continue
		}
		if m.Identity.Profile == "" {
			return j.DefaultAdapter
		}
		return j.Config.Profiles[m.Identity.Profile].Adapter
	}
	return ""
}

func readPrepared(dir string) (prepared, error) {
	var p prepared
	data, err := os.ReadFile(filepath.Join(dir, promptDir, choiceFile))
	if err != nil {
		return p, err
	}
	return p, strictjson.Decode(data, &p, strictjson.Options{})
}

// prepare returns attempt k's published preparation, or renders and publishes it for review with the judge choose
// returns when none was published; a published one is never rendered again.
func (j *Judge) prepare(k artisan.AttemptKey, review int, choose func() (config.JudgeChoice, error)) (prepared, error) {
	dir := k.Dir(j.StateDir)
	published := filepath.Join(dir, promptDir)
	if _, err := os.Lstat(published); err == nil {
		return readPrepared(dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return prepared{}, err
	}
	sr, err := j.selfReview(k.Repository, k.TaskID, review)
	if err != nil {
		return prepared{}, err
	}
	choice, err := choose()
	if err != nil {
		return prepared{}, err
	}
	partial := filepath.Join(dir, partialDir)
	if err := os.RemoveAll(partial); err != nil {
		return prepared{}, err
	}
	var brief []byte
	for _, in := range inputs {
		data, err := os.ReadFile(j.reviewFile(k.Repository, k.TaskID, review, in.suffix))
		if errors.Is(err, fs.ErrNotExist) && !in.required {
			continue
		}
		if err != nil {
			return prepared{}, fmt.Errorf("%w: %w", ErrSelfReview, err)
		}
		if in.name == "judge.md" {
			brief = data
		}
		if err := writeFile(filepath.Join(partial, render.InputsDir, in.name), data); err != nil {
			return prepared{}, err
		}
	}
	role := j.Role
	if choice.Profile != "" && j.ProfileRole != nil {
		if r := j.ProfileRole(choice.Profile); r != "" {
			role = r
		}
	}
	roleText, err := os.ReadFile(role)
	if err != nil {
		return prepared{}, fmt.Errorf("judge role: %w", err)
	}
	if err := writeFile(filepath.Join(partial, render.PromptFile), prompt(sr, roleText, brief)); err != nil {
		return prepared{}, err
	}
	p := prepared{Review: review, Judge: choice}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return prepared{}, err
	}
	if err := writeFile(filepath.Join(partial, choiceFile), append(data, '\n')); err != nil {
		return prepared{}, err
	}
	if err := os.Rename(partial, published); err != nil {
		return prepared{}, err
	}
	return p, nil
}

// prompt is the judge's prompt: its role, the brief, and what to write.
func prompt(sr SelfReview, role, brief []byte) []byte {
	var b strings.Builder
	b.Write(role)
	if len(role) > 0 && role[len(role)-1] != '\n' {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\n# Judge self-review %d of %s/%s\n\n", sr.Number, sr.Repository, sr.Task)
	fmt.Fprintf(&b, "You judge the change merged at head %s; you are neither its builder nor the Director.\n", sr.Head)
	b.WriteString("The self-review document, this brief and, when they exist, the change's diff and facts are in the `" + render.InputsDir + "` directory beside this prompt.\n")
	b.WriteString("\n## Brief\n\n")
	b.Write(brief)
	if len(brief) > 0 && brief[len(brief)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("\n## Your judgement\n\n")
	fmt.Fprintf(&b, "Write `%s` in your working directory and name it as your result's one output.\n", AnswerFile)
	b.WriteString("It is one JSON object: `merge_as_is`, `\"yes\"` or `\"no\"`, answering whether you would merge this change as it is, and `criteria`, one entry for each criterion below and no other.\n")
	b.WriteString("A score has `id`, a whole `score` from 0 to 3 against the rubric's anchors and `evidence`; a gate has `id`, a boolean `pass` and `evidence`.\n")
	b.WriteString("Evidence quotes the `file:line`, record or command output it rests on.\n\n")
	for _, id := range sr.pendingIDs() {
		fmt.Fprintf(&b, "- %s: %s\n", id, sr.Pending[id])
	}
	b.WriteString("\n```json\n{\"merge_as_is\": \"yes\", \"criteria\": [{\"id\": \"B2\", \"score\": 2, \"evidence\": \"internal/x/x_test.go:12 names its cases\"}, {\"id\": \"G2\", \"pass\": true, \"evidence\": \"no assertion is removed\"}]}\n```\n")
	return []byte(b.String())
}

// launch launches attempt k from its preparation in its empty working directory and marks it launched.
func (j *Judge) launch(ctx context.Context, k artisan.AttemptKey, p prepared) (Started, error) {
	dir := k.Dir(j.StateDir)
	work := filepath.Join(dir, workDir)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return Started{}, err
	}
	h, err := j.Launcher.LaunchFiles(ctx, k, p.Judge.Adapter, filepath.Join(dir, promptDir, render.PromptFile), work)
	if err != nil {
		return Started{}, fmt.Errorf("launch judge attempt %d: %w", k.Attempt, err)
	}
	data, err := json.Marshal(h)
	if err != nil {
		return Started{}, err
	}
	if err := writeFile(filepath.Join(dir, launchedFile), data); err != nil {
		return Started{}, err
	}
	return Started{Attempt: k.Attempt, Review: p.Review, Judge: p.Judge}, nil
}

// Taken is what Take found for a finished judge attempt.
type Taken struct {
	Attempt int `json:"attempt"`
	// Document is the recorded judgement's name; empty when nothing was recorded.
	Document string `json:"document,omitempty"`
	// Refused is why the judge's answer was refused, or why the attempt recorded nothing.
	Refused string `json:"refused,omitempty"`
}

// Take takes finished judge attempt n of the task. See docs/contracts/quality.md.
func (j *Judge) Take(repository, task string, attempt int) (Taken, error) {
	attempts, err := j.Records.Attempts(repository, task)
	if err != nil {
		return Taken{}, err
	}
	i := slices.IndexFunc(attempts, func(a store.StageAttempt) bool { return a.Step == workflow.JudgeStep && a.Number == attempt })
	if i < 0 {
		return Taken{}, fmt.Errorf("%w: attempt %d of %s/%s", ErrNotJudge, attempt, repository, task)
	}
	a := attempts[i]
	if a.Outcome == "" {
		return Taken{}, fmt.Errorf("%w: attempt %d of %s/%s", ErrJudgeRunning, attempt, repository, task)
	}
	k := j.key(repository, task, attempt)
	dir := k.Dir(j.StateDir)
	p, err := readPrepared(dir)
	if err != nil {
		return Taken{}, fmt.Errorf("judge attempt %d's preparation: %w", attempt, err)
	}
	taken := Taken{Attempt: attempt}
	name := k.AttemptID() + "-judgement"
	docs, err := j.Records.Documents(repository, task)
	if err != nil {
		return Taken{}, err
	}
	if slices.ContainsFunc(docs, func(d store.DocumentRef) bool { return d.Name == name }) {
		taken.Document = name
		return taken, nil
	}
	who := fmt.Sprintf("judge attempt %d of self-review %d of %s/%s, %s", attempt, p.Review, repository, task, describe(p.Judge))
	if a.Outcome != store.AttemptSucceeded {
		taken.Refused = fmt.Sprintf("the attempt ended %s", a.Outcome)
		text := fmt.Sprintf("The %s ended %s and recorded no judgement; guildhall quality judge --repo %s --review %d %s starts the next judge", who, a.Outcome, repository, p.Review, task)
		if p.Judge.Profile != "" {
			text += ", passing over profile " + p.Judge.Profile
		}
		return taken, j.notify(repository, task, "judge-failed."+k.AttemptID(), "judge-failed", text+".")
	}
	answer, sr, err := j.answer(k)
	if err != nil {
		taken.Refused = err.Error()
		return taken, j.notify(repository, task, "judgement-refused."+k.AttemptID(), "judgement-refused",
			fmt.Sprintf("The %s wrote a judgement Guildhall refused, so nothing was recorded: %v.", who, err))
	}
	rec := Judgement{Schema: JudgementSchema, Repository: repository, Task: task, Review: p.Review, Head: sr.Head, Attempt: attempt,
		Judge: p.Judge, MergeAsIs: answer.MergeAsIs, Criteria: answer.Criteria}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return Taken{}, err
	}
	data = append(data, '\n')
	d := store.DocumentRef{Repository: repository, TaskID: task, Name: name,
		Path: path.Join("tasks", repository, task, k.AttemptID(), AnswerFile), SHA256: digest.Of(data).Hex()}
	if err := writeFile(filepath.Join(j.DocumentDir, filepath.FromSlash(d.Path)), data); err != nil {
		return Taken{}, err
	}
	if _, err := j.Records.PutDocument(mutation("quality-judgement", repository, task, k.AttemptID()), d); err != nil {
		return Taken{}, err
	}
	taken.Document = name
	return taken, j.notify(repository, task, "judgement."+k.AttemptID(), "judgement",
		fmt.Sprintf("The %s would merge it as is: %s; it judged %s. The judgement is document %s.",
			who, answer.MergeAsIs, strings.Join(sr.pendingIDs(), ", "), name))
}

// answer reads and checks the judge's answer against the self-review it was given.
func (j *Judge) answer(k artisan.AttemptKey) (Answer, SelfReview, error) {
	dir := k.Dir(j.StateDir)
	res, err := adapter.ReadResult(filepath.Join(dir, artisan.ResultFile), adapter.Identity{TaskID: k.TaskID, AttemptID: k.AttemptID()})
	if err != nil {
		return Answer{}, SelfReview{}, err
	}
	if len(res.Outputs) == 0 || res.Outputs[0].Path != AnswerFile {
		return Answer{}, SelfReview{}, fmt.Errorf("%w: the result's first output is not %s", ErrAnswer, AnswerFile)
	}
	given, err := os.ReadFile(filepath.Join(dir, promptDir, render.InputsDir, "self-review.json"))
	if err != nil {
		return Answer{}, SelfReview{}, err
	}
	sr, err := ReadSelfReview(given)
	if err != nil {
		return Answer{}, SelfReview{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, workDir, AnswerFile))
	if err != nil {
		return Answer{}, SelfReview{}, fmt.Errorf("%w: %w", ErrAnswer, err)
	}
	a, err := ParseAnswer(data, sr.Pending)
	return a, sr, err
}

// describe names the judge and whether its family was separate.
func describe(c config.JudgeChoice) string {
	who := "on the default adapter " + c.Adapter
	if c.Profile != "" {
		who = fmt.Sprintf("profile %s on adapter %s", c.Profile, c.Adapter)
	}
	if c.SeparateFamily {
		return who + ", a separate family from the builder's"
	}
	return who + ", not a separate family: " + c.Reason
}

// notify sends the Director a message under an id derived from what it is about, so a repeat sends nothing new.
func (j *Judge) notify(repository, task, what, typ, text string) error {
	id := fmt.Sprintf("%s.%s.%s", repository, task, what)
	if len(id) > 100 {
		id = "quality." + digest.Of([]byte(id)).Hex()
	}
	_, err := j.Outbox.Send(store.Recipient{Role: store.RecipientDirector}, messaging.Envelope{
		Schema: messaging.SchemaPrefix + strconv.Itoa(messaging.SchemaVersion), ID: id, Recipient: store.RecipientDirector,
		Repository: repository, Task: task, Type: typ, Text: text,
	})
	if errors.Is(err, store.ErrMutationConflict) {
		return nil
	}
	return err
}

// mutation derives a write's mutation id from the task and the record, hashing ids longer than the store allows.
func mutation(kind string, parts ...string) string {
	id := strings.Join(append([]string{kind}, parts...), ":")
	if len(id) <= 128 {
		return id
	}
	return kind + ":" + digest.Of([]byte(id)).Hex()
}

// writeFile replaces name with data atomically, creating its directory.
func writeFile(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(name, data)
}

// AgreementRecords is what Agreement reads.
type AgreementRecords interface {
	Tasks(repository string) ([]store.Task, error)
	Documents(repository, taskID string) ([]store.DocumentRef, error)
	Decisions(repository, taskID string) ([]store.Decision, error)
}

// Pair is one task's judge verdict and audit answer.
type Pair struct {
	Repository     string `json:"repository"`
	Task           string `json:"task"`
	Audit          string `json:"audit"`
	Judge          string `json:"judge"`
	Agree          bool   `json:"agree"`
	SeparateFamily bool   `json:"separate_family"`
}

// Count is how many pairs agreed.
type Count struct {
	Paired int `json:"paired"`
	Agreed int `json:"agreed"`
}

// Report is the judge's agreement with the Guildmaster's audit.
type Report struct {
	Audited        int    `json:"audited"`
	Judged         int    `json:"judged"`
	Pairs          []Pair `json:"pairs"`
	Total          Count  `json:"total"`
	SeparateFamily Count  `json:"separate_family"`
	SameFamily     Count  `json:"same_family"`
}

// judgementName matches a recorded judgement's document name and captures its attempt number.
var judgementName = regexp.MustCompile(`^` + workflow.JudgeStep + `-([1-9][0-9]*)-judgement$`)

// Agreement pairs each task's latest judgement with its answered audit. See docs/contracts/quality.md.
func Agreement(records AgreementRecords, documentDir string, repositories []string) (Report, error) {
	rep := Report{Pairs: []Pair{}}
	for _, repo := range repositories {
		tasks, err := records.Tasks(repo)
		if err != nil {
			return Report{}, err
		}
		for _, t := range tasks {
			docs, err := records.Documents(repo, t.ID)
			if err != nil {
				return Report{}, err
			}
			var latest *store.DocumentRef
			best := 0
			for i, d := range docs {
				if m := judgementName.FindStringSubmatch(d.Name); m != nil {
					if n, _ := strconv.Atoi(m[1]); n > best {
						best, latest = n, &docs[i]
					}
				}
			}
			decisions, err := records.Decisions(repo, t.ID)
			if err != nil {
				return Report{}, err
			}
			i := slices.IndexFunc(decisions, func(d store.Decision) bool {
				return d.Kind == store.DecisionAudit && d.Answered && d.Choice != ""
			})
			if latest != nil {
				rep.Judged++
			}
			if i >= 0 {
				rep.Audited++
			}
			if latest == nil || i < 0 {
				continue
			}
			data, err := os.ReadFile(filepath.Join(documentDir, filepath.FromSlash(latest.Path)))
			if err != nil {
				return Report{}, err
			}
			if digest.Of(data).Hex() != latest.SHA256 {
				return Report{}, fmt.Errorf("%w: %s of %s/%s does not have its recorded digest", ErrJudgement, latest.Name, repo, t.ID)
			}
			j, err := ParseJudgement(data)
			if err != nil {
				return Report{}, fmt.Errorf("%s of %s/%s: %w", latest.Name, repo, t.ID, err)
			}
			p := Pair{Repository: repo, Task: t.ID, Audit: decisions[i].Choice, Judge: j.MergeAsIs, SeparateFamily: j.Judge.SeparateFamily}
			p.Agree = p.Audit == p.Judge
			rep.Pairs = append(rep.Pairs, p)
			family := &rep.SameFamily
			if p.SeparateFamily {
				family = &rep.SeparateFamily
			}
			for _, c := range []*Count{&rep.Total, family} {
				c.Paired++
				if p.Agree {
					c.Agreed++
				}
			}
		}
	}
	return rep, nil
}
