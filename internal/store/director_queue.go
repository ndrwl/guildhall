package store

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/digest"
)

// Nested buckets of the director bucket holding a copy's local queue of lessons and maintenance incidents
// (docs/store.md#lessons-and-incidents): the copy's origin, its lessons and incidents keyed by id, and the later
// observations of incidents keyed by incident id and observation sequence. Each is created by its first record, so an
// existing schema 1 file accepts them unchanged.
var (
	bucketQueueOrigin          = []byte("queue-origin")
	bucketLessons              = []byte("lessons")
	bucketIncidents            = []byte("incidents")
	bucketIncidentObservations = []byte("incident-observations")
	keyQueueOrigin             = []byte("origin")
)

// Operations recorded on lesson and incident receipts.
const (
	opProposeLesson    = "propose-lesson"
	opResolveLesson    = "resolve-lesson"
	opFileIncident     = "file-incident"
	opObserveIncident  = "observe-incident"
	opResolveIncident  = "resolve-incident"
	opCollectQueueItem = "collect-queue-item"
)

// MaxQueueItemBytes bounds one lesson body, one incident report and one incident observation, as their JSON is stored
// inline (64 KiB); a larger one is refused with its size.
const MaxQueueItemBytes = 64 << 10

// MaxIncidentBytes bounds one incident as stored: its record and every observation record linked to it, together
// (512 KiB). An observation that would take an incident past it is refused, so an incident listed with all its
// observations always fits well inside a pending list's 1 MiB cap and can never hold that list back.
const MaxIncidentBytes = 512 << 10

// MaxQueueIDBytes bounds a lesson or incident id.
const MaxQueueIDBytes = 64

// IncidentNotApplicable is the value of an incident's repository, task, step or attempt for a failure before or
// outside any task.
const IncidentNotApplicable = "n/a"

// IncidentSchema and IncidentObservationSchema are the schema prefixes of the strict-JSON incident report and
// incident observation `guildhall incident file` and `incident observe` read; version 1 is the only one.
const (
	IncidentSchema            = "guildhall.incident/"
	IncidentObservationSchema = "guildhall.incident-observation/"
	IncidentSchemaVersion     = 1
)

// LessonState is where a lesson is in its life (docs/director-modes.md#lessons).
type LessonState string

const (
	// LessonProposed is a lesson filed and not yet collected or resolved.
	LessonProposed LessonState = "proposed"
	// LessonCollected is a lesson a Steward collected: from then on its queue owns it.
	LessonCollected LessonState = "collected"
	// LessonAdopted and LessonDeclined are the Guildmaster's local decision on a standalone lesson.
	LessonAdopted  LessonState = "adopted"
	LessonDeclined LessonState = "declined"
)

// IncidentState is where an incident is in its life (docs/messaging.md#maintenance-tickets-to-the-steward).
type IncidentState string

const (
	// IncidentOpen is an incident filed and not yet collected or resolved.
	IncidentOpen IncidentState = "open"
	// IncidentCollected is an incident a Steward collected: from then on its queue owns it.
	IncidentCollected IncidentState = "collected"
	// IncidentAccepted, IncidentDeferred and IncidentDeclined are the Guildmaster's local decision on a standalone
	// incident; a deferred one may still be accepted or declined.
	IncidentAccepted IncidentState = "accepted"
	IncidentDeferred IncidentState = "deferred"
	IncidentDeclined IncidentState = "declined"
)

// Collection is the receipt a Steward's collection gave an item: the private-repository commit that holds it and when
// the copy recorded it.
type Collection struct {
	Commit  string    `json:"commit"`
	Receipt uint64    `json:"receipt"`
	Time    time.Time `json:"time"`
}

// Resolution is the Guildmaster's local decision on a lesson or incident: when it was recorded and its receipt.
type Resolution struct {
	Receipt uint64    `json:"receipt"`
	Time    time.Time `json:"time"`
}

