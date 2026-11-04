package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
)

// The Steward store's reserved triage tasks: one decision per collected incident and per collected lesson
// (docs/steward.md#triage).
const (
	stewardIncidentsTask = "incidents"
	stewardLessonsTask   = "lessons"
)

// Triage options: an incident is accepted, deferred or declined, a lesson adopted or declined.
const (
	triageAccept  = "accept"
	triageDefer   = "defer"
	triageAdopt   = "adopt"
	triageDecline = "decline"
)

// collectTimeout bounds one collection round, its SSH reads, commits, push and marks together.
const collectTimeout = 15 * time.Minute

// triageTask is the reserved task of kind's triage decisions.
func triageTask(kind store.QueueKind) string {
	if kind == store.QueueLesson {
		return stewardLessonsTask
	}
	return stewardIncidentsTask
}

// triageDecisionID is the id of the triage decision of the collected file at path in the private config: its kind and
// the first 16 hex digits of the path's sha256, so one item has one decision.
func triageDecisionID(kind store.QueueKind, path string) string {
	return string(kind) + "-" + digest.Of([]byte(path)).Hex()[:16]
}

// triageState is what an answer choice makes a collected item, and what follows from it; the Steward acts on no
// Director: an accepted incident is the Guildmaster's to make a task of, and an adopted lesson the Steward's to draft
// as an ordinary private-config change, which a later approved rollout ships.
func triageState(kind store.QueueKind, choice string) (state, outcome string) {
	switch {
	case kind == store.QueueIncident && choice == triageAccept:
		return string(store.IncidentAccepted), "the Guildmaster to create a task"
	case kind == store.QueueIncident && choice == triageDefer:
		return string(store.IncidentDeferred), "kept in the private config for a later look"
	case kind == store.QueueLesson && choice == triageAdopt:
		return string(store.LessonAdopted), "the Steward to draft it as a private-config change, shipped by a later approved rollout"
	}
	return "declined", "nothing follows"
}

// collectRound is what one collection round, the command's or the schedule's, did.
type collectRound struct {
	steward.CollectResult
	// Opened are the triage decisions this round opened, as "<repository>/<task>/<id>".
	Opened []string `json:"opened"`
}

// stewardCollector is the Collector over the Steward copy's private config, with the roster in it.
func (a *App) stewardCollector(cfg *config.Config) (*steward.Collector, error) {
	private, err := a.privateCheckout(cfg)
	if err != nil {
		return nil, err
	}
	roster, err := readRoster(private)
	if err != nil {
		return nil, err
	}
	gitPath, err := a.LookPath("git")
	if err != nil {
		return nil, err
	}
	copier, runner := a.Copier, a.Runner
	if copier == nil {
		scp, err := a.LookPath("scp")
		if err != nil {
			return nil, fmt.Errorf("scp is needed to copy each copy its collection receipt: %w", err)
		}
		copier = steward.Transport{Path: scp, Timeout: scpTimeout}
	}
	if runner == nil {
		ssh, err := a.LookPath("ssh")
		if err != nil {
			return nil, fmt.Errorf("ssh is needed to read each copy's lessons and incidents: %w", err)
		}
		runner = steward.SSH{Path: ssh, Timeout: sshTimeout}
	}
	return &steward.Collector{Roster: roster, Private: git.WorkTree{Program: git.Program{Path: gitPath, Timeout: scpTimeout}, Dir: private},
		Transport: copier, Runner: runner, Now: a.now}, nil
}

