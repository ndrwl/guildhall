package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

const webUsage = `usage:
  guildhall web [--copy <dir>]
Serves the copy's task pages at the [web] address of its config, on the tailnet only, until interrupted (docs/web.md).
A Director session's supervisor serves them while the session runs; run this when no session does.`

// webRecordFile is the serving record, under the copy's state directory, a Director session's supervisor writes
// each time it binds, so director status can say whether the session serves the pages.
const webRecordFile = "web.json"

// webRecord is the serving record: which session tried to bind, the page address, and whether it serves or why it
// could not bind.
type webRecord struct {
	Session int    `json:"session"`
	URL     string `json:"url"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
}

// Web serving states, in the record and in director status's web object.
const (
	webNotConfigured = "not configured"
	webServing       = "serving"
	webFailed        = "failed to bind"
	webNotServing    = "not serving"
)

// webStatus is director status's web object: the page address and whether the running session serves it.
type webStatus struct {
	State  string `json:"state"`
	URL    string `json:"url,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// webListen binds a web address; nil means web.Listen, which binds only on the tailnet.
func (a *App) webListen(address string) (net.Listener, error) {
	if a.WebListen != nil {
		return a.WebListen(address)
	}
	return web.Listen(address)
}

// webServer is the task pages being served for a copy, or nothing when its config has no [web] table or the
// address could not be bound.
type webServer struct {
	once    sync.Once
	running *web.Running
	record  string
}

// close stops serving and removes the serving record; a nil server closes nothing, and closing twice closes once.
func (s *webServer) close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.running.Close()
		if s.record != "" {
			_ = os.Remove(s.record)
		}
	})
}

// webHandler is the copy's task-page server over a webSource, answering only requests for the [web] address: it
// answers for the [web] table's guildmaster login, told by tailscale whois, through a webAnswerer, and is read-only
// when the table names no login.
func (a *App) webHandler(c clerkPaths, w *config.Web, log io.Writer) *web.Server {
	var id web.Identifier = web.Whois{}
	if a.WebIdentity != nil {
		id = a.WebIdentity
	}
	o := web.Options{Source: webSource{a: a, c: c}, Documents: filepath.Join(c.state, "documents"),
		Log: func(line string) { _, _ = fmt.Fprintln(log, line) }, Identity: id, Answerer: webAnswerer{a: a, c: c}}
	if w != nil {
		o.Guildmaster, o.Host = w.Guildmaster, w.Address
	}
	return web.New(o)
}

// startWeb serves the copy's task pages for Director session n when cfg has a [web] table, and records whether it
// binds in the copy's serving record. A bind failure is a warning on stdout and never fails the session; it returns
// nil when nothing is served.
func (a *App) startWeb(cfg *config.Config, n int, copyDir, storePath, state string) *webServer {
	if cfg == nil || cfg.Web == nil {
		return nil
	}
	record := filepath.Join(state, webRecordFile)
	rec := webRecord{Session: n, URL: webBaseURL(cfg.Web.Address)}
	ln, err := a.webListen(cfg.Web.Address)
	if err != nil {
		rec.State, rec.Reason = webFailed, err.Error()
		_, _ = fmt.Fprintf(a.Stdout, "web: warning: not serving the task pages: %v\n", err)
		a.writeWebRecord(record, rec)
		return nil
	}
	c := clerkPaths{dir: copyDir, store: storePath, config: filepath.Join(copyDir, "config.toml"), state: state}
	s := &webServer{running: web.Serve(ln, a.webHandler(c, cfg.Web, a.Stderr)), record: record}
	rec.State = webServing
	a.writeWebRecord(record, rec)
	_, _ = fmt.Fprintf(a.Stdout, "web: serving the task pages at %s/<repo>/<task>\n", rec.URL)
	return s
}

func (a *App) writeWebRecord(path string, rec webRecord) {
	data, err := json.Marshal(rec)
	if err == nil {
		err = atomicfile.Write(path, append(data, '\n'))
	}
	if err != nil {
		_, _ = fmt.Fprintf(a.Stdout, "web: warning: cannot record the web server's state: %v\n", err)
	}
}

// webBaseURL is the pages' base URL for address.
func webBaseURL(address string) string { return "http://" + address }

// readWebStatus is director status's web object for the config at cfgPath and the copy's state directory: not
// configured with no [web] table; else the address, and the serving record's state when it was written by session,
// the running session, or not serving.
func readWebStatus(cfgPath, state string, session statusSession) *webStatus {
	if cfgPath == "" {
		return nil
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil
	}
	if cfg.Web == nil {
		return &webStatus{State: webNotConfigured}
	}
	out := &webStatus{State: webNotServing, URL: webBaseURL(cfg.Web.Address)}
	data, err := atomicfile.Read(filepath.Join(state, webRecordFile))
	if err != nil {
		return out
	}
	var rec webRecord
	live := session.State == string(store.DirectorRunning) || session.State == string(store.DirectorRecovering)
	if json.Unmarshal(data, &rec) != nil || !live || rec.Session != session.Number {
		return out
	}
	out.State, out.Reason = rec.State, rec.Reason
	return out
}

// web runs `guildhall web`: it serves the copy's task pages at its [web] address until interrupted.
func (a *App) web(args []string) int {
	fl := flag.NewFlagSet("web", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	dir := fl.String("copy", "", "Director copy directory")
	if err := fl.Parse(args); errors.Is(err, flag.ErrHelp) {
		if _, err := fmt.Fprintln(a.Stdout, webUsage); err != nil {
			return 1
		}
		return 0
	} else if err != nil {
		return a.webUsageError("web: " + err.Error())
	}
	if fl.NArg() > 0 {
		return a.webUsageError(fmt.Sprintf("web: unexpected argument %q", fl.Arg(0)))
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: web: %v\n", err)
		return 1
	}
	c, err := a.clerkCopy(*dir)
	if err != nil {
		return fail(err)
	}
	cfg, err := config.Load(c.config)
	if errors.Is(err, fs.ErrNotExist) {
		return fail(fmt.Errorf("the copy has no config at %s", c.config))
	}
	if err != nil {
		return fail(err)
	}
	if cfg.Web == nil {
		return fail(errors.New("the copy's config has no [web] table; add one with its address (docs/web.md)"))
	}
	ln, err := a.webListen(cfg.Web.Address)
	if err != nil {
		return fail(err)
	}
	signals := a.webSignals
	if signals == nil {
		signals = make(chan os.Signal, 1)
		signal.Notify(signals, sessionSignals...)
		defer signal.Stop(signals)
	}
	running := web.Serve(ln, a.webHandler(c, cfg.Web, a.Stderr))
	_, _ = fmt.Fprintf(a.Stdout, "web: serving the task pages at %s/<repo>/<task> until interrupted\n", webBaseURL(cfg.Web.Address))
	if a.webServing != nil {
		a.webServing(ln.Addr().String())
	}
	<-signals
	running.Close()
	_, _ = fmt.Fprintln(a.Stdout, "web: stopped")
	return 0
}

func (a *App) webUsageError(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, webUsage)
	return 2
}
