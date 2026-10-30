package interp

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
)

// contextInput returns the task's context as an input, nil when the task has no entries. With k entries it is
// document context-<k>, written once when absent, so every attempt bound while the task has k entries shares one
// copy and an attempt bound earlier keeps the document it was given.
func (r *run) contextInput() (*render.Input, error) {
	k := len(r.contexts)
	if k == 0 {
		return nil, nil
	}
	name := contextName(k)
	if r.document(name) == nil {
		data := []byte(contextDocument(r.task, r.contexts))
		d := store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: r.contextPath(name), SHA256: digest.Of(data).Hex()}
		if err := r.put(d, data); err != nil {
			return nil, err
		}
	}
	d := r.document(name)
	return &render.Input{Port: ContextPort, Name: d.Path, SHA256: d.SHA256, Path: filepath.Join(r.e.DocumentDir, filepath.FromSlash(d.Path))}, nil
}

// contextName names the context document of a task's first k entries.
func contextName(k int) string { return "context-" + strconv.Itoa(k) }

// contextPath is where the task's context document name is kept: tasks/<repo>/<task>/<name>.md, beside no step's
// outputs, which are kept under <step>-<n>/.
func (r *run) contextPath(name string) string {
	return path.Join("tasks", r.task.Repository, r.task.ID, name+".md")
}

// isTaskContext reports whether in is the engine's task context input rather than an input a workflow wired to a
// port of the same name, which stale must still compare.
func (r *run) isTaskContext(in render.Input) bool {
	name, ok := strings.CutSuffix(path.Base(in.Name), ".md")
	k, err := strconv.Atoi(strings.TrimPrefix(name, "context-"))
	return in.Port == ContextPort && ok && err == nil && k > 0 && name == contextName(k) && in.Name == r.contextPath(name)
}

// contextDocument is the Markdown an Artisan reads as the task's context: a header saying whose it is, then each
// entry with its number, time and author and its text fenced, so the text is read as data and cannot open a section.
func contextDocument(t store.Task, entries []store.TaskContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Guildmaster context for task %s/%s\n\n", t.Repository, t.ID)
	b.WriteString("The Director recorded this context for the task, quoting the Guildmaster where the words are theirs.\n")
	b.WriteString("Entries are in the order they were added; a later entry supersedes an earlier one only where it says so.\n")
	for _, c := range entries {
		fmt.Fprintf(&b, "\n## Context %d\n\nAdded %s by %s.\n\n%s\n", c.Number, c.AddedAt.UTC().Format(time.RFC3339), c.Author, render.Fenced(c.Text))
	}
	return b.String()
}