// runCollect runs one collection round of c on the named machines for the Steward store at storePath: it reads the
// Guildmaster's triage answers to record, collects, and then opens a triage decision for every collected file in the
// private config that is pending and has none, once each.
func (a *App) runCollect(ctx context.Context, c *steward.Collector, storePath string, machines []string) (*collectRound, error) {
	if err := ensureStewardRecords(storePath); err != nil {
		return nil, err
	}
	decisions, err := triageDecisions(storePath)
	if err != nil {
		return nil, err
	}
	head, err := c.Private.Head(ctx)
	if err != nil {
		return nil, err
	}
	pending, err := c.Private.Grep(ctx, head, steward.TriagePendingPatterns, "lessons", "incidents")
	if err != nil {
		return nil, err
	}
	var answers []steward.TriageAnswer
	for _, path := range pending {
		kind := pathKind(path)
		d, ok := decisions[triageDecisionID(kind, path)]
		if !ok || !d.Answered {
			continue
		}
		state, outcome := triageState(kind, d.Choice)
		answers = append(answers, steward.TriageAnswer{Kind: kind, Path: path, Decision: fmt.Sprintf("%s/%s/%s", d.Repository, d.TaskID, d.ID),
			State: state, Outcome: outcome, Note: d.Note})
	}
	res, err := c.Collect(ctx, machines, answers)
	out := &collectRound{Opened: []string{}}
	if res != nil {
		out.CollectResult = *res
	}
	if err != nil {
		return out, err
	}
	head, err = c.Private.Head(ctx)
	if err != nil {
		return out, err
	}
	if pending, err = c.Private.Grep(ctx, head, steward.TriagePendingPatterns, "lessons", "incidents"); err != nil {
		return out, err
	}
	for _, path := range pending {
		kind := pathKind(path)
		id := triageDecisionID(kind, path)
		if _, ok := decisions[id]; ok {
			continue
		}
		data, ok, err := c.Private.Show(ctx, head, path)
		if err != nil || !ok {
			return out, errors.Join(err, fmt.Errorf("%s vanished from %s", path, head))
		}
		item, err := steward.ParseCollected(path, data)
		if err != nil {
			return out, fmt.Errorf("%s: %w", path, err)
		}
		if item.Triage != steward.TriagePending {
			continue
		}
		if err := openTriage(storePath, path, head, item); err != nil {
			return out, err
		}
		out.Opened = append(out.Opened, fmt.Sprintf("%s/%s/%s", stewardRepository, triageTask(kind), id))
	}
	return out, nil
}

// pathKind is the kind of the collected file at path.
func pathKind(path string) store.QueueKind {
	if strings.HasPrefix(path, "lessons/") {
		return store.QueueLesson
	}
	return store.QueueIncident
}

// triageDecisions reads every triage decision in the Steward store at storePath, by id.
func triageDecisions(storePath string) (map[string]store.Decision, error) {
	return withStore(storePath, true, func(s *store.Store) (map[string]store.Decision, error) {
		out := map[string]store.Decision{}
		for _, task := range []string{stewardIncidentsTask, stewardLessonsTask} {
			ds, err := s.Decisions(stewardRepository, task)
			if err != nil {
				return nil, err
			}
			for _, d := range ds {
				out[d.ID] = d
			}
		}
		return out, nil
	})
}

// openTriage opens, once, the triage decision of the collected item in the file at path of commit: an approval bound
// to the item's content digest, with the options of its kind.
func openTriage(storePath, path, commit string, it steward.Collected) error {
	id := triageDecisionID(it.Kind, path)
	d := store.Decision{Repository: stewardRepository, TaskID: triageTask(it.Kind), ID: id, Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: it.Digest}}
	if it.Kind == store.QueueLesson {
		d.Options = []string{triageAdopt, triageDecline}
		d.Question = fmt.Sprintf("Adopt lesson %s from %s/%s, %q? Adopt has the Steward draft it as a private-config change that a later approved rollout ships; decline closes it. It is %s in the private config at %s.",
			it.ID, it.Machine, it.Director, it.Title, path, commit)
	} else {
		d.Options = []string{triageAccept, triageDefer, triageDecline}
		d.Question = fmt.Sprintf("Accept incident %s from %s/%s, %q? Accept leaves you to create a task for it; defer keeps it for later; decline closes it. Nothing is done to the Director. It is %s in the private config at %s.",
			it.ID, it.Machine, it.Director, it.Title, path, commit)
	}
	d.Question = truncateUTF8(d.Question, store.MaxTextBytes)
	_, err := withStore(storePath, false, func(s *store.Store) (struct{}, error) {
		_, err := s.OpenDecision("steward-triage:"+id, d)
		if errors.Is(err, store.ErrExists) {
			err = nil
		}
		return struct{}{}, err
	})
	return err
}

