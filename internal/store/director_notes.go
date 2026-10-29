package store

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Nested buckets of the director bucket holding the Director's notes, keyed by note id, and every version of
// every note, keyed by zero-padded receipt sequence (docs/store.md#director-notes).
var (
	bucketNotes        = []byte("notes")
	bucketNoteVersions = []byte("note-versions")
)

// Operations recorded on note receipts.
const (
	opAddNote    = "add-note"
	opEditNote   = "edit-note"
	opRemoveNote = "remove-note"
)

// MaxNoteBytes bounds a note's text and MaxNoteReasonBytes a remove reason.
const (
	MaxNoteBytes       = 1024
	MaxNoteReasonBytes = 256
)

// NoteKind is what a note records.
type NoteKind string

const (
	// NotePromised is a promise to the Guildmaster.
	NotePromised NoteKind = "promised"
	// NoteGuildmaster is a preference or context the Guildmaster gave.
	NoteGuildmaster NoteKind = "guildmaster"
	// NotePlan is Director intent that no other record holds.
	NotePlan NoteKind = "plan"
)

// NoteKinds are the kinds, in the order notes are listed.
var NoteKinds = []NoteKind{NotePromised, NoteGuildmaster, NotePlan}

// NoteAction is what one note version did.
type NoteAction string

const (
	NoteAdded   NoteAction = "add"
	NoteEdited  NoteAction = "edit"
	NoteRemoved NoteAction = "remove"
)

// Note is one small record the Director keeps of something no other record holds, given to every new session at
// startup (docs/store.md#director-notes). A removed note stays, with Removed set.
type Note struct {
	// ID is "n<k>", k from the notes bucket's own sequence.
	ID   string   `json:"id"`
	Kind NoteKind `json:"kind"`
	// Text is one line of at most MaxNoteBytes.
	Text string `json:"text"`
	// Task is "<repository>/<id>" of an existing task, or empty.
	Task string `json:"task,omitempty"`
	// Session is the session that last changed the note, and ChangedAt when, in UTC.
	Session   int       `json:"session"`
	ChangedAt time.Time `json:"changed_at"`
	// Version is the receipt sequence of the note's latest version record.
	Version       uint64 `json:"version"`
	Removed       bool   `json:"removed,omitempty"`
	RemovedReason string `json:"removed_reason,omitempty"`
}

