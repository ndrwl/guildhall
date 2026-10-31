package adapter

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/herdr"
)

// TabHostHarness is a terminal host defined outside this package under the
// TabHost contract test: Host and Foreign are two hosts of its kind, Session is
// Host's session, Sent returns the lines and keys that reached Host's
// terminal, in order, and Reopen returns a new host on Host's terminal that
// owns none of its tabs.
type TabHostHarness struct {
	Host, Foreign TabHost
	Session       string
	Sent          func() []string
	Reopen        func() TabHost
}

// RunTabHostContract runs the TabHost contract test, including recorded tabs,
// typing into them and workspaces, against hosts from newHost.
func RunTabHostContract(t *testing.T, newHost func(t *testing.T) TabHostHarness) {
	under := func(t *testing.T) tabHostUnderTest {
		h := newHost(t)
		return tabHostUnderTest{host: h.Host, foreign: h.Foreign, session: h.Session, sent: h.Sent, reopen: h.Reopen}
	}
	testTabHost(t, under)
	testRecordedTabs(t, under)
	testWorkspaces(t, under)
}

// HerdrStub is the stub Herdr this package's test binary plays, for one
// session, with its state in a directory of t's.
type HerdrStub struct {
	t            *testing.T
	dir, session string
	exe          string
}

// NewHerdrStub makes the test binary act as Herdr, with workspace w1 only,
// for the rest of t.
func NewHerdrStub(t *testing.T, session string) HerdrStub {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(tabsStubEnv, dir)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return HerdrStub{t: t, dir: dir, session: session, exe: exe}
}

// Client returns a new Herdr client on the stub's session.
func (s HerdrStub) Client() *herdr.Client {
	c, err := herdr.New(herdr.Options{Session: s.session, Command: []string{s.exe}})
	if err != nil {
		s.t.Fatal(err)
	}
	return c
}

func (s HerdrStub) touch(name string, data string) {
	if err := os.WriteFile(filepath.Join(s.dir, s.session+"-"+name), []byte(data), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

func stubID(id string) string { return strings.ReplaceAll(id, ":", "_") }

// Split gives tab a second pane.
func (s HerdrStub) Split(tab string) { s.touch("tab-"+stubID(tab)+"-split", "") }

// Move makes tab's pane report another tab.
func (s HerdrStub) Move(tab string) { s.touch("tab-"+stubID(tab)+"-moved-pane", "") }

// Busy starts a program in pane.
func (s HerdrStub) Busy(pane string) { s.touch("pane-"+stubID(pane), "busy") }

// Gone closes workspace ws outside any host.
func (s HerdrStub) Gone(ws string) { s.touch("closed-"+ws, "") }

// ChangeOnNextProcessInfo makes the next pane process-info in ws close that
// pane's tab.
func (s HerdrStub) ChangeOnNextProcessInfo(ws string) { s.touch("change-"+ws, "") }

// RemoveTabs leaves ws listed but holding no tab.
func (s HerdrStub) RemoveTabs(ws string) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, e := range entries {
		name, ok := strings.CutPrefix(e.Name(), s.session+"-tab-")
		if !ok || strings.Contains(name, "-") {
			continue
		}
		p := filepath.Join(s.dir, e.Name())
		if b, err := os.ReadFile(p); err == nil && string(b) == ws {
			if err := os.Remove(p); err != nil {
				s.t.Fatal(err)
			}
		}
	}
}

// Sent returns the lines that reached the stub, closes included.
func (s HerdrStub) Sent() []string {
	b, err := os.ReadFile(filepath.Join(s.dir, "sent-"+s.session))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}
