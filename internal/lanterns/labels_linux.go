package lanterns

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/store"
)

func PublishLabels(copyDir string, v store.DirectorView, now time.Time) error {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return err
	}
	prefix := fmt.Sprintf("guildhall-%x-", sha256.Sum256([]byte(filepath.Clean(copyDir))))
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
		labels.Entries = append(labels.Entries, Entry{ID: prefix + id, Label: label, Source: "guildhall-lanterns", Selector: Selector{Kind: "process", PID: p.PID, StartTicks: ticks, Descendants: true}})
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
		add(fmt.Sprintf("artisan-%d", len(labels.Entries)), fmt.Sprintf("Artisan %s/%s %s #%d", o.Repository, o.TaskID, o.Step, o.Attempt), o.Containment.Worker)
	}
	path, err := labelsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	fields := map[string]json.RawMessage{}
	entries := []json.RawMessage{}
	b, err := os.ReadFile(path)
	if err == nil {
		var prior Labels
		if err := json.Unmarshal(b, &prior); err != nil {
			return err
		}
		if prior.SchemaVersion != 1 {
			return fmt.Errorf("unsupported labels schema %d", prior.SchemaVersion)
		}
		if prior.BootID == labels.BootID && prior.PIDNamespace == labels.PIDNamespace && prior.ExpiresAt.After(now) {
			if err := json.Unmarshal(b, &fields); err != nil {
				return err
			}
			var previous []json.RawMessage
			if err := json.Unmarshal(fields["entries"], &previous); err != nil {
				return err
			}
			for _, raw := range previous {
				var entry Entry
				if err := json.Unmarshal(raw, &entry); err != nil {
					return err
				}
				if entry.Source == "guildhall-lanterns" && strings.HasPrefix(entry.ID, prefix) {
					continue
				}
				if entry.Selector.Kind != "process" {
					continue
				}
				live, err := artisan.Lookup(entry.Selector.PID)
				if err != nil || live.Token != labels.BootID+":"+entry.Selector.StartTicks {
					continue
				}
				entries = append(entries, raw)
			}
			if prior.ExpiresAt.After(labels.ExpiresAt) {
				labels.ExpiresAt = prior.ExpiresAt
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range labels.Entries {
		raw, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		entries = append(entries, raw)
	}
	if len(entries) > 1000 {
		return fmt.Errorf("labels exceed 1000 entries")
	}
	b, err = json.Marshal(labels)
	if err != nil {
		return err
	}
	var current map[string]json.RawMessage
	if err := json.Unmarshal(b, &current); err != nil {
		return err
	}
	for k, value := range current {
		fields[k] = value
	}
	fields["entries"], err = json.Marshal(entries)
	if err != nil {
		return err
	}
	b, err = json.Marshal(fields)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'))
}

func labelsPath() (string, error) {
	if path := os.Getenv("VITALS_AXI_LABELS"); path != "" {
		return path, nil
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "bridge-lights", "vitals-identities.json"), nil
}
