package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// SessionManifestSchema versions the manifest of a rendered Director session.
const SessionManifestSchema = "guildhall.session-manifest/1"

// The files Session writes inside the session directory, beside the skills and the runtime's files.
const (
	RoleFile     = "role.md"
	StartupFile  = "startup.md"
	ManifestFile = "manifest.json"
)

// StartupMessage is the first message for a runtime launched in the rendered session directory.
// A runtime launched elsewhere needs a message naming StartupFile by its absolute path.
const StartupMessage = "You are starting up: read " + StartupFile + " in this directory and follow it."

// SessionInput is one Director session to render (docs/rendering.md#director-sessions).
type SessionInput struct {
	// Dir is the session directory Session writes into; it must not exist or be empty.
	Dir string
	// Number is the session's number, from 1.
	Number int
	// Role is the Director's role prompt.
	Role Source
	// SkillsDir is the slash path, relative to Dir, the skills are copied into, such as ".claude/skills".
	SkillsDir string
	// RuntimeFiles are the runtime's own files, such as its settings, by slash path relative to Dir.
	RuntimeFiles map[string][]byte
	// InFlight is the in-flight snapshot, one line per item, in order.
	InFlight []string
	// Parked lists the attempts the Director parked, one line per attempt, in order: held on purpose, for
	// information and not work. The section is left out when there are none.
	Parked []string
	// Notes are the lines of the Director's live notes, in order: a "### <kind>" line before each kind's notes and
	// one line per note. The section is left out when there are none.
	Notes []string
	// NotesBytes is the total size of the live notes' text; over NotesWarnBytes the section opens with a warning to
	// compact them.
	NotesBytes int
}

// NotesWarnBytes is the live notes' total text size above which startup.md asks the Director to compact them. It is
// a warning only: no note is refused for the total.
const NotesWarnBytes = 8 << 10

// SessionRendered is the result of Session.
type SessionRendered struct {
	// RolePath, StartupPath and SkillsPath are the local paths written inside SessionInput.Dir.
	RolePath, StartupPath, SkillsPath string
	Manifest                          SessionManifest
}

// SessionManifest lists every file a session was rendered with and its digest; Session writes it as
// ManifestFile. Paths are slash-separated and relative to the session directory.
type SessionManifest struct {
	Schema  string `json:"schema"`
	Session int    `json:"session"`
	// Role is the role prompt as written to RoleFile, its digest of the LF-normalised text and the root it came from.
	Role    File   `json:"role"`
	Startup File   `json:"startup"`
	Files   []File `json:"files"`
	// Skills are every skill of the roots, by name, each from the first root holding it, with its tree digest.
	Skills []Skill `json:"skills"`
}

// Session renders a Director session into in.Dir: the role prompt at RoleFile with LF line endings; every
// skill found in roots, by directory name, the first root holding a name winning, copied byte for byte under
// in.SkillsDir; the runtime's files; StartupFile, which says the session is starting up, lists any notes, with a
// warning to compact them over NotesWarnBytes, the in-flight snapshot and then any parked attempts, as information; and ManifestFile.
// A root directory that does not exist holds no skills; a non-directory entry of a root, such as CREDITS.md, is not a skill and is skipped; a skill directory whose
// name is not an id, or that holds a symbolic link or other non-regular file, is refused.
// A non-empty in.Dir is refused with ErrNotEmpty, and a runtime file or skills directory path that is not
// relative and slash-separated, or that collides with another file, is refused.
// On error nothing Session wrote is left behind.
func Session(in SessionInput, roots []SkillRoot) (SessionRendered, error) {
	if in.Number < 1 {
		return SessionRendered{}, fmt.Errorf("render: session number %d must be positive", in.Number)
	}
	if err := checkSessionPaths(in); err != nil {
		return SessionRendered{}, err
	}
	for _, line := range in.InFlight {
		if strings.ContainsAny(line, "\r\n") {
			return SessionRendered{}, fmt.Errorf("render: in-flight item %q must be one line", line)
		}
	}
	for _, line := range in.Parked {
		if strings.ContainsAny(line, "\r\n") {
			return SessionRendered{}, fmt.Errorf("render: parked item %q must be one line", line)
		}
	}
	for _, line := range in.Notes {
		if strings.ContainsAny(line, "\r\n") {
			return SessionRendered{}, fmt.Errorf("render: note %q must be one line", line)
		}
	}
	m := SessionManifest{Schema: SessionManifestSchema, Session: in.Number, Files: []File{}, Skills: []Skill{}}
	role, text, err := readSource("role", in.Role)
	if err != nil {
		return SessionRendered{}, err
	}
	role.Path = RoleFile
	m.Role = role
	found, err := allSkills(roots)
	if err != nil {
		return SessionRendered{}, err
	}
	r := SessionRendered{RolePath: filepath.Join(in.Dir, RoleFile), StartupPath: filepath.Join(in.Dir, StartupFile),
		SkillsPath: filepath.Join(in.Dir, filepath.FromSlash(in.SkillsDir))}
	created, err := prepareDir(in.Dir)
	if err != nil {
		return SessionRendered{}, err
	}
	if err := r.write(in, &m, found, text); err != nil {
		entries, _ := os.ReadDir(in.Dir)
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(in.Dir, e.Name()))
		}
		if created {
			_ = os.Remove(in.Dir)
		}
		return SessionRendered{}, err
	}
	r.Manifest = m
	return r, nil
}