// stewardCollectCmd runs `guildhall steward collect [--machine <label>...] [--copy <dir>]`: one collection round
// on the named enrolled machines, every one when none is named (docs/steward.md#collection). It prints the round as
// JSON and exits 1 when the lock was busy, the round failed, or a copy or item could not be collected or marked.
func (a *App) stewardCollectCmd(args []string) int {
	const name = "steward collect"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	var machines labels
	fl.Var(&machines, "machine", "")
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError(name + ": " + err.Error())
	}
	var storePath, cfgPath string
	switch err := cp.apply(a, &storePath, &cfgPath, nil); {
	case err != nil:
		return a.usageError(name + ": " + err.Error())
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("%s: unexpected argument %q", name, fl.Arg(0)))
	case cp.dir == "":
		return a.usageError(name + ": " + cp.missing(a).Error())
	}
	storePath = filepath.Join(cp.dir, stewardStoreFile)
	if _, err := os.Stat(storePath); err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, noStore(storePath)))
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if cfg.Mode != config.ModeSteward {
		return a.fail(fmt.Errorf("%s: %s is a %s config, not the steward's", name, cfgPath, cfg.Mode))
	}
	c, err := a.stewardCollector(cfg)
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	round, err := a.runCollect(ctx, c, storePath, machines)
	if round != nil {
		if code := a.printJSON(round); code != 0 {
			return code
		}
	}
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	for _, cc := range round.Copies {
		if cc.Error != "" || slices.ContainsFunc(cc.Items, func(it steward.Item) bool { return it.Problem != "" }) {
			return 1
		}
	}
	return 0
}

// scheduledCollect is a Steward session's collection on its [steward] collect_every: deterministic code, no model
// turn, run off the supervisor's decision path one round at a time.
type scheduledCollect struct {
	a         *App
	cfg       *config.Config
	storePath string
	stderr    io.Writer
	// busyRounds counts the rounds in a row that found the lock busy; any other round resets it.
	busyRounds int
}

// lockBusyRounds is how many busy rounds in a row send the Steward's inbox a lock-busy message.
const lockBusyRounds = 3

// stewardCollection is the session's scheduled collection, nil when collect_every is unset, the default.
func (a *App) stewardCollection(cfg *config.Config, storePath string) (*workspaceCloseWatch, error) {
	if cfg.Steward == nil {
		return nil, nil
	}
	every, on, err := cfg.Steward.CollectEveryDuration()
	if err != nil || !on {
		return nil, err
	}
	sc := &scheduledCollect{a: a, cfg: cfg, storePath: storePath, stderr: a.Stderr}
	return &workspaceCloseWatch{every: every, round: sc.run}, nil
}

