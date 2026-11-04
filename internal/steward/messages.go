package steward

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// MessageDropPath is where, relative to a Director copy's directory, the Steward copies the envelope of one Steward
// message, which `messages receive --copy` reads, receives and deletes (docs/steward.md#steward-messages). The
// Steward delivers one message at a time per copy, so one fixed path serves.
const MessageDropPath = "state/steward/message.json"

// ReceiveCommand is the remote command that receives the Steward message at MessageDropPath into the copy's inbox:
// `<guildhall> messages receive --copy <copyDir>`.
func ReceiveCommand(guildhall, copyDir string) []string {
	return []string{guildhall, "messages", "receive", "--copy", copyDir}
}

// Deliver delivers one Steward message, envelope, to Director d on machine m: it copies the envelope to the copy's
// MessageDropPath with t and runs ReceiveCommand there with run, and returns what the copy printed, delivered with
// its receipt or refused with its reason. An error means the delivery did not happen or its result is unknown, as
// when the machine is unreachable or its guildhall too old to know the command, and the message stays pending.
// tmpDir holds the local file scp copies.
func Deliver(ctx context.Context, t Copier, run Runner, m Machine, d Director, envelope []byte, tmpDir string) (messaging.ReceiveResult, error) {
	f, err := os.CreateTemp(tmpDir, "steward-message-*.json")
	if err != nil {
		return messaging.ReceiveResult{}, err
	}
	src := f.Name()
	defer func() { _ = os.Remove(src) }()
	_, werr := f.Write(envelope)
	if err := f.Close(); werr != nil || err != nil {
		return messaging.ReceiveResult{}, fmt.Errorf("write %s: %w", src, firstErr(werr, err))
	}
	if err := t.Put(ctx, src, Remote{Alias: m.SSHAlias, Path: d.CopyDir + "/" + MessageDropPath}); err != nil {
		return messaging.ReceiveResult{}, err
	}
	out, err := run.Run(ctx, m.SSHAlias, ReceiveCommand(m.Guildhall(), d.CopyDir))
	if err != nil {
		return messaging.ReceiveResult{}, err
	}
	return messaging.ParseReceiveResult(out)
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Report is one Steward message the scheduled run found to send: a steward-report of kind about the copy of
// Director on Machine.
type Report struct {
	Machine  string
	Director string
	Kind     string
	// Key names what the report is about, such as the rollout receipt, so one issue makes one message id.
	Key     string
	Subject string
	Text    string
}

// ReportID is the message id of r: its kind and the first 16 hex digits of the sha256 of its copy and key, so one
// issue on one copy has one id however often a run finds it.
func ReportID(r Report) string {
	return r.Kind + "." + shortDigest(r.Machine+"/"+r.Director+"\n"+r.Key)
}

// Destination is the outbox destination of a message to Director on machine: "<machine>/<director>".
func Destination(machine, director string) string { return machine + "/" + director }

// Envelope is the guildhall.director-message/1 envelope of r, from the Steward to the Director it is about.
func (r Report) Envelope() ([]byte, error) {
	e := messaging.DirectorEnvelope{Schema: messaging.DirectorSchemaPrefix + "1", ID: ReportID(r), From: store.SenderSteward, To: r.Director,
		Type: store.MessageStewardReport, Kind: r.Kind, Subject: r.Subject, Text: r.Text}
	m, err := messaging.ExternalMessage(store.SenderSteward, r.Director, e)
	if err != nil {
		return nil, err
	}
	if err := store.ValidateExternalMessage(m); err != nil {
		return nil, err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// DirectorOf finds Director name on the roster machine labelled machine.
func (r *Roster) DirectorOf(machine, name string) (Machine, Director, bool) {
	m, ok := r.Machine(machine)
	if !ok {
		return Machine{}, Director{}, false
	}
	for _, d := range m.Directors {
		if d.Name == name {
			return m, d, true
		}
	}
	return Machine{}, Director{}, false
}

// shortDigest is the first 16 hex digits of the sha256 of s.
func shortDigest(s string) string { return digest.Of([]byte(s)).Hex()[:16] }
