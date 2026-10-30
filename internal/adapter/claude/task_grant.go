package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
)

// The Director copy's store file and document directory, as `--copy` names them (docs/running.md#copies).
const (
	copyStoreFile   = "director.store"
	copyDocumentDir = "state/documents"
)

// BoundTaskGrant is the task grant attempt attemptID, <step>-<n>, of task taskID in repository was bound with, read
// from the Director copy at copyDir's store, never from a plain file (docs/decisions/task-grants.md): the
// attempt's binding document, <step>-<n>-inputs, is checked against the SHA-256 the store holds for it, and its
// grant is given only while the store does not record it revoked. revoked is true for a bound grant the store
// records revoked, which grants nothing. A copy without a store, an attempt id of another form and an attempt with
// no binding, or a binding without a grant, have none. A store that cannot be opened or read, a binding file that is
// missing or does not match its digest, and a grant the store does not hold are errors: the attempt never runs with
// rules other than those recorded.
func BoundTaskGrant(copyDir, repository, taskID, attemptID string) (grant *store.BoundGrant, revoked bool, err error) {
	if copyDir == "" || repository == "" {
		return nil, false, nil
	}
	i := strings.LastIndex(attemptID, "-")
	if i < 0 {
		return nil, false, nil
	}
	if n, err := strconv.Atoi(attemptID[i+1:]); err != nil || n < 1 || strconv.Itoa(n) != attemptID[i+1:] {
		return nil, false, nil
	}
	path := filepath.Join(copyDir, copyStoreFile)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true, LockTimeout: config.DefaultLockWait})
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	docs, err := s.Documents(repository, taskID)
	if err != nil {
		return nil, false, err
	}
	// The binding's name is the engine's: interp records attempt <step>-<n>'s binding as <step>-<n>-inputs.
	name := attemptID + "-inputs"
	j := slices.IndexFunc(docs, func(d store.DocumentRef) bool { return d.Name == name })
	if j < 0 {
		return nil, false, nil
	}
	ref := docs[j]
	data, err := os.ReadFile(filepath.Join(copyDir, filepath.FromSlash(copyDocumentDir), filepath.FromSlash(ref.Path)))
	if err != nil {
		return nil, false, fmt.Errorf("binding %s: %w", ref.Name, err)
	}
	if got := digest.Of(data).Hex(); got != ref.SHA256 {
		return nil, false, fmt.Errorf("binding %s: sha256 %s, but the store recorded %s", ref.Name, got, ref.SHA256)
	}
	var b struct {
		Grant *store.BoundGrant `json:"grant"`
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, false, fmt.Errorf("binding %s: %w", ref.Name, err)
	}
	if b.Grant == nil {
		return nil, false, nil
	}
	for _, rule := range b.Grant.Rules {
		if err := store.CheckTaskGrantRule(rule); err != nil {
			return nil, false, fmt.Errorf("binding %s: task grant %s: %w", ref.Name, b.Grant.ID, err)
		}
	}
	grants, err := s.TaskGrants(repository, taskID)
	if err != nil {
		return nil, false, err
	}
	k := slices.IndexFunc(grants, func(g store.TaskGrant) bool { return g.ID == b.Grant.ID })
	switch {
	case k < 0:
		return nil, false, fmt.Errorf("binding %s names task grant %s, which the store does not hold", ref.Name, b.Grant.ID)
	case grants[k].Revoked != nil:
		return b.Grant, true, nil
	}
	return b.Grant, false, nil
}
