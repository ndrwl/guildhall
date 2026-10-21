package adapter

import "testing"

// TabHostHarness is a terminal host defined outside this package under the
// tabHost contract test: Host and Foreign are two hosts of its kind, Session is
// Host's session and Sent returns the lines and keys that reached Host's
// terminal, in order.
type TabHostHarness struct {
	Host, Foreign tabHost
	Session       string
	Sent          func() []string
}

// RunTabHostContract runs the tabHost contract test against hosts from newHost.
func RunTabHostContract(t *testing.T, newHost func(t *testing.T) TabHostHarness) {
	testTabHost(t, func(t *testing.T) tabHostUnderTest {
		h := newHost(t)
		return tabHostUnderTest{host: h.Host, foreign: h.Foreign, session: h.Session, sent: h.Sent}
	})
}