// NoteVersion is one add, edit or remove of a note, with the note as it then stood; receipts keep only digests,
// so these records are the notes' history.
type NoteVersion struct {
	// Sequence is the receipt sequence of the change, and Prior the note's version before it, 0 for an add.
	Sequence uint64     `json:"sequence"`
	Prior    uint64     `json:"prior,omitempty"`
	Note     string     `json:"note"`
	Action   NoteAction `json:"action"`
	Kind     NoteKind   `json:"kind"`
	Text     string     `json:"text"`
	Task     string     `json:"task,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Session  int        `json:"session"`
	At       time.Time  `json:"at"`
}

func noteRecord(id string) string { return "director/note/" + id }

// AddNote adds a note of kind with text, naming task ("<repository>/<id>") when it is not empty, written by
// session n, under the mutation id "note-add:<n>:<sha256(kind|task|text)>": replaying it returns the note it added.
// A kind other than NoteKinds, a blank, multi-line or oversized text, a malformed task and a session that is not
// the newest or is stopped are ErrInvalid; an unknown task or session is ErrNotFound.
func (s *Store) AddNote(n int, kind NoteKind, task, text string) (Note, Receipt, error) {
	if !slices.Contains(NoteKinds, kind) {
		return Note{}, Receipt{}, invalid("note kind %q must be one of promised, guildmaster and plan", kind)
	}
	if err := validateNoteText("note text", text, MaxNoteBytes); err != nil {
		return Note{}, Receipt{}, err
	}
	repo, taskID, err := splitNoteTask(task)
	if err != nil {
		return Note{}, Receipt{}, err
	}
	if n < 1 {
		return Note{}, Receipt{}, invalid("director session number %d must be positive", n)
	}
	req := struct {
		Session int      `json:"session"`
		Kind    NoteKind `json:"kind"`
		Task    string   `json:"task"`
		Text    string   `json:"text"`
	}{n, kind, task, text}
	id := fmt.Sprintf("note-add:%d:%s", n, hexSum(string(kind)+"|"+task+"|"+text))
	r, err := s.mutate(id, opAddNote, req, func(tx *bolt.Tx, r *Receipt) error {
		if err := checkWriterSession(tx, n); err != nil {
			return err
		}
		if task != "" {
			if _, err := getTask(tx, repo, taskID); err != nil {
				return err
			}
		}
		notes, err := createDirectorBucket(tx, bucketNotes)
		if err != nil {
			return err
		}
		k, err := notes.NextSequence()
		if err != nil {
			return err
		}
		note := Note{ID: "n" + strconv.FormatUint(k, 10), Kind: kind, Text: text, Task: task, Session: n, ChangedAt: s.now().UTC(), Version: r.Sequence}
		r.Record = noteRecord(note.ID)
		return putNote(tx, notes, note, NoteVersion{Note: note.ID, Action: NoteAdded})
	})
	if err != nil {
		return Note{}, Receipt{}, err
	}
	return s.noteOf(r)
}

// EditNote replaces the text of note id, written by session n, under the mutation id
// "note-edit:<id>:<sha256(text)>:<prior>", where prior is the note's version the edit follows: replaying it returns
// the edit's receipt, and the same text after another edit is a new version. A note changed since prior is
// ErrExists; a removed note, an invalid text or a session that is not the newest or is stopped is ErrInvalid; an
// unknown note is ErrNotFound.
func (s *Store) EditNote(n int, id, text string) (Note, Receipt, error) {
	if err := validateNoteText("note text", text, MaxNoteBytes); err != nil {
		return Note{}, Receipt{}, err
	}
	if n < 1 {
		return Note{}, Receipt{}, invalid("director session number %d must be positive", n)
	}
	var prior uint64
	if err := s.view(func(tx *bolt.Tx) error {
		cur, err := readNote(tx, id)
		if err != nil {
			return err
		}
		prior = cur.Version
		if cur.Text != text {
			return nil
		}
		// This text may be this very edit, already applied: it then follows the version before it.
		var v NoteVersion
		if err := getJSON(directorBucket(tx, bucketNoteVersions), seqKey(cur.Version), &v); err != nil {
			return err
		}
		if v.Action == NoteEdited {
			prior = v.Prior
		}
		return nil
	}); err != nil {
		return Note{}, Receipt{}, fmt.Errorf("store: note %s: %w", id, err)
	}
	req := struct {
		ID    string `json:"id"`
		Text  string `json:"text"`
		Prior uint64 `json:"prior"`
	}{id, text, prior}
	r, err := s.mutate(fmt.Sprintf("note-edit:%s:%s:%d", id, hexSum(text), prior), opEditNote, req, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = noteRecord(id)
		if err := checkWriterSession(tx, n); err != nil {
			return err
		}
		note, err := readNote(tx, id)
		switch {
		case err != nil:
			return err
		case note.Removed:
			return invalid("note %s is removed", id)
		case note.Version != prior:
			return fmt.Errorf("%w: note %s changed since version %d", ErrExists, id, prior)
		}
		note.Text, note.Session, note.ChangedAt, note.Version = text, n, s.now().UTC(), r.Sequence
		return putNote(tx, directorBucket(tx, bucketNotes), note, NoteVersion{Note: id, Action: NoteEdited, Prior: prior})
	})
	if err != nil {
		return Note{}, Receipt{}, err
	}
	return s.noteOf(r)
}

// RemoveNote removes note id, written by session n, with an optional one-line reason of at most
// MaxNoteReasonBytes, under the mutation id "note-remove:<id>": the note stays with Removed set, and replaying
// returns the removal's receipt. A note already removed, an invalid reason or a session that is not the newest or
// is stopped is ErrInvalid; an unknown note is ErrNotFound.
func (s *Store) RemoveNote(n int, id, reason string) (Note, Receipt, error) {
	if reason != "" {
		if err := validateNoteText("remove reason", reason, MaxNoteReasonBytes); err != nil {
			return Note{}, Receipt{}, err
		}
	}
	if n < 1 {
		return Note{}, Receipt{}, invalid("director session number %d must be positive", n)
	}
	req := struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}{id, reason}
	r, err := s.mutate("note-remove:"+id, opRemoveNote, req, func(tx *bolt.Tx, r *Receipt) error {
		r.Record = noteRecord(id)
		if err := checkWriterSession(tx, n); err != nil {
			return err
		}
		note, err := readNote(tx, id)
		switch {
		case err != nil:
			return err
		case note.Removed:
			return invalid("note %s is already removed", id)
		}
		prior := note.Version
		note.Session, note.ChangedAt, note.Version, note.Removed, note.RemovedReason = n, s.now().UTC(), r.Sequence, true, reason
		return putNote(tx, directorBucket(tx, bucketNotes), note, NoteVersion{Note: id, Action: NoteRemoved, Prior: prior, Reason: reason})
	})
	if err != nil {
		return Note{}, Receipt{}, err
	}
	return s.noteOf(r)
}

// LiveNotes returns the notes that are not removed, ordered by kind as NoteKinds lists them and then by id.
func (s *Store) LiveNotes() ([]Note, error) {
	var out []Note
	err := s.view(func(tx *bolt.Tx) error {
		b := directorBucket(tx, bucketNotes)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var note Note
			if err := decode(k, v, &note); err != nil {
				return err
			}
			if !note.Removed {
				out = append(out, note)
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("store: live notes: %w", err)
	}
	slices.SortFunc(out, func(a, b Note) int {
		return cmp.Or(cmp.Compare(slices.Index(NoteKinds, a.Kind), slices.Index(NoteKinds, b.Kind)), cmp.Compare(noteNumber(a.ID), noteNumber(b.ID)))
	})
	return out, nil
}

// NoteVersions returns every version of note id, oldest first, or ErrNotFound when it has none.
func (s *Store) NoteVersions(id string) ([]NoteVersion, error) {
	var out []NoteVersion
	err := s.view(func(tx *bolt.Tx) error {
		b := directorBucket(tx, bucketNoteVersions)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var nv NoteVersion
			if err := decode(k, v, &nv); err != nil {
				return err
			}
			if nv.Note == id {
				out = append(out, nv)
			}
			return nil
		})
	})
	if err == nil && len(out) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: versions of note %s: %w", id, err)
	}
	return out, nil
}

// noteOf reads the note a note receipt names.
func (s *Store) noteOf(r Receipt) (Note, Receipt, error) {
	id := strings.TrimPrefix(r.Record, noteRecord(""))
	var note Note
	if err := s.view(func(tx *bolt.Tx) error {
		var err error
		note, err = readNote(tx, id)
		return err
	}); err != nil {
		return Note{}, Receipt{}, fmt.Errorf("store: note %s: %w", id, err)
	}
	return note, r, nil
}

// putNote writes note and its version record v, completed from note, at the note's Version.
func putNote(tx *bolt.Tx, notes *bolt.Bucket, note Note, v NoteVersion) error {
	versions, err := createDirectorBucket(tx, bucketNoteVersions)
	if err != nil {
		return err
	}
	v.Sequence, v.Kind, v.Text, v.Task, v.Session, v.At = note.Version, note.Kind, note.Text, note.Task, note.Session, note.ChangedAt
	if err := putJSON(versions, seqKey(v.Sequence), v); err != nil {
		return err
	}
	return putJSON(notes, []byte(note.ID), note)
}

func readNote(tx *bolt.Tx, id string) (Note, error) {
	var note Note
	if err := getJSON(directorBucket(tx, bucketNotes), []byte(id), &note); err != nil {
		return Note{}, fmt.Errorf("note %s: %w", id, err)
	}
	return note, nil
}

// checkWriterSession refuses a Director write unless session n is the newest session and not stopped.
func checkWriterSession(tx *bolt.Tx, n int) error {
	d, err := latestSession(tx)
	switch {
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("director session %d: %w", n, ErrNotFound)
	case err != nil:
		return err
	case d.Number < n:
		return fmt.Errorf("director session %d: %w", n, ErrNotFound)
	case d.Number > n:
		return invalid("director session %d is superseded by session %d", n, d.Number)
	case d.State == DirectorStopped:
		return invalid("director session %d is stopped", n)
	}
	return nil
}

// validateNoteText checks a note's text or remove reason: validateText's rules, and one line.
func validateNoteText(name, v string, limit int) error {
	if err := validateText(name, v, limit); err != nil {
		return err
	}
	if strings.ContainsAny(v, "\r\n") {
		return invalid("%s must be one line", name)
	}
	return nil
}

// splitNoteTask splits a note's task "<repository>/<id>", empty for none.
func splitNoteTask(task string) (string, string, error) {
	if task == "" {
		return "", "", nil
	}
	repo, id, ok := strings.Cut(task, "/")
	if !ok {
		return "", "", invalid("note task %q must be <repository>/<id>", task)
	}
	if err := validateIDs(repo, id); err != nil {
		return "", "", err
	}
	return repo, id, nil
}

// noteNumber is k of note id "n<k>", 0 for any other id.
func noteNumber(id string) uint64 {
	k, _ := strconv.ParseUint(strings.TrimPrefix(id, "n"), 10, 64)
	return k
}

func hexSum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