// Lesson is one proposed change to shared practice: a Markdown body with a "# " title and "## Evidence" and
// "## Limits" sections, stored inline.
type Lesson struct {
	ID string `json:"id"`
	// Origin is the copy's queue origin, set by the store.
	Origin string `json:"origin"`
	// Title is the body's "# " title, set by the store.
	Title string `json:"title"`
	Body  string `json:"body"`
	// Digest is the body's sha256, set by the store.
	Digest string      `json:"digest"`
	State  LessonState `json:"state"`
	// Receipt and Time are the filing's receipt sequence and time; Resolved and Collected are set once the lesson
	// leaves proposed.
	Receipt   uint64      `json:"receipt"`
	Time      time.Time   `json:"time"`
	Resolved  *Resolution `json:"resolved,omitempty"`
	Collected *Collection `json:"collected,omitempty"`
}

// Evidence is one evidence reference: what it is, such as a path or a URL, and the sha256 of its content. Large
// evidence stays where it is; only the reference is filed.
type Evidence struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

// IncidentReport is what a Director files about an engine dead end, an unexpected state or a workaround
// (docs/messaging.md#maintenance-tickets-to-the-steward): the strict-JSON document `guildhall incident file` reads.
type IncidentReport struct {
	Schema string `json:"schema"`
	ID     string `json:"id"`
	// Repository, Task, Step and Attempt name where it happened, each IncidentNotApplicable for a failure outside a
	// task.
	Repository string `json:"repository"`
	Task       string `json:"task"`
	Step       string `json:"step"`
	Attempt    string `json:"attempt"`
	// Release and Workflow are the Guildhall release and workflow revision in use.
	Release  string `json:"release"`
	Workflow string `json:"workflow"`
	// ObservedState is what was found, at ObservedAt, and Error the exact error.
	ObservedState string     `json:"observed_state"`
	ObservedAt    time.Time  `json:"observed_at"`
	Error         string     `json:"error"`
	Evidence      []Evidence `json:"evidence"`
	// Tried is what the Director tried, Done what it actually did, Workaround the workaround, if any, and Risk the
	// risk that remains.
	Tried      string `json:"tried"`
	Done       string `json:"done"`
	Workaround string `json:"workaround,omitempty"`
	Risk       string `json:"risk"`
}

// Incident is one filed maintenance incident: the report as filed, never changed, and its state.
type Incident struct {
	Report IncidentReport `json:"report"`
	// Origin is the copy's queue origin and Digest the sha256 of the report's JSON; both are set by the store.
	Origin string        `json:"origin"`
	Digest string        `json:"digest"`
	State  IncidentState `json:"state"`
	// Receipt and Time are the filing's receipt sequence and time.
	Receipt   uint64      `json:"receipt"`
	Time      time.Time   `json:"time"`
	Resolved  *Resolution `json:"resolved,omitempty"`
	Collected *Collection `json:"collected,omitempty"`
	// Observations are the later observations linked to the incident, oldest first; they are read with it and stored
	// apart.
	Observations []IncidentObservation `json:"observations,omitempty"`
}

// ObservationReport is later evidence about a filed incident: the strict-JSON document `guildhall incident observe`
// reads.
type ObservationReport struct {
	Schema     string     `json:"schema"`
	ObservedAt time.Time  `json:"observed_at"`
	Note       string     `json:"note"`
	Evidence   []Evidence `json:"evidence"`
}

// IncidentObservation is one observation linked to an incident, never changed.
type IncidentObservation struct {
	Incident string `json:"incident"`
	// Sequence numbers the incident's observations from 1; Origin, Digest, Receipt and Time are set by the store.
	Sequence uint64            `json:"sequence"`
	Report   ObservationReport `json:"report"`
	Origin   string            `json:"origin"`
	Digest   string            `json:"digest"`
	Receipt  uint64            `json:"receipt"`
	Time     time.Time         `json:"time"`
}

