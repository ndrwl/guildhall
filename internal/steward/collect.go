package steward

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Schemas of collection's documents: the receipt the Steward copies to a Director copy, what `director collected`
// prints, and the collected lesson and incident files in the private config.
const (
	CollectReceiptSchema     = "guildhall.collect-receipt/1"
	CollectedReportSchema    = "guildhall.collected-report/1"
	CollectedLessonSchema    = "guildhall.collected-lesson/1"
	CollectedIncidentSchema  = "guildhall.collected-incident/1"
	collectedLessonFrontLine = "---"
)

// Triage states of a collected item in the private config: pending until the Guildmaster answers its decision.
const (
	TriagePending = "pending"
)

// Statuses `director collected` reports for each receipt item.
const (
	// MarkCollected is an item marked collected now, and MarkAlready one marked by an earlier receipt.
	MarkCollected = "collected"
	MarkAlready   = "already-collected"
	// MarkMismatch is an item whose content digest or origin is not the copy's, which stays pending.
	MarkMismatch = "mismatch"
	// MarkMissing is an item the copy does not hold.
	MarkMissing = "missing"
)

// ErrBusy reports a collection round that found .rollout.lock held, by a rollout or another round, and changed
// nothing.
var ErrBusy = errors.New("busy: the private config's .rollout.lock is held by a rollout or another collection")

// CollectReceipt is what the Steward copies to CollectReceiptPath on a Director copy before `director collected`:
// each item it committed to the private config, by origin, id and content digest, and the commit that holds it.
type CollectReceipt struct {
	Schema string        `json:"schema"`
	Items  []ReceiptItem `json:"items"`
}

// ReceiptItem is one item of a CollectReceipt.
type ReceiptItem struct {
	Kind   store.QueueKind `json:"kind"`
	Origin string          `json:"origin"`
	ID     string          `json:"id"`
	Digest string          `json:"digest"`
	Commit string          `json:"commit"`
}

// ParseCollectReceipt decodes and checks a receipt strictly.
func ParseCollectReceipt(data []byte) (*CollectReceipt, error) {
	var r CollectReceipt
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("collect receipt: %w", err)
	}
	if r.Schema != CollectReceiptSchema {
		return nil, fmt.Errorf("collect receipt: schema %q, want %s", r.Schema, CollectReceiptSchema)
	}
	for i, it := range r.Items {
		if it.Kind != store.QueueLesson && it.Kind != store.QueueIncident {
			return nil, fmt.Errorf("collect receipt: item %d: kind %q", i+1, it.Kind)
		}
		if !originPattern.MatchString(it.Origin) || !workflow.ValidID(it.ID) {
			return nil, fmt.Errorf("collect receipt: item %d: origin %q or id %q is not valid", i+1, it.Origin, it.ID)
		}
		if _, err := digest.Parse(it.Digest); err != nil || !digest.ValidHead(it.Commit) {
			return nil, fmt.Errorf("collect receipt: item %d: digest %q or commit %q is not valid", i+1, it.Digest, it.Commit)
		}
	}
	return &r, nil
}

// CollectedReport is what `director collected` prints: each receipt item's status.
type CollectedReport struct {
	Schema string         `json:"schema"`
	Items  []MarkedResult `json:"items"`
}

// MarkedResult is one receipt item's status in a CollectedReport, with the reason for any status but collected.
type MarkedResult struct {
	Kind   store.QueueKind `json:"kind"`
	Origin string          `json:"origin"`
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Error  string          `json:"error,omitempty"`
}

// originPattern is a queue origin: 16 lowercase hex digits (store.QueueOrigin).
var originPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Item is one lesson or incident a collection round read from a Director copy.
type Item struct {
	Kind     store.QueueKind `json:"kind"`
	Machine  string          `json:"machine"`
	Director string          `json:"director"`
	Origin   string          `json:"origin"`
	ID       string          `json:"id"`
	Digest   string          `json:"digest"`
	// Path is the item's file in the private config, Title its lesson title or the first line of its incident's error.
	Path  string `json:"path"`
	Title string `json:"title"`
	// New is set when this round wrote the file; an item already committed with the same digest only has its mark
	// sent again. Marked is set once the copy recorded it collected.
	New     bool   `json:"new"`
	Marked  bool   `json:"marked"`
	Problem string `json:"problem,omitempty"`

	file []byte
}