// run is one scheduled round. A busy lock skips it, and the third busy round in a row sends the Steward's inbox one
// lock-busy message naming the lock's owner, tagged with the director sender, and wakes the session; a reload, which
// starts the count again, sends no second one for the same owner. A round that opened triage decisions sends the
// Steward's inbox one triage-opened message per kind, tagged with the director sender, and wakes the session through
// the usual wake; a round with nothing new sends nothing and wakes nothing.
func (sc *scheduledCollect) run() {
	c, err := sc.a.stewardCollector(sc.cfg)
	if err != nil {
		sc.busyRounds = 0
		sc.warn("%v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	round, err := sc.a.runCollect(ctx, c, sc.storePath, nil)
	busy := errors.Is(err, steward.ErrBusy)
	if err != nil && !busy {
		sc.warn("%v", err)
	}
	// Steward messages follow the round, even one a busy lock skipped, whose copies then record no reach: the reports
	// the run finds, and every pending one.
	reached := round
	if busy {
		reached = nil
	}
	for _, p := range sc.a.runStewardMessages(ctx, c, sc.cfg, sc.storePath, reached) {
		if sc.stderr != nil {
			_, _ = fmt.Fprintf(sc.stderr, "guildhall: steward messages: %s\n", p)
		}
	}
	if busy {
		sc.busyRounds++
		var be *steward.BusyError
		if sc.busyRounds == lockBusyRounds && errors.As(err, &be) {
			sc.notify(lockBusyMessage(be), "lock-busy")
		}
		return
	}
	sc.busyRounds = 0
	if round == nil {
		return
	}
	for _, cc := range round.Copies {
		if cc.Error != "" {
			sc.warn("%s/%s: %s", cc.Machine, cc.Director, cc.Error)
		}
		for _, it := range cc.Items {
			if it.Problem != "" {
				sc.warn("%s/%s: %s %s: %s", cc.Machine, cc.Director, it.Kind, it.ID, it.Problem)
			}
		}
	}
	if len(round.Opened) == 0 {
		return
	}
	for _, kind := range []store.QueueKind{store.QueueIncident, store.QueueLesson} {
		var ids []string
		for _, d := range round.Opened {
			if strings.HasPrefix(d, stewardRepository+"/"+triageTask(kind)+"/") {
				ids = append(ids, d)
			}
		}
		if len(ids) == 0 {
			continue
		}
		sc.notify(triageMessage(kind, ids), "triage-opened")
	}
}

// notify sends m to the Steward's inbox unless a message with its id is there already, and wakes the session when
// it sent it; what fails is warned about under label.
func (sc *scheduledCollect) notify(m store.Message, label string) {
	sent, err := withStore(sc.storePath, false, func(s *store.Store) (bool, error) {
		if _, err := s.Message(m.ID); err == nil {
			return false, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return false, err
		}
		_, err := s.SendMessage(m)
		return err == nil, err
	})
	if err != nil {
		sc.warn("%s: %v", label, err)
		return
	}
	if !sent {
		return
	}
	if _, err := sc.a.wake(sc.storePath, m.ID, store.SenderGuildhall); err != nil {
		sc.warn("wake for %s: %v", m.ID, err)
	}
}

// lockBusyMessage is the Steward inbox message reporting that scheduled rounds keep finding the lock held. Its id
// is a digest of the lock's owner record, so each owner is reported once, across reloads too.
func lockBusyMessage(be *steward.BusyError) store.Message {
	record, _ := json.Marshal(be.Owner)
	since := "an unknown time"
	if !be.Owner.At.IsZero() {
		since = be.Owner.At.UTC().Format(time.RFC3339)
	}
	text := fmt.Sprintf("Scheduled collection found %s held %d rounds in a row: held by %s since %s. Tell the Guildmaster; "+
		"if its owner has stopped, they remove the lock by hand once they have confirmed it.", be.Path, lockBusyRounds, be.Owner, since)
	return store.Message{ID: "lock-busy." + digest.Of(record).Hex()[:16],
		Recipient: store.RecipientSteward, Repository: stewardRepository, TaskID: stewardRolloutsTask, Type: store.MessageLockBusy,
		Text: truncateUTF8(text, store.MaxMessageTextBytes), Sender: store.SenderDirector}
}

// triageMessage is the Steward inbox message reporting the triage decisions ids of kind a scheduled round opened.
func triageMessage(kind store.QueueKind, ids []string) store.Message {
	text := fmt.Sprintf("Collection opened %d triage %s: %s. Present each to the Guildmaster with the guildhall-steward-triage skill.",
		len(ids), plural(len(ids), "decision", "decisions"), strings.Join(ids, ", "))
	return store.Message{ID: "triage-opened." + string(kind) + "." + digest.Of([]byte(strings.Join(ids, "\n"))).Hex()[:16],
		Recipient: store.RecipientSteward, Repository: stewardRepository, TaskID: triageTask(kind), Type: store.MessageTriageOpened,
		Text: truncateUTF8(text, store.MaxMessageTextBytes), Sender: store.SenderDirector}
}

func (sc *scheduledCollect) warn(format string, args ...any) {
	if sc.stderr != nil {
		_, _ = fmt.Fprintf(sc.stderr, "guildhall: steward collect: "+format+"\n", args...)
	}
}
