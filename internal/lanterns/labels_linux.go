package lanterns

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/store"
)

// PublishLabels replaces the copy's Guildhall-owned labels file atomically with
// live, generation-verified Director and Artisan entries. It omits completed,
// released or unverified identities. Errors leave the prior generation to expire.
// The caller owns scheduling; concurrent writers can replace only whole files.
func PublishLabels(copyDir string, v store.DirectorView, now time.Time) error {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return err
	}
	labels := Labels{SchemaVersion: 1, BootID: strings.TrimSpace(string(boot)), PIDNamespace: ns, GeneratedAt: now.UTC(), ExpiresAt: now.UTC().Add(time.Minute), Entries: []Entry{}}
	add := func(id, label string, p store.ProcessIdentity) {
		if len(labels.Entries) >= 1000 {
			return
		}
		live, err := artisan.Lookup(p.PID)
		if err != nil || live.Token != p.Token {
			return
		}
		bootID, ticks, ok := strings.Cut(p.Token, ":")
		if !ok || bootID != labels.BootID {
			return
		}
		if _, err := strconv.ParseUint(ticks, 10, 64); err != nil {
			return
		}
		labels.Entries = append(labels.Entries, Entry{ID: id, Label: label, Source: "guildhall-lanterns", Selector: Selector{Kind: "process", PID: p.PID, StartTicks: ticks, Descendants: true}})
	}
	if d := v.Session; d != nil && d.State != store.DirectorStopped {
		add(fmt.Sprintf("director-%d", d.Number), fmt.Sprintf("Director %d", d.Number), d.Identity)
	}
	running := map[string]bool{}
	key := func(repo, task, step string, n int) string { return fmt.Sprintf("%s/%s/%s/%d", repo, task, step, n) }
	for _, a := range v.Attempts {
		if a.Outcome == "" {
			running[key(a.Repository, a.TaskID, a.Step, a.Number)] = true
		}
	}
	for _, o := range v.Owners {
		k := key(o.Repository, o.TaskID, o.Step, o.Attempt)
		if o.State == store.OwnerReleased || !running[k] {
			continue
		}
		// IDs are numeric and bounded; labels carry the operator-facing task name.
		add(fmt.Sprintf("artisan-%d", len(labels.Entries)), fmt.Sprintf("Artisan %s/%s %s #%d", o.Repository, o.TaskID, o.Step, o.Attempt), o.Containment.Worker)
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	dir := filepath.Join(copyDir, "director", "lanterns")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, "vitals-identities.json"), append(b, '\n'))
}
