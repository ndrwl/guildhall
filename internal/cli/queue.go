package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

// maxQueueRead bounds what lesson propose, incident file and incident observe read before refusing a body as too big,
// so a body over store.MaxQueueItemBytes is refused with its size without reading without end.
const maxQueueRead = 16 << 20

// utf8BOM is the UTF-8 byte order mark readQueueInput drops.
var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// MaxPendingJSONBytes bounds the items `lesson list --json --pending` and `incident list --json --pending` print, so a
// Steward reading them over SSH reads a bounded answer; the rest is printed by a later list, once these are collected.
const MaxPendingJSONBytes = 1 << 20

// queueCopy selects the Director copy a lesson or incident command acts on, --copy or copy discovery, and returns its
// store path and the command's positional arguments; code is non-negative when the command is done, having printed
// an error. It runs no configuration admission, as director mode does not, so an incident about a refused managed
// copy can still be filed; it refuses a Steward copy and a copy with no store.
func (a *App) queueCopy(name string, args []string, register func(*flag.FlagSet)) (storePath string, positional []string, code int) {
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	if register != nil {
		register(fl)
	}
	if err := parseInterspersed(fl, args); err != nil {
		return "", nil, a.usageError(name + ": " + err.Error())
	}
	var cfgPath string
	switch err := cp.apply(a, nil, &cfgPath, nil); {
	case err != nil:
		return "", nil, a.usageError(name + ": " + err.Error())
	case cp.dir == "":
		return "", nil, a.usageError(name + ": " + cp.missing(a).Error())
	}
	if copyStoreFile(cp.dir) == stewardStoreFile {
		return "", nil, a.fail(fmt.Errorf("%s: %s is a Steward copy; lessons and incidents are filed on a Director copy", name, cp.dir))
	}
	storePath = filepath.Join(cp.dir, directorStoreFile)
	if _, err := os.Stat(storePath); errors.Is(err, fs.ErrNotExist) {
		return "", nil, a.fail(fmt.Errorf("%s: %w", name, noStore(storePath)))
	}
	return storePath, fl.Args(), -1
}

// readQueueInput reads what from a file, or from standard input for "-": a leading UTF-8 byte order mark is dropped,
// UTF-16 is refused and CRLF line endings become LF, so the same words file the same content on every platform. A
// result over store.MaxQueueItemBytes is refused with its size.
func (a *App) readQueueInput(what, name string) ([]byte, error) {
	r := a.Stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxQueueRead+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if len(data) > maxQueueRead {
		return nil, fmt.Errorf("%s is more than %d bytes; the limit is %d", what, maxQueueRead, store.MaxQueueItemBytes)
	}
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		return nil, fmt.Errorf("%s is UTF-16; write it as UTF-8, for example with Set-Content -Encoding utf8", what)
	}
	data = bytes.ReplaceAll(bytes.TrimPrefix(data, utf8BOM), []byte("\r\n"), []byte("\n"))
	if len(data) > store.MaxQueueItemBytes {
		return nil, fmt.Errorf("%s is %d bytes; the limit is %d", what, len(data), store.MaxQueueItemBytes)
	}
	return data, nil
}

// lesson runs `guildhall lesson propose|list|resolve` (docs/director-modes.md#lessons).
func (a *App) lesson(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "propose":
			return a.lessonPropose(args[1:])
		case "list":
			return a.lessonList(args[1:])
		case "resolve":
			return a.lessonResolve(args[1:])
		}
	}
	return a.usageError("lesson: the commands are propose, list and resolve")
}

