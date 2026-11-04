package cli

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
)

// readDirectorHealth reads, with run, the status feed of each copy in roster the scheduled run's round did not
// find unreachable, and returns each copy's DirectorHealth by "<machine>/<director>" with what went wrong reading
// the others. A copy whose read fails keeps its conditions as they were (docs/steward.md#director-health).
func readDirectorHealth(ctx context.Context, roster *steward.Roster, run steward.Runner, round *collectRound) (map[string]steward.DirectorHealth, []string) {
	unreachable := map[string]bool{}
	if round != nil {
		for _, cc := range round.Copies {
			if cc.Error != "" {
				unreachable[steward.Destination(cc.Machine, cc.Director)] = true
			}
		}
	}
	out := map[string]steward.DirectorHealth{}
	var problems []string
	for _, m := range roster.Machines {
		for _, d := range m.Directors {
			name := steward.Destination(m.Label, d.Name)
			if unreachable[name] || ctx.Err() != nil {
				continue
			}
			b, err := run.Run(ctx, m.SSHAlias, steward.StatusCommand(m.Guildhall(), d.CopyDir))
			var h steward.DirectorHealth
			if err == nil {
				h, err = steward.ParseDirectorStatus(b)
			}
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: director status: %s", name, oneLine(err.Error())))
				continue
			}
			out[name] = h
		}
	}
	return out, problems
}

// directorCondition is one Director condition found on a copy: whether it holds, the mark naming what holds, and,
// when it holds, the report's subject and text, with whether it is reported at once or only once it has held for
// the span.
type directorCondition struct {
	kind    string
	holds   bool
	mark    string
	atOnce  bool
	subject string
	text    string
}

// directorConditions are the three conditions of h at now with span (docs/steward.md#director-health):
// director-stuck when messages wait and the Director has taken no turn for span since the later of its last turn
// and the oldest message's arrival; director-unhealthy when the session's supervisor is gone while the session is
// not stopped, or the session has stayed recovering for span; and release-behind when the session runs a release
// older than the copy's pin.
func directorConditions(h steward.DirectorHealth, span time.Duration, now time.Time) []directorCondition {
	stuck := directorCondition{kind: store.ReportDirectorStuck, atOnce: true}
	quiet := h.WaitingSince
	if h.LastTurn.After(quiet) {
		quiet = h.LastTurn
	}
	if h.Waiting > 0 && !quiet.IsZero() && now.Sub(quiet) >= span {
		stuck.holds, stuck.mark = true, quiet.UTC().Format(time.RFC3339Nano)
		messages := "1 message waits"
		if h.Waiting > 1 {
			messages = fmt.Sprintf("%d messages wait", h.Waiting)
		}
		last := "no turn signal from this session"
		if !h.LastTurn.IsZero() {
			last = "its last turn at " + h.LastTurn.UTC().Format(time.RFC3339)
		}
		session := "no Director session"
		if h.Number > 0 {
			session = fmt.Sprintf("session %d is %s", h.Number, h.State)
		}
		stuck.subject = fmt.Sprintf("No Director turn for %s while %s", now.Sub(quiet).Round(time.Minute), messages)
		stuck.text = fmt.Sprintf("The Steward read this copy's status at %s: %s, the oldest since %s, with %s; %s.\n"+
			"The Steward found no Director turn for at least %s while work waited.\n%s",
			now.UTC().Format(time.RFC3339), messages, h.WaitingSince.UTC().Format(time.RFC3339), last, session, span, stewardInformsOnly)
	}
	unhealthy := directorCondition{kind: store.ReportDirectorUnhealthy, atOnce: true}
	switch {
	case h.Supervisor == "gone":
		unhealthy.holds, unhealthy.mark = true, fmt.Sprintf("session %d supervisor gone", h.Number)
		unhealthy.subject = fmt.Sprintf("Session %d's supervisor is gone while the session is %s", h.Number, h.State)
		unhealthy.text = fmt.Sprintf("The Steward read this copy's status at %s: Director session %d is recorded %s, but its supervisor process is gone, "+
			"so no wake, watcher or scheduled work runs for it.\nThe Guildmaster decides whether to record it with director stop and start a new session.\n%s",
			now.UTC().Format(time.RFC3339), h.Number, h.State, stewardInformsOnly)
	case h.State == string(store.DirectorRecovering) && !h.StartedAt.IsZero() && now.Sub(h.StartedAt) >= span:
		unhealthy.holds, unhealthy.mark = true, fmt.Sprintf("session %d recovering", h.Number)
		unhealthy.subject = fmt.Sprintf("Session %d has been recovering for %s", h.Number, now.Sub(h.StartedAt).Round(time.Minute))
		unhealthy.text = fmt.Sprintf("The Steward read this copy's status at %s: Director session %d started at %s and is still recovering, "+
			"so it starts no new work until it is ready.\n%s", now.UTC().Format(time.RFC3339), h.Number, h.StartedAt.UTC().Format(time.RFC3339), stewardInformsOnly)
	}
	behind := directorCondition{kind: store.ReportReleaseBehind}
	if h.Behind {
		behind.holds, behind.mark = true, h.Release+" "+h.Pin
		behind.subject = fmt.Sprintf("Session %d runs %s, behind this copy's pin %s", h.Number, h.Release, h.Pin)
		behind.text = fmt.Sprintf("The Steward read this copy's status at %s: Director session %d's supervisor runs release %s, "+
			"older than the copy's pin %s, and has for at least %s; a director reload moves it onto the pin.\n%s",
			now.UTC().Format(time.RFC3339), h.Number, h.Release, h.Pin, span, stewardInformsOnly)
	}
	return []directorCondition{stuck, unhealthy, behind}
}

// directorReports records the conditions of each copy's health in the Steward store at storePath at now and returns
// a report for each that holds and is due: at once, or once it has held for span. The report's key is the
// condition's mark and the time the Steward first found it, so one condition is one message until it clears.
func directorReports(health map[string]steward.DirectorHealth, storePath string, span time.Duration, now time.Time) ([]steward.Report, []string) {
	var out []steward.Report
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(health)) {
		machine, director, _ := strings.Cut(name, "/")
		for _, c := range directorConditions(health[name], span, now) {
			since, err := withStore(storePath, false, func(s *store.Store) (time.Time, error) {
				return s.MarkCopyCondition(name, c.kind, c.mark, c.holds, now)
			})
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %s: %v", name, c.kind, err))
				continue
			}
			if !c.holds || !c.atOnce && now.Sub(since) < span {
				continue
			}
			out = append(out, steward.Report{Machine: machine, Director: director, Kind: c.kind,
				Key: c.mark + "\n" + since.UTC().Format(time.RFC3339Nano), Subject: truncateUTF8(c.subject, store.MaxSubjectBytes),
				Text: truncateUTF8(c.text, store.MaxMessageTextBytes)})
		}
	}
	return out, problems
}