// checkSessionPaths refuses a skills directory or runtime file that is not portable, or that collides with
// another file Session writes.
func checkSessionPaths(in SessionInput) error {
	own := []string{RoleFile, StartupFile, ManifestFile}
	under := func(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }
	if !portable(in.SkillsDir) || slices.ContainsFunc(own, func(f string) bool { return under(f, in.SkillsDir) || under(in.SkillsDir, f) }) {
		return fmt.Errorf("render: skills directory %q must be a relative slash-separated path apart from the session's own files", in.SkillsDir)
	}
	for p := range in.RuntimeFiles {
		if !portable(p) || slices.Contains(own, p) || under(p, in.SkillsDir) || under(in.SkillsDir, p) {
			return fmt.Errorf("render: runtime file %q must be a relative slash-separated path apart from the session's other files", p)
		}
	}
	return nil
}

// allSkills finds every skill directory in roots, by name, the first root holding a name winning, in name order.
func allSkills(roots []SkillRoot) ([]foundSkill, error) {
	byName := map[string]foundSkill{}
	for _, root := range roots {
		entries, err := os.ReadDir(root.Dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("render: %s skill root: %w", root.Name, err)
		}
		for _, e := range entries {
			if _, seen := byName[e.Name()]; seen || !e.IsDir() {
				continue
			}
			if !workflow.ValidID(e.Name()) {
				return nil, fmt.Errorf("render: invalid skill name %q in %s root", e.Name(), root.Name)
			}
			byName[e.Name()] = foundSkill{name: e.Name(), root: root.Name, dir: filepath.Join(root.Dir, e.Name())}
		}
	}
	var found []foundSkill
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		found = append(found, byName[name])
	}
	return found, nil
}

func (r SessionRendered) write(in SessionInput, m *SessionManifest, found []foundSkill, role string) error {
	put := func(rel string, data []byte) (File, error) {
		path := filepath.Join(in.Dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return File{}, fmt.Errorf("render: %w", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return File{}, fmt.Errorf("render: %w", err)
		}
		return File{Path: rel, Digest: string(digest.Of(data))}, nil
	}
	if _, err := put(RoleFile, []byte(role)); err != nil {
		return err
	}
	if err := os.MkdirAll(r.SkillsPath, 0o755); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	for _, sk := range found {
		skill, err := copySkill(sk, filepath.Join(r.SkillsPath, sk.name))
		if err != nil {
			return err
		}
		skill.Path = in.SkillsDir + "/" + sk.name
		m.Skills = append(m.Skills, skill)
	}
	for _, p := range slices.Sorted(maps.Keys(in.RuntimeFiles)) {
		f, err := put(p, in.RuntimeFiles[p])
		if err != nil {
			return err
		}
		m.Files = append(m.Files, f)
	}
	var err error
	if m.Startup, err = put(StartupFile, []byte(composeStartup(in))); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	_, err = put(ManifestFile, append(b, '\n'))
	return err
}

func composeStartup(in SessionInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Guildhall Director session %d\n\n", in.Number)
	b.WriteString("You are starting up.\n")
	b.WriteString("This session is recovering: it starts no new work until you say it is ready.\n")
	b.WriteString("Read your notes, if there are any, then work through each in-flight item with the command it names.\n")
	b.WriteString("When nothing in flight needs you, run `guildhall director ready`; only then start new work.\n")
	if len(in.Notes) > 0 {
		b.WriteString("\n## Notes\n\n")
		if in.NotesBytes > NotesWarnBytes {
			fmt.Fprintf(&b, "Notes total %d KB, over %d KB: compact them (merge, shorten or remove).\n\n", (in.NotesBytes+1023)/1024, NotesWarnBytes>>10)
		}
		for _, line := range in.Notes {
			fmt.Fprintf(&b, "%s\n", line)
		}
	}
	var items strings.Builder
	for _, line := range in.InFlight {
		fmt.Fprintf(&items, "- %s\n", line)
	}
	fmt.Fprintf(&b, "\n## In flight\n\n%s", orNone(items.String()))
	if len(in.InFlight) == 0 {
		b.WriteString("\n")
	}
	if len(in.Parked) > 0 {
		b.WriteString("\n## Parked\n\n")
		b.WriteString("These attempts are held on purpose and are not work: nothing here needs you until its hold ends.\n\n")
		for _, line := range in.Parked {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	return b.String()
}