// CopyCollection is one Director copy's part of a round.
type CopyCollection struct {
	Machine  string `json:"machine"`
	Director string `json:"director"`
	Items    []Item `json:"items"`
	// Truncated is set when a pending list was capped, so a later round collects the rest.
	Truncated bool `json:"truncated,omitempty"`
	// Error is why the copy could not be read or marked, as an unreachable machine; it is retried next round.
	Error string `json:"error,omitempty"`
}

// CollectResult is what one collection round did.
type CollectResult struct {
	// Commit is the private-config commit holding the items, pushed, and empty when the round committed nothing and
	// had nothing to mark.
	Commit string           `json:"commit,omitempty"`
	Copies []CopyCollection `json:"copies"`
	// Answers are the private-config files whose triage answer this round recorded.
	Answers []string `json:"answers,omitempty"`
}

// Items returns every item of the round that the private config holds, in copy order.
func (r *CollectResult) Items() []Item {
	var out []Item
	for _, c := range r.Copies {
		for _, it := range c.Items {
			if it.Problem == "" || it.Marked {
				out = append(out, it)
			}
		}
	}
	return out
}

// TriageAnswer is the Guildmaster's answer to an item's triage decision, which a round writes into the item's file.
type TriageAnswer struct {
	Kind store.QueueKind
	// Path is the item's file in the private config, and Decision the decision as "<repository>/<task>/<id>".
	Path, Decision string
	// State is what the answer makes the item: adopted or declined for a lesson, accepted, deferred or declined for an
	// incident; Outcome what follows from it, and Note the Guildmaster's words after the option.
	State, Outcome, Note string
}

// Collector collects the lessons and incidents of enrolled Director copies into the private config checkout
// (docs/steward.md#collection). It reaches the copies only with the fixed remote commands and scp, writes only
// lessons/ and incidents/ files and commits only them.
type Collector struct {
	Roster    *Roster
	Private   git.WorkTree
	Transport Copier
	Runner    Runner
	// Now stamps the collected files and commits.
	Now func() time.Time
}

// collectSignature is who the collection commits are by.
func (c *Collector) signature() git.Signature {
	return git.Signature{Name: "Guildhall Steward", Email: "steward@guildhall.invalid", When: c.Now().UTC()}
}

// pendingLessons and pendingIncidents are what `lesson list --json --pending` and `incident list --json --pending`
// print.
type pendingLessons struct {
	Origin    string         `json:"origin"`
	Lessons   []store.Lesson `json:"lessons"`
	Truncated bool           `json:"truncated"`
}

type pendingIncidents struct {
	Origin    string           `json:"origin"`
	Incidents []store.Incident `json:"incidents"`
	Truncated bool             `json:"truncated"`
}

