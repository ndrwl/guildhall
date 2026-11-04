package steward

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MaxStatusBytes bounds the `director status --json` output the Steward reads from a copy.
const MaxStatusBytes = 8 << 20

// ErrNoHealth reports a status feed with no health, printed by a guildhall from before the Director health checks.
var ErrNoHealth = errors.New("the copy's director status reports no health; its guildhall predates the Steward's Director health checks")

// DirectorHealth is what the Steward reads from a copy's status feed for its Director health checks
// (docs/steward.md#director-health): the newest session's state, number and start, and the feed's health.
type DirectorHealth struct {
	State     string
	Number    int
	StartedAt time.Time
	// Supervisor is alive, gone, unverified or none.
	Supervisor string
	// LastTurn is the newest turn signal's time, zero when the runtime gave none.
	LastTurn     time.Time
	Waiting      int
	WaitingSince time.Time
	Release      string
	Pin          string
	Behind       bool
}

// ParseDirectorStatus reads the DirectorHealth of a copy from its `director status --json` output. The feed is the
// copy's own and grows over releases, so fields the Steward does not read are ignored; a feed with no health block
// is ErrNoHealth.
func ParseDirectorStatus(out []byte) (DirectorHealth, error) {
	if len(out) > MaxStatusBytes {
		return DirectorHealth{}, fmt.Errorf("director status: output exceeds %d bytes", MaxStatusBytes)
	}
	var feed struct {
		Session struct {
			State     string    `json:"state"`
			Number    int       `json:"number"`
			StartedAt time.Time `json:"started_at"`
		} `json:"session"`
		Health *struct {
			Supervisor string `json:"supervisor"`
			LastTurn   *struct {
				At time.Time `json:"at"`
			} `json:"last_turn"`
			Waiting      int       `json:"waiting"`
			WaitingSince time.Time `json:"waiting_since"`
			Release      string    `json:"release"`
			Pin          string    `json:"pin"`
			Behind       bool      `json:"behind"`
		} `json:"health"`
	}
	if err := json.Unmarshal(out, &feed); err != nil {
		return DirectorHealth{}, fmt.Errorf("director status: %w", err)
	}
	if feed.Health == nil {
		return DirectorHealth{}, ErrNoHealth
	}
	h := feed.Health
	d := DirectorHealth{State: feed.Session.State, Number: feed.Session.Number, StartedAt: feed.Session.StartedAt, Supervisor: h.Supervisor,
		Waiting: h.Waiting, WaitingSince: h.WaitingSince, Release: h.Release, Pin: h.Pin, Behind: h.Behind}
	if h.LastTurn != nil {
		d.LastTurn = h.LastTurn.At
	}
	return d, nil
}
