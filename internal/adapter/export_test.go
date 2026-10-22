package adapter

import "testing"

// TabHost names the tabHost interface for harnesses outside this package.
type TabHost = tabHost

// TabHostHarness is a terminal host defined outside this package under the
// tabHost contract test: Host and Foreign are two hosts of its kind, Session is
// Host's session, Sent returns the lines and keys that reached Host's
// terminal, in order, and Reopen returns a new host on Host's terminal that
// owns none of its tabs.
type TabHostHarness struct {
	Host, Foreign tabHost
	Session       string
	Sent          func() []string
	Reopen        func() tabHost
}

// RunTabHostContract runs the tabHost contract test, including recorded tabs
// and typing into them, against hosts from newHost.
func RunTabHostContract(t *testing.T, newHost func(t *testing.T) TabHostHarness) {
	under := func(t *testing.T) tabHostUnderTest {
		h := newHost(t)
		return tabHostUnderTest{host: h.Host, foreign: h.Foreign, session: h.Session, sent: h.Sent, reopen: h.Reopen}
	}
	testTabHost(t, under)
	testRecordedTabs(t, under)
}