// Collect runs one round on the Director copies of the named machines, every enrolled machine when none is named:
//
//  1. it reads each copy's pending lessons and incidents with the fixed read-only commands;
//  2. it takes .rollout.lock in the private config, and stops with ErrBusy, changing nothing, when it is held;
//  3. it writes each item not yet committed to lessons/<machine>/<origin>-<id>.md or
//     incidents/<machine>/<origin>-<id>.json and commits only those files, one commit per machine, and writes each
//     answer into its item's file in one more commit;
//  4. it pushes what the remote lacks, replaying onto it once if it moved ahead;
//  5. it copies each copy a receipt of its items and runs `director collected` there, which marks them.
//
// An item committed already with the same digest is not written again; only its mark is sent again. A copy that
// cannot be read or marked is reported in its CopyCollection and retried next round, and the others go on; a
// failed commit or push is the round's error, and nothing is marked.
func (c *Collector) Collect(ctx context.Context, machines []string, answers []TriageAnswer) (*CollectResult, error) {
	selected, err := c.machines(machines)
	if err != nil {
		return nil, err
	}
	res := &CollectResult{Copies: []CopyCollection{}}
	for _, m := range selected {
		for _, d := range m.Directors {
			res.Copies = append(res.Copies, c.read(ctx, m, d))
		}
	}

	lock := filepath.Join(c.Private.Dir, ".rollout.lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return res, ErrBusy
		}
		return res, fmt.Errorf("collection lock %s: %w", lock, err)
	}
	defer func() { _ = os.Remove(lock) }()
	if err := f.Close(); err != nil {
		return res, err
	}

	head, err := c.Private.Head(ctx)
	if err != nil {
		return res, err
	}
	start := head
	for _, m := range selected {
		files, lessons, incidents := map[string][]byte{}, 0, 0
		for i := range res.Copies {
			cc := &res.Copies[i]
			if cc.Machine != m.Label {
				continue
			}
			for j := range cc.Items {
				it := &cc.Items[j]
				if it.Problem != "" {
					continue
				}
				old, ok, err := c.Private.Show(ctx, head, it.Path)
				if err != nil {
					return res, err
				}
				if ok {
					if d := fileDigest(it.Kind, old); d != it.Digest {
						it.Problem = fmt.Sprintf("the private config already holds %s with digest %s, not %s", it.Path, d, it.Digest)
					}
					continue
				}
				it.New, files[it.Path] = true, it.file
				if it.Kind == store.QueueLesson {
					lessons++
				} else {
					incidents++
				}
			}
		}
		if len(files) == 0 {
			continue
		}
		if head, err = c.Private.CommitFiles(ctx, files, collectMessage(m.Label, lessons, incidents), c.signature()); err != nil {
			return res, fmt.Errorf("committing the items of %s: %w", m.Label, err)
		}
	}
	if err := c.recordAnswers(ctx, head, answers, res); err != nil {
		return res, err
	}
	pushed, err := c.Private.Publish(ctx, c.signature())
	if err != nil {
		return res, fmt.Errorf("pushing the private config: %w", err)
	}
	if pushed != start || len(res.Items()) > 0 {
		res.Commit = pushed
	}
	for i := range res.Copies {
		c.mark(ctx, selected, &res.Copies[i], pushed)
	}
	return res, nil
}