var (
	lessonTitleLine = regexp.MustCompile(`^# (\S.*)$`)
	sha256Hex       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// LessonTitle is the "# " title of lesson body, its first line that is not blank, after checking that the body has
// "## Evidence" and "## Limits" sections, outside fenced code blocks; a body without them is ErrInvalid.
func LessonTitle(body string) (string, error) {
	var title string
	evidence, limits := false, false
	fence := ""
	for line := range strings.Lines(body) {
		line = strings.TrimRight(line, " \t\r\n")
		if title != "" {
			if marker := fenceMarker(line); fence == "" && marker != "" {
				fence = marker
				continue
			} else if fence != "" {
				if strings.HasPrefix(strings.TrimLeft(line, " "), fence) && strings.Trim(strings.TrimLeft(line, " "), fence[:1]) == "" {
					fence = ""
				}
				continue
			}
		}
		switch {
		case title == "" && line == "":
		case title == "":
			m := lessonTitleLine.FindStringSubmatch(line)
			if m == nil {
				return "", invalid("a lesson starts with a \"# \" title line, not %q", firstBytes(line, 80))
			}
			title = strings.TrimSpace(m[1])
		case line == "## Evidence":
			evidence = true
		case line == "## Limits":
			limits = true
		}
	}
	switch {
	case title == "":
		return "", invalid("a lesson starts with a \"# \" title line")
	case len(title) > 200:
		return "", invalid("a lesson title is at most 200 bytes, not %d", len(title))
	case !evidence || !limits:
		return "", invalid("a lesson needs a \"## Evidence\" section and a \"## Limits\" section")
	}
	return title, nil
}

// fenceMarker is the run of three or more backticks or tildes that opens a fenced code block on line, indented at
// most three spaces, or "" when line opens none.
func fenceMarker(line string) string {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 || t == "" || (t[0] != '`' && t[0] != '~') {
		return ""
	}
	n := len(t) - len(strings.TrimLeft(t, t[:1]))
	if n < 3 {
		return ""
	}
	return t[:n]
}

// DefaultLessonID is the id a lesson filed without one gets: "l-" and the first 12 hex digits of its body's sha256.
func DefaultLessonID(body string) string { return "l-" + digest.Of([]byte(body)).Hex()[:12] }

func firstBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// validateQueueID checks a lesson or incident id: a lowercase id of at most MaxQueueIDBytes.
func validateQueueID(what, id string) error {
	if len(id) > MaxQueueIDBytes {
		return invalid("%s id %q is longer than %d bytes", what, firstBytes(id, MaxQueueIDBytes), MaxQueueIDBytes)
	}
	if err := validateIDs(id); err != nil {
		return invalid("%s id %q must be lowercase letters, digits and hyphens, starting with a letter", what, id)
	}
	return nil
}

// validateLine checks a one-line field: not empty, at most limit bytes of UTF-8 and no line break.
func validateLine(name, v string, limit int) error {
	if err := validateText(name, v, limit); err != nil {
		return err
	}
	if strings.ContainsAny(v, "\r\n") {
		return invalid("%s must be one line", name)
	}
	return nil
}

func validateEvidence(evidence []Evidence) error {
	for i, e := range evidence {
		if err := validateLine(fmt.Sprintf("evidence %d ref", i+1), e.Ref, 1024); err != nil {
			return err
		}
		d, err := digest.Parse(e.Digest)
		if err != nil || !sha256Hex.MatchString(d.Hex()) {
			return invalid("evidence %d digest %q must be sha256:<64 lowercase hex>", i+1, e.Digest)
		}
	}
	return nil
}

// ValidateIncidentReport checks r as `incident file` and the store do: its schema, an id, the four references, each
// IncidentNotApplicable when it does not apply, the release and workflow revisions, the observed state and time, the
// exact error, the evidence references with digests, what was tried and done and the remaining risk, all within
// MaxQueueItemBytes as JSON.
func ValidateIncidentReport(r IncidentReport) error {
	if r.Schema != fmt.Sprintf("%s%d", IncidentSchema, IncidentSchemaVersion) {
		return invalid("incident schema %q must be %s%d", r.Schema, IncidentSchema, IncidentSchemaVersion)
	}
	if err := validateQueueID("incident", r.ID); err != nil {
		return err
	}
	for _, f := range []struct{ name, v string }{
		{"repository", r.Repository}, {"task", r.Task}, {"step", r.Step}, {"attempt", r.Attempt},
		{"release", r.Release}, {"workflow", r.Workflow},
	} {
		if err := validateLine("incident "+f.name, f.v, 256); err != nil {
			return err
		}
	}
	if r.ObservedAt.IsZero() {
		return invalid("incident observed_at must be the time the state was observed")
	}
	for _, f := range []struct{ name, v string }{
		{"observed_state", r.ObservedState}, {"error", r.Error}, {"tried", r.Tried}, {"done", r.Done}, {"risk", r.Risk},
	} {
		if err := validateText("incident "+f.name, f.v, MaxQueueItemBytes); err != nil {
			return err
		}
	}
	if r.Workaround != "" {
		if err := validateText("incident workaround", r.Workaround, MaxQueueItemBytes); err != nil {
			return err
		}
	}
	if err := validateEvidence(r.Evidence); err != nil {
		return err
	}
	if b, err := json.Marshal(r); err != nil {
		return err
	} else if len(b) > MaxQueueItemBytes {
		return invalid("incident is %d bytes; the limit is %d", len(b), MaxQueueItemBytes)
	}
	return nil
}

// ValidateObservationReport checks o as `incident observe` and the store do: its schema, the time, a note and the
// evidence references, all within MaxQueueItemBytes as JSON.
func ValidateObservationReport(o ObservationReport) error {
	if o.Schema != fmt.Sprintf("%s%d", IncidentObservationSchema, IncidentSchemaVersion) {
		return invalid("observation schema %q must be %s%d", o.Schema, IncidentObservationSchema, IncidentSchemaVersion)
	}
	if o.ObservedAt.IsZero() {
		return invalid("observation observed_at must be the time it was observed")
	}
	if err := validateText("observation note", o.Note, MaxQueueItemBytes); err != nil {
		return err
	}
	if err := validateEvidence(o.Evidence); err != nil {
		return err
	}
	if b, err := json.Marshal(o); err != nil {
		return err
	} else if len(b) > MaxQueueItemBytes {
		return invalid("observation is %d bytes; the limit is %d", len(b), MaxQueueItemBytes)
	}
	return nil
}

// jsonDigest is the sha256 of v's JSON.
func jsonDigest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(digest.Of(b)), nil
}