// lessonPropose runs `guildhall lesson propose <file|-> [--id <id>] [--copy <dir>]`: it files the Markdown lesson,
// with its "# " title and "## Evidence" and "## Limits" sections, as proposed, under --id or the id its content gives
// (store.DefaultLessonID). Filing the same lesson again prints it again; another under the same id exits 1.
func (a *App) lessonPropose(args []string) int {
	const name = "lesson propose"
	var id string
	path, pos, code := a.queueCopy(name, args, func(fl *flag.FlagSet) {
		fl.StringVar(&id, "id", "", "the lesson's id; by default l- and 12 hex digits of its content's sha256")
	})
	if code >= 0 {
		return code
	}
	if len(pos) != 1 {
		return a.usageError(name + ": give one lesson file, or - for standard input")
	}
	body, err := a.readQueueInput("lesson", pos[0])
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if id == "" {
		id = store.DefaultLessonID(string(body))
	}
	got, err := withStoreClock(path, false, a.Now, func(s *store.Store) (store.Lesson, error) {
		l, _, err := s.ProposeLesson(store.Lesson{ID: id, Body: string(body)})
		return l, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	return a.printf("lesson %s %s: %s (receipt %d)\n", got.ID, got.State, got.Title, got.Receipt)
}

// lessonResolve runs `guildhall lesson resolve <id> adopted|declined [--copy <dir>]`, the Guildmaster's local decision
// on a lesson that is not collected.
func (a *App) lessonResolve(args []string) int {
	const name = "lesson resolve"
	path, pos, code := a.queueCopy(name, args, nil)
	if code >= 0 {
		return code
	}
	if len(pos) != 2 || (pos[1] != string(store.LessonAdopted) && pos[1] != string(store.LessonDeclined)) {
		return a.usageError(name + ": give the lesson id and adopted or declined")
	}
	got, err := withStoreClock(path, false, a.Now, func(s *store.Store) (store.Lesson, error) {
		l, _, err := s.ResolveLesson(pos[0], store.LessonState(pos[1]))
		return l, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	return a.printf("lesson %s %s\n", got.ID, got.State)
}

// queueListFlags are the flags lesson list and incident list share.
type queueListFlags struct{ json, pending bool }

func (f *queueListFlags) register(fl *flag.FlagSet) {
	fl.BoolVar(&f.json, "json", false, "print JSON")
	fl.BoolVar(&f.pending, "pending", false, "list only the items not yet collected")
}

// lessonListJSON is what lesson list --json prints.
type lessonListJSON struct {
	Origin    string         `json:"origin"`
	Lessons   []store.Lesson `json:"lessons"`
	Truncated bool           `json:"truncated"`
}

// lessonList runs `guildhall lesson list [--json] [--pending] [--copy <dir>]`: every lesson in the order filed, or only
// those not yet collected, one line each, or the copy's queue origin and the lessons as JSON. A pending JSON list is
// capped at MaxPendingJSONBytes of lessons, with truncated set when lessons were left out.
func (a *App) lessonList(args []string) int {
	const name = "lesson list"
	var f queueListFlags
	path, pos, code := a.queueCopy(name, args, f.register)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(fmt.Sprintf("%s: unexpected argument %q", name, pos[0]))
	}
	if f.json && f.pending {
		if err := ensureReceiptDir(path); err != nil {
			return a.fail(fmt.Errorf("%s: %w", name, err))
		}
	}
	out := lessonListJSON{Lessons: []store.Lesson{}}
	_, err := withStore(path, true, func(s *store.Store) (struct{}, error) {
		var err error
		if out.Origin, err = queueOrigin(s); err != nil {
			return struct{}{}, err
		}
		all, err := s.Lessons()
		for _, l := range all {
			if !f.pending || l.Collected == nil {
				out.Lessons = append(out.Lessons, l)
			}
		}
		return struct{}{}, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if f.json {
		if f.pending {
			out.Lessons, out.Truncated = capJSON(out.Lessons, MaxPendingJSONBytes)
		}
		return a.printJSON(out)
	}
	if len(out.Lessons) == 0 {
		return a.printf("No lessons.\n")
	}
	var b strings.Builder
	for _, l := range out.Lessons {
		fmt.Fprintf(&b, "%s %s %s\n", l.ID, l.State, l.Title)
	}
	return a.printf("%s", b.String())
}

// incident runs `guildhall incident file|observe|list|resolve` (docs/messaging.md#maintenance-tickets-to-the-steward).
func (a *App) incident(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "file":
			return a.incidentFile(args[1:])
		case "observe":
			return a.incidentObserve(args[1:])
		case "list":
			return a.incidentList(args[1:])
		case "resolve":
			return a.incidentResolve(args[1:])
		}
	}
	return a.usageError("incident: the commands are file, observe, list and resolve")
}

// incidentFile runs `guildhall incident file <file|-> [--copy <dir>]`: it files the strict-JSON incident report
// (store.IncidentReport) as open. Filing the same report again prints it again; another under the same id exits 1.
// Filing launches nothing, changes no policy and blocks no task.
func (a *App) incidentFile(args []string) int {
	const name = "incident file"
	path, pos, code := a.queueCopy(name, args, nil)
	if code >= 0 {
		return code
	}
	if len(pos) != 1 {
		return a.usageError(name + ": give one incident file, or - for standard input")
	}
	var report store.IncidentReport
	if err := a.readQueueJSON("incident", pos[0], store.IncidentSchema, &report); err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	got, err := withStoreClock(path, false, a.Now, func(s *store.Store) (store.Incident, error) {
		inc, _, err := s.FileIncident(report)
		return inc, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	return a.printf("incident %s %s (receipt %d)\n", got.Report.ID, got.State, got.Receipt)
}

// incidentObserve runs `guildhall incident observe <id> <file|-> [--copy <dir>]`: it links the strict-JSON
// observation (store.ObservationReport) to the incident as its next observation, never changing the incident.
func (a *App) incidentObserve(args []string) int {
	const name = "incident observe"
	path, pos, code := a.queueCopy(name, args, nil)
	if code >= 0 {
		return code
	}
	if len(pos) != 2 {
		return a.usageError(name + ": give the incident id and one observation file, or - for standard input")
	}
	var o store.ObservationReport
	if err := a.readQueueJSON("observation", pos[1], store.IncidentObservationSchema, &o); err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	got, err := withStoreClock(path, false, a.Now, func(s *store.Store) (store.IncidentObservation, error) {
		ob, _, err := s.ObserveIncident(pos[0], o)
		return ob, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	return a.printf("incident %s observation %d recorded (receipt %d)\n", got.Incident, got.Sequence, got.Receipt)
}

// readQueueJSON reads what with readQueueInput and decodes it strictly into v under schema, version
// store.IncidentSchemaVersion.
func (a *App) readQueueJSON(what, name, schema string, v any) error {
	data, err := a.readQueueInput(what, name)
	if err != nil {
		return err
	}
	if err := strictjson.Decode(data, v, strictjson.Options{Schema: schema, Version: store.IncidentSchemaVersion, MaxBytes: store.MaxQueueItemBytes}); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// incidentResolve runs `guildhall incident resolve <id> accepted|deferred|declined [--copy <dir>]`, the Guildmaster's
// local decision on an incident that is not collected. It launches nothing: an accepted incident is work for the
// Guildmaster to create as a task.
func (a *App) incidentResolve(args []string) int {
	const name = "incident resolve"
	path, pos, code := a.queueCopy(name, args, nil)
	if code >= 0 {
		return code
	}
	if len(pos) != 2 || !strings.Contains(" accepted deferred declined ", " "+pos[1]+" ") {
		return a.usageError(name + ": give the incident id and accepted, deferred or declined")
	}
	got, err := withStoreClock(path, false, a.Now, func(s *store.Store) (store.Incident, error) {
		inc, _, err := s.ResolveIncident(pos[0], store.IncidentState(pos[1]))
		return inc, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	return a.printf("incident %s %s\n", got.Report.ID, got.State)
}

// incidentListJSON is what incident list --json prints.
type incidentListJSON struct {
	Origin    string           `json:"origin"`
	Incidents []store.Incident `json:"incidents"`
	Truncated bool             `json:"truncated"`
}

// incidentList runs `guildhall incident list [--json] [--pending] [--copy <dir>]`: every incident in the order filed,
// or only those not yet collected, one line each, or the copy's queue origin and the incidents with their
// observations as JSON. A pending JSON list is capped at MaxPendingJSONBytes of incidents, with truncated set when
// incidents were left out.
func (a *App) incidentList(args []string) int {
	const name = "incident list"
	var f queueListFlags
	path, pos, code := a.queueCopy(name, args, f.register)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(fmt.Sprintf("%s: unexpected argument %q", name, pos[0]))
	}
	if f.json && f.pending {
		if err := ensureReceiptDir(path); err != nil {
			return a.fail(fmt.Errorf("%s: %w", name, err))
		}
	}
	out := incidentListJSON{Incidents: []store.Incident{}}
	_, err := withStore(path, true, func(s *store.Store) (struct{}, error) {
		var err error
		if out.Origin, err = queueOrigin(s); err != nil {
			return struct{}{}, err
		}
		all, err := s.Incidents()
		for _, inc := range all {
			if !f.pending || inc.Collected == nil {
				out.Incidents = append(out.Incidents, inc)
			}
		}
		return struct{}{}, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if f.json {
		if f.pending {
			out.Incidents, out.Truncated = capJSON(out.Incidents, MaxPendingJSONBytes)
		}
		return a.printJSON(out)
	}
	if len(out.Incidents) == 0 {
		return a.printf("No incidents.\n")
	}
	var b strings.Builder
	for _, inc := range out.Incidents {
		r := inc.Report
		fmt.Fprintf(&b, "%s %s %s/%s %s %s: %s", r.ID, inc.State, r.Repository, r.Task, r.Step, r.Attempt, errorLine(r.Error, 100))
		if n := len(inc.Observations); n > 0 {
			fmt.Fprintf(&b, " (%d observations)", n)
		}
		b.WriteString("\n")
	}
	return a.printf("%s", b.String())
}

// ensureReceiptDir creates, when missing, the directory of the copy whose store is at storePath that a Steward
// copies its collection receipt into (steward.CollectReceiptPath), since scp creates no directory. The pending JSON
// lists, which only a Steward's collection reads, create it before the Steward copies the receipt.
func ensureReceiptDir(storePath string) error {
	return os.MkdirAll(filepath.Dir(filepath.Join(filepath.Dir(storePath), filepath.FromSlash(steward.CollectReceiptPath))), 0o700)
}

// directorCollected runs `guildhall director collected [--copy <dir>]`, the fixed remote command a Steward's
// collection runs after copying its receipt to steward.CollectReceiptPath in the copy (docs/steward.md#collection):
// each receipt item of this copy's origin whose content digest is the item's is marked collected in the private
// config commit the receipt names; any other item is reported and stays pending. It prints a steward.CollectedReport
// and deletes the receipt. Like the lesson and incident commands it runs no configuration admission.
func (a *App) directorCollected(args []string) int {
	const name = "director collected"
	path, pos, code := a.queueCopy(name, args, nil)
	if code >= 0 {
		return code
	}
	if len(pos) > 0 {
		return a.usageError(fmt.Sprintf("%s: unexpected argument %q", name, pos[0]))
	}
	file := filepath.Join(filepath.Dir(path), filepath.FromSlash(steward.CollectReceiptPath))
	data, err := os.ReadFile(file)
	if err != nil {
		return a.fail(fmt.Errorf("%s: no collection receipt to read: %w", name, err))
	}
	receipt, err := steward.ParseCollectReceipt(data)
	if err != nil {
		return a.fail(fmt.Errorf("%s: %s: %w", name, file, err))
	}
	report, err := withStoreClock(path, false, a.Now, func(s *store.Store) (steward.CollectedReport, error) {
		out := steward.CollectedReport{Schema: steward.CollectedReportSchema, Items: []steward.MarkedResult{}}
		origin, err := queueOrigin(s)
		if err != nil {
			return out, err
		}
		for _, it := range receipt.Items {
			r := steward.MarkedResult{Kind: it.Kind, Origin: it.Origin, ID: it.ID, Status: steward.MarkCollected}
			r.Status, r.Error = markItem(s, origin, it)
			out.Items = append(out.Items, r)
		}
		return out, nil
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if err := os.Remove(file); err != nil {
		return a.fail(fmt.Errorf("%s: the items are marked, but the receipt stays: %w", name, err))
	}
	return a.printJSON(report)
}

// markItem marks one receipt item collected in s, whose queue origin is origin, and returns its status and, for any
// status but collected, why.
func markItem(s *store.Store, origin string, it steward.ReceiptItem) (string, string) {
	if it.Origin != origin {
		return steward.MarkMismatch, fmt.Sprintf("origin %s is not this copy's %q", it.Origin, origin)
	}
	var have string
	var collected *store.Collection
	var err error
	if it.Kind == store.QueueLesson {
		var l store.Lesson
		l, err = s.Lesson(it.ID)
		have, collected = l.Digest, l.Collected
	} else {
		var inc store.Incident
		inc, err = s.Incident(it.ID)
		have, collected = inc.Digest, inc.Collected
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return steward.MarkMissing, fmt.Sprintf("this copy has no %s %s", it.Kind, it.ID)
	case err != nil:
		return steward.MarkMismatch, err.Error()
	case have != it.Digest:
		return steward.MarkMismatch, fmt.Sprintf("its digest is %s, not %s", have, it.Digest)
	case collected != nil:
		return steward.MarkAlready, ""
	}
	if _, err := s.MarkCollected(it.Kind, it.ID, it.Digest, it.Commit); err != nil {
		return steward.MarkMismatch, err.Error()
	}
	return steward.MarkCollected, ""
}

// queueOrigin is the copy's queue origin, empty before its first lesson or incident.
func queueOrigin(s *store.Store) (string, error) {
	o, err := s.QueueOrigin()
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return o, err
}

// capJSON is the longest prefix of items whose JSON, together, is at most limit bytes, and whether items were left
// out.
func capJSON[T any](items []T, limit int) ([]T, bool) {
	total := 0
	for i, it := range items {
		b, err := json.Marshal(it)
		if err != nil {
			return items[:i], true
		}
		if total += len(b) + 1; total > limit {
			return items[:i], true
		}
	}
	return items, false
}

// errorLine is the first line of s, cut to at most n bytes.
func errorLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > n {
		cut := n
		for cut > 0 && (s[cut]&0xc0) == 0x80 {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// printf writes to a.Stdout, exiting 1 when it cannot.
func (a *App) printf(format string, args ...any) int {
	if _, err := fmt.Fprintf(a.Stdout, format, args...); err != nil {
		return 1
	}
	return 0
}