func collectMessage(machine string, lessons, incidents int) string {
	var parts []string
	if lessons > 0 {
		parts = append(parts, plural(lessons, "lesson"))
	}
	if incidents > 0 {
		parts = append(parts, plural(incidents, "incident"))
	}
	return fmt.Sprintf("Collect %s from %s", strings.Join(parts, " and "), machine)
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// machines returns the roster machines named, every machine with Directors when none is named.
func (c *Collector) machines(labels []string) ([]Machine, error) {
	if len(labels) == 0 {
		var out []Machine
		for _, m := range c.Roster.Machines {
			if len(m.Directors) > 0 {
				out = append(out, m)
			}
		}
		return out, nil
	}
	var out []Machine
	for i, label := range labels {
		if slices.Contains(labels[:i], label) {
			return nil, fmt.Errorf("machine %q is named twice", label)
		}
		m, ok := c.Roster.Machine(label)
		if !ok {
			return nil, fmt.Errorf("machine %q is not enrolled in the roster", label)
		}
		if len(m.Directors) == 0 {
			return nil, fmt.Errorf("machine %q has no Director copies", label)
		}
		out = append(out, m)
	}
	return out, nil
}

// read reads Director copy d's pending items on machine m and renders their files.
func (c *Collector) read(ctx context.Context, m Machine, d Director) CopyCollection {
	cc := CopyCollection{Machine: m.Label, Director: d.Name, Items: []Item{}}
	var lessons pendingLessons
	var incidents pendingIncidents
	for _, q := range []struct {
		argv []string
		into any
	}{
		{PendingLessonsCommand(m.Guildhall(), d.CopyDir), &lessons},
		{PendingIncidentsCommand(m.Guildhall(), d.CopyDir), &incidents},
	} {
		out, err := c.Runner.Run(ctx, m.SSHAlias, q.argv)
		if err == nil {
			err = json.Unmarshal(out, q.into)
		}
		if err != nil {
			cc.Error = fmt.Sprintf("reading %s: %v", strings.Join(q.argv[1:3], " "), err)
			return cc
		}
	}
	cc.Truncated = lessons.Truncated || incidents.Truncated
	now := c.Now().UTC()
	for _, l := range lessons.Lessons {
		it := Item{Kind: store.QueueLesson, Machine: m.Label, Director: d.Name, Origin: l.Origin, ID: l.ID, Digest: l.Digest, Title: l.Title}
		switch {
		case l.Origin != lessons.Origin || !originPattern.MatchString(l.Origin) || !workflow.ValidID(l.ID):
			it.Problem = fmt.Sprintf("lesson origin %q or id %q is not valid", l.Origin, l.ID)
		case string(digest.Of([]byte(l.Body))) != l.Digest:
			it.Problem = "the lesson's body does not have its digest " + l.Digest
		default:
			it.Path = fmt.Sprintf("lessons/%s/%s-%s.md", m.Label, l.Origin, l.ID)
			it.file = renderLesson(m.Label, d.Name, l, now, nil)
		}
		cc.Items = append(cc.Items, it)
	}
	for _, inc := range incidents.Incidents {
		r := inc.Report
		it := Item{Kind: store.QueueIncident, Machine: m.Label, Director: d.Name, Origin: inc.Origin, ID: r.ID, Digest: inc.Digest, Title: firstLine(r.Error, 120)}
		sum, err := json.Marshal(r)
		switch {
		case inc.Origin != incidents.Origin || !originPattern.MatchString(inc.Origin) || !workflow.ValidID(r.ID):
			it.Problem = fmt.Sprintf("incident origin %q or id %q is not valid", inc.Origin, r.ID)
		case err != nil || string(digest.Of(sum)) != inc.Digest:
			it.Problem = "the incident's report does not have its digest " + inc.Digest
		default:
			it.Path = fmt.Sprintf("incidents/%s/%s-%s.json", m.Label, inc.Origin, r.ID)
			if it.file, err = renderIncident(collectedIncident{Schema: CollectedIncidentSchema, Machine: m.Label, Director: d.Name,
				Origin: inc.Origin, ID: r.ID, Digest: inc.Digest, CollectedAt: now, Incident: inc, Triage: TriagePending}); err != nil {
				it.Problem = err.Error()
			}
		}
		cc.Items = append(cc.Items, it)
	}
	return cc
}

// mark copies cc's copy a receipt of its items held at commit and runs `director collected` there, setting each
// item's Marked or Problem from what it reports.
func (c *Collector) mark(ctx context.Context, machines []Machine, cc *CopyCollection, commit string) {
	if cc.Error != "" {
		return
	}
	receipt := CollectReceipt{Schema: CollectReceiptSchema, Items: []ReceiptItem{}}
	for _, it := range cc.Items {
		if it.Problem == "" {
			receipt.Items = append(receipt.Items, ReceiptItem{Kind: it.Kind, Origin: it.Origin, ID: it.ID, Digest: it.Digest, Commit: commit})
		}
	}
	if len(receipt.Items) == 0 {
		return
	}
	var m Machine
	var d Director
	for _, x := range machines {
		if x.Label == cc.Machine {
			m = x
			for _, y := range x.Directors {
				if y.Name == cc.Director {
					d = y
				}
			}
		}
	}
	tmp, err := os.MkdirTemp("", "guildhall-collect-")
	if err != nil {
		cc.Error = err.Error()
		return
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(tmp, "collect-receipt.json"), append(data, '\n'), 0o600)
	}
	if err == nil {
		err = c.Transport.Put(ctx, filepath.Join(tmp, "collect-receipt.json"), Remote{m.SSHAlias, d.CopyDir + "/" + CollectReceiptPath})
	}
	var out []byte
	if err == nil {
		out, err = c.Runner.Run(ctx, m.SSHAlias, CollectedCommand(m.Guildhall(), d.CopyDir))
	}
	var report CollectedReport
	if err == nil {
		if err = json.Unmarshal(out, &report); err == nil && report.Schema != CollectedReportSchema {
			err = fmt.Errorf("director collected printed schema %q, not %s", report.Schema, CollectedReportSchema)
		}
	}
	if err != nil {
		cc.Error = "marking collected: " + err.Error()
		return
	}
	for i := range cc.Items {
		it := &cc.Items[i]
		if it.Problem != "" {
			continue
		}
		it.Problem = "director collected did not report it"
		for _, r := range report.Items {
			if r.Kind != it.Kind || r.Origin != it.Origin || r.ID != it.ID {
				continue
			}
			switch r.Status {
			case MarkCollected, MarkAlready:
				it.Marked, it.Problem = true, ""
			default:
				it.Problem = fmt.Sprintf("%s: %s", r.Status, r.Error)
			}
		}
	}
}

