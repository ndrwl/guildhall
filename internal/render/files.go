package render

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// stageKind looks up the step's kind and checks it has the actor the caller renders for.
func stageKind(stage Stage, human bool) (workflow.Kind, error) {
	kind, ok := workflow.LookupKind(stage.Step.Type, stage.Step.Version)
	if !ok {
		return workflow.Kind{}, fmt.Errorf("render: step %q: unknown kind %s version %d", stage.Step.ID, stage.Step.Type, stage.Step.Version)
	}
	actor := stage.Step.Actor()
	if actor == "" || (actor == workflow.GuildmasterActor) != human {
		return workflow.Kind{}, fmt.Errorf("render: step %q (%s): %w", stage.Step.ID, kind.Type, ErrWrongActor)
	}
	if stage.Repository == "" || stage.Task == "" || stage.Step.ID == "" || stage.Attempt < 1 {
		return workflow.Kind{}, errors.New("render: stage needs a repository, task, step id and attempt number")
	}
	return kind, nil
}

func identity(stage Stage, kind workflow.Kind) Identity {
	return Identity{
		Repository: stage.Repository, Task: stage.Task, Step: stage.Step.ID,
		Kind: kind.Type, KindVersion: kind.Version, Attempt: stage.Attempt, Profile: stage.Step.Profile(),
	}
}

// normalise replaces CRLF and lone CR line endings with LF.
func normalise(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// slashName reads backslashes as separators so a name recorded on Windows is portable.
func slashName(name string) string { return strings.ReplaceAll(name, `\`, "/") }

// portable reports whether a slash-separated name is relative, without dot segments or a drive letter.
func portable(name string) bool {
	return fs.ValidPath(name) && name != "." && !strings.Contains(name, ":")
}

func readSource(what string, src Source) (File, string, error) {
	name := slashName(src.Name)
	if !portable(name) {
		return File{}, "", fmt.Errorf("render: %s name %q must be a relative slash-separated path", what, src.Name)
	}
	data, err := os.ReadFile(src.Path)
	if err != nil {
		return File{}, "", fmt.Errorf("render: %s %s: %w", what, name, err)
	}
	text := normalise(string(data))
	return File{Path: name, Digest: string(digest.Of([]byte(text))), Root: src.Root}, text, nil
}

func checkInput(in Input) (InputFile, []byte, error) {
	name := slashName(in.Name)
	if in.Port == "" || !portable(name) {
		return InputFile{}, nil, fmt.Errorf("render: input %q on port %q needs a port and a relative slash-separated name", in.Name, in.Port)
	}
	data, err := os.ReadFile(in.Path)
	if err != nil {
		return InputFile{}, nil, fmt.Errorf("render: input %s: %w", name, err)
	}
	got := digest.Of(data)
	if got.Hex() != in.SHA256 {
		return InputFile{}, nil, fmt.Errorf("render: input %s: %w: recorded %s, file is %s", name, ErrDigestMismatch, in.SHA256, got)
	}
	return InputFile{Port: in.Port, Path: name, Digest: string(got)}, data, nil
}

// prepareDir creates dir, or accepts an existing empty one, and reports whether it created it.
func prepareDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return false, fmt.Errorf("render: %w", err)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("render: %w", err)
	case len(entries) > 0:
		return false, fmt.Errorf("render: %s: %w", dir, ErrNotEmpty)
	}
	return false, nil
}

type foundSkill struct {
	name, root, dir string
}

// resolveSkills finds each named skill in the first root that holds a directory of that name.
func resolveSkills(names []string, roots []SkillRoot) ([]foundSkill, error) {
	var found []foundSkill
	for _, name := range names {
		if !workflow.ValidID(name) {
			return nil, fmt.Errorf("render: invalid skill name %q", name)
		}
		var hit *foundSkill
		for _, root := range roots {
			dir := filepath.Join(root.Dir, name)
			info, err := os.Lstat(dir)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("render: skill %q: %w", name, err)
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("render: skill %q in %s root is not a directory", name, root.Name)
			}
			hit = &foundSkill{name: name, root: root.Name, dir: dir}
			break
		}
		if hit == nil {
			return nil, fmt.Errorf("render: %w %q", ErrUnknownSkill, name)
		}
		found = append(found, *hit)
	}
	return found, nil
}

// copySkill copies every file of a skill byte for byte and computes its tree digest.
func copySkill(sk foundSkill, dst string) (Skill, error) {
	skill := Skill{Name: sk.name, Root: sk.root, Path: SkillsDir + "/" + sk.name, Files: []File{}}
	err := filepath.WalkDir(sk.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sk.dir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file", filepath.ToSlash(rel))
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Chmod(target, info.Mode().Perm()); err != nil {
			return err
		}
		skill.Files = append(skill.Files, File{Path: filepath.ToSlash(rel), Digest: string(digest.Of(data))})
		return nil
	})
	if err != nil {
		return Skill{}, fmt.Errorf("render: skill %q: %w", sk.name, err)
	}
	slices.SortFunc(skill.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	skill.Digest = string(TreeDigest(skill.Files))
	return skill, nil
}

// TreeDigest is a skill's tree digest over its files, as Skill.Digest defines it.
// The files may be in any order; files is not modified.
func TreeDigest(files []File) digest.Digest {
	sorted := slices.SortedFunc(slices.Values(files), func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	var lines strings.Builder
	for _, f := range sorted {
		fmt.Fprintf(&lines, "%s  %s\n", digest.Digest(f.Digest).Hex(), f.Path)
	}
	return digest.Of([]byte(lines.String()))
}
