package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/spec"
	"github.com/ndrwl/guildhall/internal/store"
)

// barStoreWait bounds how long a pane bridge waits for the store's lock to read its status bar, so a busy store
// delays the Artisan's start by at most this; a bar it cannot read names the task without its title or Asked: line.
const barStoreWait = 2 * time.Second

// withBar fills b's status bar fields (docs/artisan.md#workbench): the task's title, the Asked: line and the
// accepted spec's slice count from the store at b.Attempt.Store, the task page's URL from the [web] table of the
// copy's config, copyDir's or else the one beside the store, and the attempt's start, when its prompt file was
// written, just before the launch. Anything that does not read is left out rather than failing the launch.
func withBar(b launch.Bridge, copyDir, prompt string) launch.Bridge {
	k := b.Attempt.Key
	if st, err := os.Stat(prompt); err == nil {
		b.Start = st.ModTime()
	}
	cfgPath := filepath.Join(filepath.Dir(b.Attempt.Store), "config.toml")
	if copyDir != "" {
		cfgPath = filepath.Join(copyDir, "config.toml")
	}
	if cfg, err := config.LoadUnadmitted(cfgPath); err == nil && cfg.Web != nil {
		b.URL = taskPageURL(cfg.Web.Address, k.Repository, k.TaskID)
	}
	type records struct {
		task      store.Task
		docs      []store.DocumentRef
		decisions []store.Decision
	}
	r, err := withRecords(storeRecords{path: b.Attempt.Store, wait: barStoreWait}, true, nil, func(s *store.Store) (records, error) {
		var r records
		var err error
		if r.task, err = s.Task(k.Repository, k.TaskID); err != nil {
			return r, err
		}
		if r.docs, err = s.Documents(k.Repository, k.TaskID); err != nil {
			return r, err
		}
		r.decisions, err = s.Decisions(k.Repository, k.TaskID)
		return r, err
	})
	if err != nil {
		return b
	}
	b.Title = r.task.Title
	b.Asked = firstSentence(r.task.Detail)
	if data, ok := acceptedSpec(r.docs, r.decisions, filepath.Join(b.Attempt.StateDir, "documents")); ok {
		if goal := specGoal(data); goal != "" {
			b.Asked = goal
		}
		if entries, err := spec.Slices(data); err == nil {
			b.Slices = len(entries)
		}
	}
	return b
}

// taskPageURL is the task page of repo/task under the pages served at address (docs/web.md#the-task-page).
func taskPageURL(address, repo, task string) string {
	return webBaseURL(address) + "/" + repo + "/" + task
}

// acceptedSpec reads the task's newest accepted spec: the document an approved acceptance recorded as
// <decision>-spec, from the latest round, read from documentDir and checked against its recorded digest.
func acceptedSpec(docs []store.DocumentRef, decisions []store.Decision, documentDir string) ([]byte, bool) {
	var best *store.DocumentRef
	bestRound := 0
	for _, d := range decisions {
		if !d.Answered || d.Void {
			continue
		}
		i := slices.IndexFunc(docs, func(doc store.DocumentRef) bool { return doc.Name == d.ID+"-spec" })
		if i < 0 {
			continue
		}
		round := 0
		if j := strings.LastIndexByte(d.ID, '-'); j >= 0 {
			round, _ = strconv.Atoi(d.ID[j+1:])
		}
		if best == nil || round > bestRound || round == bestRound && docs[i].Name > best.Name {
			best, bestRound = &docs[i], round
		}
	}
	if best == nil {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(documentDir, filepath.FromSlash(best.Path)))
	if err != nil || digest.Of(data).Hex() != best.SHA256 {
		return nil, false
	}
	return data, true
}

// goalBullet opens the Summary block's goal line.
const goalBullet = "- **Goal:**"

// specGoal is the text of the `- **Goal:**` bullet of data's Summary block (spec.Summary), with its Markdown
// markers taken out, or empty when it has none.
func specGoal(data []byte) string {
	summary, ok := spec.Summary(data)
	if !ok {
		return ""
	}
	for _, l := range strings.Split(summary, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), goalBullet); ok {
			return strings.TrimSpace(strings.NewReplacer("**", "", "`", "").Replace(rest))
		}
	}
	return ""
}

// firstSentence is text's first sentence: up to and including the first full stop, question or exclamation mark
// followed by a space or the end, or up to its first blank line, on one line.
func firstSentence(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if i := strings.Index(text, "\n\n"); i >= 0 {
		text = text[:i]
	}
	text = strings.Join(strings.Fields(text), " ")
	end := len(text)
	for _, mark := range []string{". ", "? ", "! "} {
		if i := strings.Index(text, mark); i >= 0 {
			end = min(end, i+1)
		}
	}
	return text[:end]
}