// ensureOrigin is the copy's queue origin in tx, created on first use as 16 random hex digits and never changed.
func ensureOrigin(tx *bolt.Tx) (string, error) {
	b, err := createDirectorBucket(tx, bucketQueueOrigin)
	if err != nil {
		return "", err
	}
	if v := b.Get(keyQueueOrigin); v != nil {
		return string(v), nil
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	origin := hex.EncodeToString(raw[:])
	return origin, b.Put(keyQueueOrigin, []byte(origin))
}

// QueueOrigin is the copy's queue origin, the random id its first lesson or incident wrote, or ErrNotFound before one.
func (s *Store) QueueOrigin() (string, error) {
	var origin string
	err := s.view(func(tx *bolt.Tx) error {
		b := directorBucket(tx, bucketQueueOrigin)
		if b == nil || b.Get(keyQueueOrigin) == nil {
			return ErrNotFound
		}
		origin = string(b.Get(keyQueueOrigin))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("store: queue origin: %w", err)
	}
	return origin, nil
}

// lessonRequest is what a lesson filing's receipt digests: the id and the body, so filing the same again replays and
// another body under the id conflicts.
type lessonRequest struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// ProposeLesson files l, its ID and Body, as a proposed lesson under the mutation id "lesson:<id>", setting its
// origin, title, digest, state, receipt and time: filing the same id and body again returns the lesson and its
// receipt, and another body under the id is ErrMutationConflict. An invalid id, a body over MaxQueueItemBytes, one
// without its title and sections, and any store-set field are ErrInvalid.
func (s *Store) ProposeLesson(l Lesson) (Lesson, Receipt, error) {
	if l.Origin != "" || l.Title != "" || l.Digest != "" || l.State != "" || l.Receipt != 0 || !l.Time.IsZero() || l.Resolved != nil || l.Collected != nil {
		return Lesson{}, Receipt{}, invalid("a lesson's origin, title, digest, state, receipt and time are set by the store")
	}
	if err := validateQueueID("lesson", l.ID); err != nil {
		return Lesson{}, Receipt{}, err
	}
	if len(l.Body) > MaxQueueItemBytes {
		return Lesson{}, Receipt{}, invalid("lesson is %d bytes; the limit is %d", len(l.Body), MaxQueueItemBytes)
	}
	if err := validateText("lesson", l.Body, MaxQueueItemBytes); err != nil {
		return Lesson{}, Receipt{}, err
	}
	title, err := LessonTitle(l.Body)
	if err != nil {
		return Lesson{}, Receipt{}, err
	}
	r, err := s.mutate("lesson:"+l.ID, opProposeLesson, lessonRequest{l.ID, l.Body}, func(tx *bolt.Tx, r *Receipt) error {
		b, err := createDirectorBucket(tx, bucketLessons)
		if err != nil {
			return err
		}
		if b.Get([]byte(l.ID)) != nil {
			return fmt.Errorf("%w: lesson %s is already filed", ErrMutationConflict, l.ID)
		}
		if l.Origin, err = ensureOrigin(tx); err != nil {
			return err
		}
		r.Record = "director/lesson/" + l.ID
		l.Title, l.Digest, l.State = title, string(digest.Of([]byte(l.Body))), LessonProposed
		l.Receipt, l.Time = r.Sequence, s.now().UTC()
		return putJSON(b, []byte(l.ID), l)
	})
	if err != nil {
		return Lesson{}, Receipt{}, err
	}
	got, err := s.Lesson(l.ID)
	return got, r, err
}

// Lesson returns the lesson with id, ErrNotFound for none.
func (s *Store) Lesson(id string) (Lesson, error) {
	var l Lesson
	if err := s.view(func(tx *bolt.Tx) error { return getJSON(directorBucket(tx, bucketLessons), []byte(id), &l) }); err != nil {
		return Lesson{}, fmt.Errorf("store: lesson %q: %w", id, err)
	}
	return l, nil
}

// Lessons returns every lesson, in the order they were filed.
func (s *Store) Lessons() ([]Lesson, error) {
	out := []Lesson{}
	err := s.view(func(tx *bolt.Tx) error {
		return forEachJSON(directorBucket(tx, bucketLessons), func() *Lesson { return new(Lesson) }, func(l *Lesson) { out = append(out, *l) })
	})
	if err != nil {
		return nil, fmt.Errorf("store: lessons: %w", err)
	}
	slices.SortFunc(out, func(a, b Lesson) int { return cmp.Compare(a.Receipt, b.Receipt) })
	return out, nil
}

// resolveRequest is what a resolution's receipt digests.
type resolveRequest struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// ResolveLesson records the Guildmaster's local decision on the proposed lesson id, adopted or declined, under the
// mutation id "lesson-resolve:<id>": resolving it the same way again returns the lesson and its receipt. A collected
// lesson, owned by the Steward's queue from then on, one already resolved the other way and any other state are
// ErrInvalid; no such lesson is ErrNotFound.
func (s *Store) ResolveLesson(id string, state LessonState) (Lesson, Receipt, error) {
	if state != LessonAdopted && state != LessonDeclined {
		return Lesson{}, Receipt{}, invalid("a lesson is resolved %s or %s, not %q", LessonAdopted, LessonDeclined, state)
	}
	cur, err := s.Lesson(id)
	if err != nil {
		return Lesson{}, Receipt{}, err
	}
	mid := "lesson-resolve:" + id
	switch cur.State {
	case state:
		r, err := s.Receipt(mid)
		return cur, r, err
	case LessonProposed:
	case LessonCollected:
		return Lesson{}, Receipt{}, invalid("lesson %s is collected, so the Steward's queue owns it", id)
	default:
		return Lesson{}, Receipt{}, invalid("lesson %s is already %s", id, cur.State)
	}
	r, err := s.mutate(mid, opResolveLesson, resolveRequest{id, string(state)}, func(tx *bolt.Tx, r *Receipt) error {
		b := directorBucket(tx, bucketLessons)
		var l Lesson
		if err := getJSON(b, []byte(id), &l); err != nil {
			return err
		}
		if l.State != LessonProposed {
			return fmt.Errorf("%w: lesson %s became %s meanwhile", ErrMutationConflict, id, l.State)
		}
		r.Record = "director/lesson/" + id
		l.State, l.Resolved = state, &Resolution{Receipt: r.Sequence, Time: s.now().UTC()}
		return putJSON(b, []byte(id), l)
	})
	if err != nil {
		return Lesson{}, Receipt{}, err
	}
	got, err := s.Lesson(id)
	return got, r, err
}

// FileIncident files report as an open incident under the mutation id "incident:<id>", setting its origin, digest,
// state, receipt and time: filing the same report again returns the incident and its receipt, and another report
// under the id is ErrMutationConflict. An invalid report (ValidateIncidentReport) is ErrInvalid.
func (s *Store) FileIncident(report IncidentReport) (Incident, Receipt, error) {
	if err := ValidateIncidentReport(report); err != nil {
		return Incident{}, Receipt{}, err
	}
	sum, err := jsonDigest(report)
	if err != nil {
		return Incident{}, Receipt{}, err
	}
	r, err := s.mutate("incident:"+report.ID, opFileIncident, report, func(tx *bolt.Tx, r *Receipt) error {
		b, err := createDirectorBucket(tx, bucketIncidents)
		if err != nil {
			return err
		}
		if b.Get([]byte(report.ID)) != nil {
			return fmt.Errorf("%w: incident %s is already filed", ErrMutationConflict, report.ID)
		}
		origin, err := ensureOrigin(tx)
		if err != nil {
			return err
		}
		r.Record = "director/incident/" + report.ID
		return putJSON(b, []byte(report.ID), Incident{Report: report, Origin: origin, Digest: sum, State: IncidentOpen, Receipt: r.Sequence, Time: s.now().UTC()})
	})
	if err != nil {
		return Incident{}, Receipt{}, err
	}
	got, err := s.Incident(report.ID)
	return got, r, err
}

// observeRequest is what an observation's receipt digests.
type observeRequest struct {
	Incident string            `json:"incident"`
	Report   ObservationReport `json:"report"`
}

// ObserveIncident links observation o to the filed incident id as its next observation, under the mutation id
// "incident-observe:<id>:<first 16 hex digits of o's sha256>", so the same observation again returns the one recorded
// and its receipt. It never changes the incident's report, and it is recorded whatever the incident's state. An
// invalid observation, or one that would take the incident and its observations past MaxIncidentBytes, is ErrInvalid,
// and no such incident is ErrNotFound.
func (s *Store) ObserveIncident(id string, o ObservationReport) (IncidentObservation, Receipt, error) {
	if err := ValidateObservationReport(o); err != nil {
		return IncidentObservation{}, Receipt{}, err
	}
	if _, err := s.Incident(id); err != nil {
		return IncidentObservation{}, Receipt{}, err
	}
	sum, err := jsonDigest(o)
	if err != nil {
		return IncidentObservation{}, Receipt{}, err
	}
	var key []byte
	r, err := s.mutate("incident-observe:"+id+":"+digest.Digest(sum).Hex()[:16], opObserveIncident, observeRequest{id, o}, func(tx *bolt.Tx, r *Receipt) error {
		if directorBucket(tx, bucketIncidents).Get([]byte(id)) == nil {
			return ErrNotFound
		}
		b, err := createDirectorBucket(tx, bucketIncidentObservations)
		if err != nil {
			return err
		}
		var seq uint64 = 1
		size := len(directorBucket(tx, bucketIncidents).Get([]byte(id)))
		prefix := []byte(id + "/")
		c := b.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			seq++
			size += len(v)
		}
		origin, err := ensureOrigin(tx)
		if err != nil {
			return err
		}
		key = observationKey(id, seq)
		r.Record = fmt.Sprintf("director/incident/%s/observation/%d", id, seq)
		rec, err := json.Marshal(IncidentObservation{Incident: id, Sequence: seq, Report: o, Origin: origin, Digest: sum, Receipt: r.Sequence, Time: s.now().UTC()})
		if err != nil {
			return err
		}
		if size+len(rec) > MaxIncidentBytes {
			return invalid("incident %s with its observations would be %d bytes; the limit is %d, so file a new incident that names it", id, size+len(rec), MaxIncidentBytes)
		}
		return b.Put(key, rec)
	})
	if err != nil {
		return IncidentObservation{}, Receipt{}, err
	}
	inc, err := s.Incident(id)
	if err != nil {
		return IncidentObservation{}, Receipt{}, err
	}
	for _, ob := range inc.Observations {
		if ob.Receipt == r.Sequence {
			return ob, r, nil
		}
	}
	return IncidentObservation{}, Receipt{}, fmt.Errorf("store: observation of receipt %d of incident %s: %w", r.Sequence, id, ErrNotFound)
}

func observationKey(id string, seq uint64) []byte { return fmt.Appendf(nil, "%s/%010d", id, seq) }

// Incident returns the incident with id and its observations, ErrNotFound for none.
func (s *Store) Incident(id string) (Incident, error) {
	var inc Incident
	err := s.view(func(tx *bolt.Tx) error {
		if err := getJSON(directorBucket(tx, bucketIncidents), []byte(id), &inc); err != nil {
			return err
		}
		obs, err := observations(tx)
		inc.Observations = obs[id]
		return err
	})
	if err != nil {
		return Incident{}, fmt.Errorf("store: incident %q: %w", id, err)
	}
	return inc, nil
}

// Incidents returns every incident with its observations, in the order they were filed.
func (s *Store) Incidents() ([]Incident, error) {
	out := []Incident{}
	err := s.view(func(tx *bolt.Tx) error {
		obs, err := observations(tx)
		if err != nil {
			return err
		}
		return forEachJSON(directorBucket(tx, bucketIncidents), func() *Incident { return new(Incident) }, func(inc *Incident) {
			inc.Observations = obs[inc.Report.ID]
			out = append(out, *inc)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: incidents: %w", err)
	}
	slices.SortFunc(out, func(a, b Incident) int { return cmp.Compare(a.Receipt, b.Receipt) })
	return out, nil
}

// observations reads every incident observation in tx, by incident id, oldest first.
func observations(tx *bolt.Tx) (map[string][]IncidentObservation, error) {
	out := map[string][]IncidentObservation{}
	err := forEachJSON(directorBucket(tx, bucketIncidentObservations), func() *IncidentObservation { return new(IncidentObservation) },
		func(o *IncidentObservation) { out[o.Incident] = append(out[o.Incident], *o) })
	return out, err
}

// incidentMoves is each incident state the Guildmaster may resolve an incident from, and the states it may move to.
var incidentMoves = map[IncidentState][]IncidentState{
	IncidentOpen:     {IncidentAccepted, IncidentDeferred, IncidentDeclined},
	IncidentDeferred: {IncidentAccepted, IncidentDeclined},
}

// ResolveIncident records the Guildmaster's local decision on incident id, accepted, deferred or declined, under the
// mutation id "incident-resolve:<id>:<state>": an open incident takes any of them, and a deferred one accepted or
// declined; resolving it to the state it is in again returns it and that resolution's receipt. A collected incident,
// owned by the Steward's queue from then on, and any other move are ErrInvalid; no such incident is ErrNotFound.
// Resolving launches nothing and changes no policy.
func (s *Store) ResolveIncident(id string, state IncidentState) (Incident, Receipt, error) {
	if state != IncidentAccepted && state != IncidentDeferred && state != IncidentDeclined {
		return Incident{}, Receipt{}, invalid("an incident is resolved %s, %s or %s, not %q", IncidentAccepted, IncidentDeferred, IncidentDeclined, state)
	}
	cur, err := s.Incident(id)
	if err != nil {
		return Incident{}, Receipt{}, err
	}
	mid := "incident-resolve:" + id + ":" + string(state)
	switch {
	case cur.State == state:
		r, err := s.Receipt(mid)
		return cur, r, err
	case cur.State == IncidentCollected:
		return Incident{}, Receipt{}, invalid("incident %s is collected, so the Steward's queue owns it", id)
	case !slices.Contains(incidentMoves[cur.State], state):
		return Incident{}, Receipt{}, invalid("incident %s is already %s", id, cur.State)
	}
	r, err := s.mutate(mid, opResolveIncident, resolveRequest{id, string(state)}, func(tx *bolt.Tx, r *Receipt) error {
		b := directorBucket(tx, bucketIncidents)
		var inc Incident
		if err := getJSON(b, []byte(id), &inc); err != nil {
			return err
		}
		if inc.State != cur.State {
			return fmt.Errorf("%w: incident %s became %s meanwhile", ErrMutationConflict, id, inc.State)
		}
		r.Record = "director/incident/" + id
		inc.State, inc.Resolved = state, &Resolution{Receipt: r.Sequence, Time: s.now().UTC()}
		return putJSON(b, []byte(id), inc)
	})
	if err != nil {
		return Incident{}, Receipt{}, err
	}
	got, err := s.Incident(id)
	return got, r, err
}

// QueueKind names the queue a collected item is in.
type QueueKind string

const (
	QueueLesson   QueueKind = "lesson"
	QueueIncident QueueKind = "incident"
)

// collectRequest is what a collection mark's receipt digests.
type collectRequest struct {
	Kind   QueueKind `json:"kind"`
	ID     string    `json:"id"`
	Digest string    `json:"digest"`
	Commit string    `json:"commit"`
}

var commitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ErrDigestMismatch reports a collection receipt whose digest is not the item's: the item stays pending.
var ErrDigestMismatch = errors.New("the collected digest is not the item's")

// MarkCollected records that a Steward collected the lesson or incident id, whose content has digest d, into the
// private-repository commit commit, under the mutation id "collected:<kind>:<id>:<first 16 hex digits of d>": its state
// becomes collected, keeping any local resolution, and marking it again with the same commit returns its receipt. A
// digest that is not the item's is ErrDigestMismatch and leaves the item pending; an item already collected, a bad
// commit and a bad kind are ErrInvalid; no such item is ErrNotFound. `director collected` will call it (d124 slice 2).
func (s *Store) MarkCollected(kind QueueKind, id, d, commit string) (Receipt, error) {
	if !commitPattern.MatchString(commit) {
		return Receipt{}, invalid("collection commit %q must be 40 or 64 lowercase hex digits", commit)
	}
	pd, err := digest.Parse(d)
	if err != nil {
		return Receipt{}, invalid("collected digest: %v", err)
	}
	var bucket []byte
	switch kind {
	case QueueLesson:
		bucket = bucketLessons
	case QueueIncident:
		bucket = bucketIncidents
	default:
		return Receipt{}, invalid("queue kind %q must be %s or %s", kind, QueueLesson, QueueIncident)
	}
	mid := "collected:" + string(kind) + ":" + id + ":" + pd.Hex()[:16]
	return s.mutate(mid, opCollectQueueItem, collectRequest{kind, id, d, commit}, func(tx *bolt.Tx, r *Receipt) error {
		b := directorBucket(tx, bucket)
		r.Record = "director/" + string(kind) + "/" + id
		at := &Collection{Commit: commit, Receipt: r.Sequence, Time: s.now().UTC()}
		if kind == QueueLesson {
			var l Lesson
			if err := getJSON(b, []byte(id), &l); err != nil {
				return err
			}
			switch {
			case l.Digest != d:
				return fmt.Errorf("store: lesson %s: %w: %s, not %s", id, ErrDigestMismatch, l.Digest, d)
			case l.Collected != nil:
				return invalid("lesson %s is already collected in commit %s", id, l.Collected.Commit)
			}
			l.State, l.Collected = LessonCollected, at
			return putJSON(b, []byte(id), l)
		}
		var inc Incident
		if err := getJSON(b, []byte(id), &inc); err != nil {
			return err
		}
		switch {
		case inc.Digest != d:
			return fmt.Errorf("store: incident %s: %w: %s, not %s", id, ErrDigestMismatch, inc.Digest, d)
		case inc.Collected != nil:
			return invalid("incident %s is already collected in commit %s", id, inc.Collected.Commit)
		}
		inc.State, inc.Collected, inc.Observations = IncidentCollected, at, nil
		return putJSON(b, []byte(id), inc)
	})
}

// forEachJSON decodes every record of b, which may be nil, with fresh, and passes each to each.
func forEachJSON[T any](b *bolt.Bucket, fresh func() *T, each func(*T)) error {
	if b == nil {
		return nil
	}
	return b.ForEach(func(k, raw []byte) error {
		v := fresh()
		if err := decode(k, raw, v); err != nil {
			return err
		}
		each(v)
		return nil
	})
}
