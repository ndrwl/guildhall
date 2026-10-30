package interp

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
)

// linkInputs returns, on port linked, the task's linked documents (docs/tasks.md#task-links), nothing when no link
// is active. With m link records it is first document links-<m>, written once when absent, an index of each active
// link's source, the path the Artisan finds it at, its digest and the Guildmaster's words; then each linked document,
// in link order, as recorded. Every linked file is checked against the digest its link pinned before anything is
// written, so an attempt is never bound without a document its task links.
func (r *run) linkInputs() ([]render.Input, error) {
	active := store.ActiveTaskLinks(r.links)
	if len(active) == 0 {
		return nil, nil
	}
	for _, l := range active {
		if err := r.checkLinked(l); err != nil {
			return nil, err
		}
	}
	name := linksName(len(r.links))
	if r.document(name) == nil {
		data := []byte(linksDocument(r.task, active))
		d := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: r.contextPath(name), SHA256: digest.Of(data).Hex()}
		if err := r.put(d, data); err != nil {
			return nil, err
		}
	}
	out := []render.Input{docInput(LinkedPort, *r.document(name), r.e.DocumentDir)}
	for _, l := range active {
		out = append(out, docInput(LinkedPort, l.Source, r.e.DocumentDir))
	}
	return out, nil
}

// checkLinked refuses a linked document whose file is missing or no longer holds the bytes its link pinned.
func (r *run) checkLinked(l store.TaskLink) error {
	refuse := func(why string) error {
		return fmt.Errorf("task %s/%s: link %d %s: %s", r.task.Repository, r.task.ID, l.Number, store.TaskLinkSource(l.Source), why)
	}
	data, err := os.ReadFile(filepath.Join(r.e.DocumentDir, filepath.FromSlash(l.Source.Path)))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return refuse("file missing")
	case err != nil:
		return refuse(err.Error())
	case digest.Of(data).Hex() != l.Source.SHA256:
		return refuse(fmt.Sprintf("file %s holds sha256:%s, not the linked sha256:%s", l.Source.Path, digest.Of(data).Hex(), l.Source.SHA256))
	}
	return nil
}

// linksName names the link index document of a task's first m link records.
func linksName(m int) string { return "links-" + strconv.Itoa(m) }

// linksDocument is the Markdown an Artisan reads to learn where each linked document came from and why: each active
// link with its number, source, the path beside the prompt, digest, time and author, and the Guildmaster's words
// fenced, so they are read as data and cannot open a section.
func linksDocument(t store.Task, active []store.TaskLink) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Linked documents for task %s/%s\n\n", t.Repository, t.ID)
	b.WriteString("The Director linked these recorded documents of other tasks to this task, quoting the Guildmaster on why.\n")
	b.WriteString("Each is a verified copy among this attempt's inputs, at the path given.\n")
	for _, l := range active {
		fmt.Fprintf(&b, "\n## Link %d: %s\n\n- Path: %s\n- Digest: sha256:%s\n- Linked %s by %s.\n\n%s\n", l.Number, store.TaskLinkSource(l.Source),
			path.Join("inputs", l.Source.Path), l.Source.SHA256, l.AddedAt.UTC().Format(time.RFC3339), l.Author, render.Fenced(l.Text))
	}
	return b.String()
}