// recordAnswers writes each answer into its item's file at head, unless the file already records an answer, and
// commits them in one commit.
func (c *Collector) recordAnswers(ctx context.Context, head string, answers []TriageAnswer, res *CollectResult) error {
	files := map[string][]byte{}
	for _, a := range answers {
		old, ok, err := c.Private.Show(ctx, head, a.Path)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		t := triage{State: a.State, Decision: a.Decision, Outcome: a.Outcome, Note: a.Note}
		var next []byte
		if a.Kind == store.QueueLesson {
			next, err = setLessonTriage(old, t)
		} else {
			next, err = setIncidentTriage(old, t)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", a.Path, err)
		}
		if next != nil {
			files[a.Path] = next
		}
	}
	if len(files) == 0 {
		return nil
	}
	if _, err := c.Private.CommitFiles(ctx, files, "Record "+plural(len(files), "triage answer"), c.signature()); err != nil {
		return fmt.Errorf("recording triage answers: %w", err)
	}
	res.Answers = slices.Sorted(func(yield func(string) bool) {
		for p := range files {
			if !yield(p) {
				return
			}
		}
	})
	return nil
}

// triage is a collected item's triage in its file.
type triage struct {
	State    string `json:"state"`
	Decision string `json:"decision,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Note     string `json:"note,omitempty"`
}

// collectedIncident is a collected incident's file. Its triage fields are flat, as a collected lesson's front matter
// is, so TriagePendingPatterns finds a pending one with one line.
type collectedIncident struct {
	Schema         string         `json:"schema"`
	Machine        string         `json:"machine"`
	Director       string         `json:"director"`
	Origin         string         `json:"origin"`
	ID             string         `json:"id"`
	Digest         string         `json:"digest"`
	CollectedAt    time.Time      `json:"collected_at"`
	Triage         string         `json:"triage"`
	TriageDecision string         `json:"triage_decision,omitempty"`
	TriageOutcome  string         `json:"triage_outcome,omitempty"`
	TriageNote     string         `json:"triage_note,omitempty"`
	Incident       store.Incident `json:"incident"`
}

// TriagePendingPatterns are the extended regular expressions that match the line of a collected lesson or incident
// file whose triage is still pending, for git grep.
var TriagePendingPatterns = []string{`^triage: "pending"$`, `^  "triage": "pending",$`}

// Collected is what a collected lesson or incident file says about its item.
type Collected struct {
	Kind     store.QueueKind
	Machine  string
	Director string
	Origin   string
	ID       string
	Digest   string
	// Title is a lesson's title or the first line of an incident's error.
	Title string
	// Triage is the item's triage state, TriagePending until its answer is recorded.
	Triage string
}

// ParseCollected reads the collected lesson or incident file at path, a lessons/ or incidents/ file of the private
// config, from data.
func ParseCollected(path string, data []byte) (Collected, error) {
	switch {
	case strings.HasPrefix(path, "lessons/") && strings.HasSuffix(path, ".md"):
		front, _, err := lessonFront(data)
		if err != nil {
			return Collected{}, err
		}
		c := Collected{Kind: store.QueueLesson}
		for _, kv := range front {
			switch kv[0] {
			case "machine":
				c.Machine = kv[1]
			case "director":
				c.Director = kv[1]
			case "origin":
				c.Origin = kv[1]
			case "id":
				c.ID = kv[1]
			case "digest":
				c.Digest = kv[1]
			case "title":
				c.Title = kv[1]
			case "triage":
				c.Triage = kv[1]
			}
		}
		return c, nil
	case strings.HasPrefix(path, "incidents/") && strings.HasSuffix(path, ".json"):
		var ci collectedIncident
		if err := json.Unmarshal(data, &ci); err != nil {
			return Collected{}, fmt.Errorf("not a collected incident: %w", err)
		}
		return Collected{Kind: store.QueueIncident, Machine: ci.Machine, Director: ci.Director, Origin: ci.Origin, ID: ci.ID,
			Digest: ci.Digest, Title: firstLine(ci.Incident.Report.Error, 120), Triage: ci.Triage}, nil
	}
	return Collected{}, fmt.Errorf("%s is not a collected lesson or incident", path)
}

func renderIncident(ci collectedIncident) ([]byte, error) {
	data, err := json.MarshalIndent(ci, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// renderLesson is a collected lesson's file: a front matter of JSON-valued lines, then the lesson's body as filed.
func renderLesson(machine, director string, l store.Lesson, at time.Time, t *triage) []byte {
	if t == nil {
		t = &triage{State: TriagePending}
	}
	var b bytes.Buffer
	b.WriteString(collectedLessonFrontLine + "\n")
	for _, kv := range [][2]string{
		{"schema", CollectedLessonSchema}, {"machine", machine}, {"director", director}, {"origin", l.Origin}, {"id", l.ID},
		{"digest", l.Digest}, {"title", l.Title}, {"filed_at", l.Time.UTC().Format(time.RFC3339)}, {"collected_at", at.Format(time.RFC3339)},
	} {
		writeFront(&b, kv[0], kv[1])
	}
	writeTriage(&b, *t)
	b.WriteString(collectedLessonFrontLine + "\n")
	b.WriteString(l.Body)
	return b.Bytes()
}

func writeFront(b *bytes.Buffer, key, value string) {
	v, _ := json.Marshal(value)
	fmt.Fprintf(b, "%s: %s\n", key, v)
}

func writeTriage(b *bytes.Buffer, t triage) {
	writeFront(b, "triage", t.State)
	if t.Decision != "" {
		writeFront(b, "triage_decision", t.Decision)
	}
	if t.Outcome != "" {
		writeFront(b, "triage_outcome", t.Outcome)
	}
	if t.Note != "" {
		writeFront(b, "triage_note", t.Note)
	}
}

// lessonFront splits a collected lesson file into its front matter values, in order, and its body.
func lessonFront(data []byte) ([][2]string, []byte, error) {
	rest, ok := bytes.CutPrefix(data, []byte(collectedLessonFrontLine+"\n"))
	if !ok {
		return nil, nil, errors.New("not a collected lesson: no front matter")
	}
	var front [][2]string
	for {
		line, after, found := bytes.Cut(rest, []byte("\n"))
		if !found {
			return nil, nil, errors.New("not a collected lesson: the front matter does not end")
		}
		rest = after
		if string(line) == collectedLessonFrontLine {
			return front, rest, nil
		}
		key, raw, ok := strings.Cut(string(line), ": ")
		var v string
		if !ok || json.Unmarshal([]byte(raw), &v) != nil {
			return nil, nil, fmt.Errorf("not a collected lesson: front matter line %q", line)
		}
		front = append(front, [2]string{key, v})
	}
}

// fileDigest is the item digest a collected file records, "" when it records none.
func fileDigest(kind store.QueueKind, data []byte) string {
	if kind == store.QueueLesson {
		front, _, err := lessonFront(data)
		if err != nil {
			return ""
		}
		for _, kv := range front {
			if kv[0] == "digest" {
				return kv[1]
			}
		}
		return ""
	}
	var ci collectedIncident
	if json.Unmarshal(data, &ci) != nil {
		return ""
	}
	return ci.Digest
}

// setLessonTriage is the collected lesson file data with triage t, or nil when it already records an answer.
func setLessonTriage(data []byte, t triage) ([]byte, error) {
	front, body, err := lessonFront(data)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(collectedLessonFrontLine + "\n")
	for _, kv := range front {
		if kv[0] == "triage" && kv[1] != TriagePending {
			return nil, nil
		}
		if strings.HasPrefix(kv[0], "triage") {
			continue
		}
		writeFront(&b, kv[0], kv[1])
	}
	writeTriage(&b, t)
	b.WriteString(collectedLessonFrontLine + "\n")
	b.Write(body)
	return b.Bytes(), nil
}

// setIncidentTriage is the collected incident file data with triage t, or nil when it already records an answer.
func setIncidentTriage(data []byte, t triage) ([]byte, error) {
	var ci collectedIncident
	if err := json.Unmarshal(data, &ci); err != nil {
		return nil, fmt.Errorf("not a collected incident: %w", err)
	}
	if ci.Triage != TriagePending {
		return nil, nil
	}
	ci.Triage, ci.TriageDecision, ci.TriageOutcome, ci.TriageNote = t.State, t.Decision, t.Outcome, t.Note
	return renderIncident(ci)
}

func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}
