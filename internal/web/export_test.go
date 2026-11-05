package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// FixtureToken is the form token of AnsweringFixture's server, fixed so its page is a stable golden file.
const FixtureToken = "fixture-token"

// FixtureAddress is AnsweringFixture's configured [web] address.
const FixtureAddress = "andrew-lap-wsl.tail5da40a.ts.net:4388"

// AnsweringFixture is the fixture's server at FixtureAddress answering for the login guildmaster, told by id,
// through a.
func AnsweringFixture(t *testing.T, id Identifier, guildmaster string, a Answerer) *Server {
	t.Helper()
	dir, sums := writeFixtureFiles(t)
	src := &fakeSource{tasks: map[string]Task{"guildhall/d122": fixtureTask(sums)}}
	s := New(Options{Source: src, Documents: dir, Location: time.UTC, Host: FixtureAddress, Identity: id, Guildmaster: guildmaster,
		Answerer: a})
	s.token, s.start = FixtureToken, FixtureStart
	return s
}

// FixtureStart is AnsweringFixture's server's start id, fixed so its page is a stable golden file.
const FixtureStart = "fixture-start"

// DraftsFixture is AnsweringFixture with drafts saved on the fixture task.
func DraftsFixture(t *testing.T, id Identifier, guildmaster string, a Answerer, drafts []Draft) *Server {
	t.Helper()
	s := AnsweringFixture(t, id, guildmaster, a)
	src := s.o.Source.(*fakeSource)
	task := src.tasks["guildhall/d122"]
	task.Drafts = drafts
	src.tasks["guildhall/d122"] = task
	return s
}

// PickDesign is the design round ask of PicksFixture whose design has a lettered open question.
const PickDesign = "design-4"

// PicksFixture is AnsweringFixture with design-4's design given a lettered open question, Q1, so its card is a pick;
// it returns the server and the ask's subject digest.
func PicksFixture(t *testing.T, id Identifier, guildmaster string, a Answerer) (*Server, string) {
	t.Helper()
	s := AnsweringFixture(t, id, guildmaster, a)
	body := "# The task page's answers\n\n## Open questions\n\n**Q1. Which address**\nThe page needs an address to be served on.\nOptions: (a) the tailnet one; (b) localhost only.\nRecommended: (a). The Guildmaster reads it from a phone.\n"
	return s, RewriteDocument(t, s, "tasks/guildhall/d122/designer-4/design.md", PickDesign, body)
}

// RewriteDocument writes body as the fixture document at p on s, a fixture's server, with its digest recorded on the
// document and as the subject of the ask about it, and returns that subject.
func RewriteDocument(t *testing.T, s *Server, p, ask, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.o.Documents, filepath.FromSlash(p)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	src := s.o.Source.(*fakeSource)
	task := src.tasks["guildhall/d122"]
	for i := range task.Documents {
		if task.Documents[i].Path == p {
			task.Documents[i].SHA256 = digest
		}
	}
	for i := range task.Asks {
		if task.Asks[i].ID == ask {
			task.Asks[i].Subject = "sha256:" + digest
		}
	}
	src.tasks["guildhall/d122"] = task
	return "sha256:" + digest
}

// WithoutOwnScripts is body with exactly the own <script> elements removed, so any "<script" left is not one.
func WithoutOwnScripts(body string) string { return withoutOwnScripts(body) }

// CheckOwnScripts fails t unless every script on w's page is an own script, with no attributes, pinned in w's CSP.
func CheckOwnScripts(t *testing.T, name string, w *httptest.ResponseRecorder) {
	t.Helper()
	checkOwnScripts(t, name, w)
}

// UpdateGolden reports whether the test run rewrites its golden files.
func UpdateGolden() bool { return *update }

// TailscaleSelf is this machine's own tailnet address, as an "ip:port", and the login tailscale status names for
// it; ok is false when tailscale is missing or not up.
func TailscaleSelf(t *testing.T) (string, string, bool) {
	t.Helper()
	if _, err := exec.LookPath("tailscale"); err != nil {
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return "", "", false
	}
	var st struct {
		BackendState string
		Self         *struct {
			TailscaleIPs []string
			UserID       int64
		}
		User map[string]struct{ LoginName string }
	}
	if json.NewDecoder(bytes.NewReader(out)).Decode(&st) != nil || st.BackendState != "Running" || st.Self == nil || len(st.Self.TailscaleIPs) == 0 {
		return "", "", false
	}
	u, ok := st.User[strconv.FormatInt(st.Self.UserID, 10)]
	if !ok || u.LoginName == "" {
		return "", "", false
	}
	return net.JoinHostPort(st.Self.TailscaleIPs[0], "1"), u.LoginName, true
}
