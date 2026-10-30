// Package herdr drives the Herdr terminal multiplexer through its documented
// command-line API (`herdr <group> <command> ... --session <name>`).
//
// Every call names its session explicitly; the ambient HERDR_SESSION,
// HERDR_SOCKET_PATH and pane context variables never choose the target.
// The client creates workspaces and tabs, reads them, runs a command in an
// owned tab, interrupts that command, closes only owned tabs and the owned
// Herdr workspaces they prove, and reports a pane's agent with its resume
// command. It never starts, stops or deletes Herdr servers or sessions.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MinVersion is the oldest Herdr release whose client and server this package
// has been exercised against.
const MinVersion = "0.9.5"

var (
	// ErrUnsupportedVersion reports a Herdr client or server older than MinVersion.
	ErrUnsupportedVersion = errors.New("herdr version is older than the supported minimum " + MinVersion)
	// ErrServerNotRunning reports that the named session has no running server.
	// The client never starts one.
	ErrServerNotRunning = errors.New("herdr session has no running server")
	// ErrMoved reports a recorded tab or pane id that now names a tab or pane
	// somewhere other than the record says, so the record grants nothing there.
	ErrMoved = errors.New("herdr recorded identity moved")
)

// sessionName is the accepted shape of an explicit session name.
var sessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// idShape is the accepted shape of a workspace, tab or pane id argument; ids
// are opaque but must never be mistaken for an option.
var idShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_-]*$`)

// ambientVars are inherited Herdr context variables removed from every call so
// only the explicit session selects the target server.
var ambientVars = []string{"HERDR_SESSION", "HERDR_SOCKET_PATH", "HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID", "HERDR_ENV"}

// Options configures a Client.
type Options struct {
	// Session is the explicit, non-default Herdr session every call targets.
	Session string
	// Command is the Herdr executable followed by any leading arguments, for
	// example a wrapper script run by an interpreter.
	Command []string
}

// Client runs Herdr API commands against one named session.
type Client struct {
	session string
	command []string
	// run executes argv with env and returns stdout, stderr and the exit code;
	// err is set only when the process could not be run at all.
	run           func(ctx context.Context, argv, env []string) (stdout, stderr []byte, exit int, err error)
	environ       func() []string
	moveWorkspace func(context.Context, string, string, int) ([]byte, error)
}

// New returns a client for the named session. The session must be given and
// must not be Herdr's default session.
func New(opts Options) (*Client, error) {
	if !sessionName.MatchString(opts.Session) {
		return nil, fmt.Errorf("herdr session %q: need a name of letters, digits, '-' or '_'", opts.Session)
	}
	if opts.Session == "default" {
		return nil, errors.New(`herdr session "default" is refused: name a dedicated session`)
	}
	if len(opts.Command) == 0 || opts.Command[0] == "" {
		return nil, errors.New("herdr command is empty")
	}
	return &Client{
		session: opts.Session,
		command: append([]string(nil), opts.Command...),
		run:     execRun,
		environ: os.Environ,
	}, nil
}

// Session returns the session this client targets.
func (c *Client) Session() string { return c.session }

// runWaitDelay is how long a run whose context ended waits for its output pipes, which a child of the stopped
// program may hold, before they are closed, so a call ends within this of its context.
const runWaitDelay = 5 * time.Second

func execRun(ctx context.Context, argv, env []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.WaitDelay = runWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	// A cancelled call is reported as the context's error, never as the exit
	// status the killed process happened to leave.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout.Bytes(), stderr.Bytes(), -1, fmt.Errorf("herdr stopped: %w", ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return nil, nil, -1, err
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

// APIError is an error reported by the Herdr server as
// {"error":{"code":...,"message":...}}.
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("herdr %s: %s", e.Code, e.Message) }

// IsNotFound reports whether err is a Herdr not-found error for a workspace,
// tab or pane.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && strings.HasSuffix(apiErr.Code, "_not_found")
}

// call runs one Herdr subcommand and returns its stdout.
func (c *Client) call(ctx context.Context, args ...string) ([]byte, error) {
	return c.callTail(ctx, args, nil)
}

// callTail is call with tail passed after `--`, following the session, so no
// word of it is read as a Herdr option.
func (c *Client) callTail(ctx context.Context, args, tail []string) ([]byte, error) {
	r := c.runTail(ctx, args, tail)
	if r.err != nil {
		return nil, r.err
	}
	return r.stdout, nil
}

// runResult is one Herdr run as runTail saw it: stdout, stderr and the exit
// code are kept on every path, and err is the error callTail returns.
type runResult struct {
	stdout, stderr []byte
	// exit is -1 when Herdr did not run or the context ended.
	exit int
	err  error
}

// runTail runs one Herdr subcommand as callTail does, keeping what callTail
// drops.
func (c *Client) runTail(ctx context.Context, args, tail []string) runResult {
	argv := make([]string, 0, len(c.command)+len(args)+len(tail)+3)
	argv = append(argv, c.command...)
	argv = append(argv, args...)
	argv = append(argv, "--session", c.session)
	if len(tail) > 0 {
		argv = append(append(argv, "--"), tail...)
	}
	stdout, stderr, exit, err := c.run(ctx, argv, c.env())
	r := runResult{stdout: stdout, stderr: stderr, exit: exit}
	if err != nil {
		r.exit = -1
		r.err = fmt.Errorf("herdr %s: %w", strings.Join(args[:min(2, len(args))], " "), err)
		return r
	}
	if exit != 0 {
		var body struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(bytes.TrimSpace(stderr), &body) == nil && body.Error != nil {
			r.err = &APIError{Code: body.Error.Code, Message: body.Error.Message}
			return r
		}
		r.err = fmt.Errorf("herdr %s: exit %d: %s", strings.Join(args[:min(2, len(args))], " "), exit, strings.TrimSpace(string(stderr)))
	}
	return r
}

func (c *Client) env() []string {
	var env []string
	for _, kv := range c.environ() {
		name, _, _ := strings.Cut(kv, "=")
		ambient := false
		for _, v := range ambientVars {
			if strings.EqualFold(name, v) {
				ambient = true
				break
			}
		}
		if !ambient {
			env = append(env, kv)
		}
	}
	return append(env, "HERDR_SESSION="+c.session)
}

// decode parses a success response {"id":...,"result":{...}} into result.
func decode(out []byte, result any) error {
	var body struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return fmt.Errorf("herdr response is not JSON: %w", err)
	}
	if len(body.Result) == 0 {
		return errors.New("herdr response has no result")
	}
	if err := json.Unmarshal(body.Result, result); err != nil {
		return fmt.Errorf("herdr result: %w", err)
	}
	return nil
}

// ValidSession reports whether name has the shape of an explicit session name New accepts; it does not refuse
// the default session.
func ValidSession(name string) bool { return sessionName.MatchString(name) }

// ValidID reports whether id has the shape of a workspace, tab or pane id the client accepts.
func ValidID(id string) bool { return idShape.MatchString(id) }

func checkID(kind, id string) error {
	if !idShape.MatchString(id) {
		return fmt.Errorf("herdr %s id %q is malformed", kind, id)
	}
	return nil
}

// Status is the client and server state of the session.
type Status struct {
	ClientVersion string
	ServerVersion string
	Running       bool
}

// Status reads `herdr status --json` and checks that the session's server is
// running, is the session asked for, and that both ends meet MinVersion.
func (c *Client) Status(ctx context.Context) (Status, error) {
	out, err := c.call(ctx, "status", "--json")
	if err != nil {
		return Status{}, err
	}
	var body struct {
		Client struct {
			Version string `json:"version"`
			Session string `json:"session"`
		} `json:"client"`
		Server struct {
			Running bool   `json:"running"`
			Version string `json:"version"`
			Session string `json:"session"`
		} `json:"server"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return Status{}, fmt.Errorf("herdr status is not JSON: %w", err)
	}
	st := Status{ClientVersion: body.Client.Version, ServerVersion: body.Server.Version, Running: body.Server.Running}
	if body.Client.Session != c.session {
		return st, fmt.Errorf("herdr status reports session %q, want %q", body.Client.Session, c.session)
	}
	if !st.Running {
		return st, fmt.Errorf("session %q: %w", c.session, ErrServerNotRunning)
	}
	if body.Server.Session != c.session {
		return st, fmt.Errorf("herdr server reports session %q, want %q", body.Server.Session, c.session)
	}
	for _, v := range []string{st.ClientVersion, st.ServerVersion} {
		cmp, err := compareVersions(v, MinVersion)
		if err != nil {
			return st, err
		}
		if cmp < 0 {
			return st, fmt.Errorf("herdr %s: %w", v, ErrUnsupportedVersion)
		}
	}
	return st, nil
}

// compareVersions compares two MAJOR.MINOR.PATCH[-PRERELEASE] versions; a
// prerelease sorts before its release.
func compareVersions(a, b string) (int, error) {
	pa, preA, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	pb, preB, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	switch {
	case preA == preB:
		return 0, nil
	case preA == "":
		return 1, nil
	case preB == "":
		return -1, nil
	}
	return strings.Compare(preA, preB), nil
}

func parseVersion(v string) ([3]int, string, error) {
	var parts [3]int
	core, pre, _ := strings.Cut(v, "-")
	fields := strings.Split(core, ".")
	if len(fields) != 3 {
		return parts, "", fmt.Errorf("herdr version %q is not MAJOR.MINOR.PATCH", v)
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return parts, "", fmt.Errorf("herdr version %q is not MAJOR.MINOR.PATCH", v)
		}
		parts[i] = n
	}
	return parts, pre, nil
}

// Workspace is a Herdr workspace.
type Workspace struct {
	ID    string `json:"workspace_id"`
	Label string `json:"label"`
	// InitialTab is ownership proof from WorkspaceCreate's response only.
	// WorkspaceList never grants ownership of a workspace's existing tabs.
	InitialTab OwnedTab `json:"-"`
}

// WorkspaceList lists the session's workspaces.
func (c *Client) WorkspaceList(ctx context.Context) ([]Workspace, error) {
	out, err := c.call(ctx, "workspace", "list")
	if err != nil {
		return nil, err
	}
	var r struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	if err := decode(out, &r); err != nil {
		return nil, err
	}
	return r.Workspaces, nil
}

// WorkspaceCreate creates a workspace without taking focus. Herdr gives every
// new workspace a first tab of its own.
func (c *Client) WorkspaceCreate(ctx context.Context, label, cwd string) (Workspace, error) {
	args := []string{"workspace", "create", "--label", label}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	out, err := c.call(ctx, append(args, "--no-focus")...)
	if err != nil {
		return Workspace{}, err
	}
	var r struct {
		Workspace Workspace `json:"workspace"`
		Tab       Tab       `json:"tab"`
		RootPane  struct {
			PaneID      string `json:"pane_id"`
			TabID       string `json:"tab_id"`
			WorkspaceID string `json:"workspace_id"`
		} `json:"root_pane"`
	}
	if err := decode(out, &r); err != nil {
		return Workspace{}, err
	}
	if err := checkID("workspace", r.Workspace.ID); err != nil {
		return Workspace{}, err
	}
	for kind, id := range map[string]string{"tab": r.Tab.ID, "pane": r.RootPane.PaneID} {
		if err := checkID(kind, id); err != nil {
			return Workspace{}, err
		}
	}
	if r.Tab.WorkspaceID != r.Workspace.ID || r.RootPane.WorkspaceID != r.Workspace.ID || r.RootPane.TabID != r.Tab.ID {
		return Workspace{}, errors.New("herdr workspace creation returned inconsistent starting tab identity")
	}
	r.Workspace.InitialTab = OwnedTab{session: c.session, workspaceID: r.Workspace.ID, tabID: r.Tab.ID, paneID: r.RootPane.PaneID}
	return r.Workspace, nil
}

// Tab is a Herdr tab as listed by the server.
type Tab struct {
	ID          string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	PaneCount   int    `json:"pane_count"`
}

// TabSpec describes a tab to create.
type TabSpec struct {
	WorkspaceID string
	Label       string
	Cwd         string
	// Env holds NAME=VALUE entries set in the tab's shell.
	Env []string
}

// OwnedTab proves ownership in the caller's session, obtained from TabCreate,
// WorkspaceCreate's starting tab, or RecordedTab's durable-record adoption.
type OwnedTab struct {
	session     string
	workspaceID string
	tabID       string
	paneID      string
}

// ID returns the tab id.
func (t OwnedTab) ID() string { return t.tabID }

// PaneID returns the id of the tab's root pane.
func (t OwnedTab) PaneID() string { return t.paneID }

// WorkspaceID returns the id of the workspace holding the tab.
func (t OwnedTab) WorkspaceID() string { return t.workspaceID }

// Session returns the session the tab was created in.
func (t OwnedTab) Session() string { return t.session }

func (c *Client) checkOwned(t OwnedTab) error {
	if t.tabID == "" || t.paneID == "" {
		return errors.New("herdr: not a tab created by this client")
	}
	if t.session != c.session {
		return fmt.Errorf("herdr: tab %s belongs to session %q, not %q", t.tabID, t.session, c.session)
	}
	return nil
}

// TabCreate creates a tab in an existing workspace without taking focus and
// returns it as an OwnedTab.
func (c *Client) TabCreate(ctx context.Context, spec TabSpec) (OwnedTab, error) {
	if err := checkID("workspace", spec.WorkspaceID); err != nil {
		return OwnedTab{}, err
	}
	args := []string{"tab", "create", "--workspace", spec.WorkspaceID, "--label", spec.Label}
	if spec.Cwd != "" {
		args = append(args, "--cwd", spec.Cwd)
	}
	for _, kv := range spec.Env {
		if name, _, ok := strings.Cut(kv, "="); !ok || name == "" {
			return OwnedTab{}, fmt.Errorf("herdr tab env %q is not NAME=VALUE", kv)
		}
		args = append(args, "--env", kv)
	}
	out, err := c.call(ctx, append(args, "--no-focus")...)
	if err != nil {
		return OwnedTab{}, err
	}
	var r struct {
		Tab      Tab `json:"tab"`
		RootPane struct {
			PaneID      string `json:"pane_id"`
			TabID       string `json:"tab_id"`
			WorkspaceID string `json:"workspace_id"`
		} `json:"root_pane"`
	}
	if err := decode(out, &r); err != nil {
		return OwnedTab{}, err
	}
	if err := checkID("tab", r.Tab.ID); err != nil {
		return OwnedTab{}, err
	}
	if err := checkID("pane", r.RootPane.PaneID); err != nil {
		return OwnedTab{}, err
	}
	if r.Tab.WorkspaceID != spec.WorkspaceID || r.RootPane.TabID != r.Tab.ID || r.RootPane.WorkspaceID != spec.WorkspaceID {
		return OwnedTab{}, fmt.Errorf("herdr created tab %s in workspace %s, want %s", r.Tab.ID, r.Tab.WorkspaceID, spec.WorkspaceID)
	}
	return OwnedTab{session: c.session, workspaceID: spec.WorkspaceID, tabID: r.Tab.ID, paneID: r.RootPane.PaneID}, nil
}

// RecordedTab returns the OwnedTab for a tab an earlier process of the
// caller's program created in this client's session and recorded durably,
// such as the Director's own pane (R1.54). The caller vouches for the record;
// RecordedTab checks only that the tab still exists in the recorded workspace,
// so a closed tab yields an error for which IsNotFound is true and a tab id
// now naming a tab in another workspace is refused. It sends nothing to the pane.
func (c *Client) RecordedTab(ctx context.Context, workspaceID, tabID, paneID string) (OwnedTab, error) {
	for kind, id := range map[string]string{"workspace": workspaceID, "tab": tabID, "pane": paneID} {
		if err := checkID(kind, id); err != nil {
			return OwnedTab{}, err
		}
	}
	tab, err := c.TabGet(ctx, tabID)
	if err != nil {
		return OwnedTab{}, err
	}
	if tab.WorkspaceID != workspaceID {
		return OwnedTab{}, fmt.Errorf("herdr tab %s is in workspace %s, not the recorded %s: %w", tabID, tab.WorkspaceID, workspaceID, ErrMoved)
	}
	return OwnedTab{session: c.session, workspaceID: workspaceID, tabID: tabID, paneID: paneID}, nil
}

// TabList lists the tabs of one workspace.
func (c *Client) TabList(ctx context.Context, workspaceID string) ([]Tab, error) {
	if err := checkID("workspace", workspaceID); err != nil {
		return nil, err
	}
	out, err := c.call(ctx, "tab", "list", "--workspace", workspaceID)
	if err != nil {
		return nil, err
	}
	var r struct {
		Tabs []Tab `json:"tabs"`
	}
	if err := decode(out, &r); err != nil {
		return nil, err
	}
	return r.Tabs, nil
}

// TabGet reads one tab. A closed tab yields an error for which IsNotFound is
// true; that says only that the tab is gone, never that its work finished.
func (c *Client) TabGet(ctx context.Context, tabID string) (Tab, error) {
	if err := checkID("tab", tabID); err != nil {
		return Tab{}, err
	}
	out, err := c.call(ctx, "tab", "get", tabID)
	if err != nil {
		return Tab{}, err
	}
	var r struct {
		Tab Tab `json:"tab"`
	}
	if err := decode(out, &r); err != nil {
		return Tab{}, err
	}
	return r.Tab, nil
}

// CheckOwnedPane verifies that an owned tab's recorded pane still belongs to
// that exact tab and workspace. Pane moves must not turn an old ownership
// binding into permission to close a tab containing a different pane.
func (c *Client) CheckOwnedPane(ctx context.Context, t OwnedTab) error {
	if err := c.checkOwned(t); err != nil {
		return err
	}
	out, err := c.call(ctx, "pane", "get", t.paneID)
	if err != nil {
		return err
	}
	var r struct {
		Pane struct {
			ID        string `json:"pane_id"`
			Tab       string `json:"tab_id"`
			Workspace string `json:"workspace_id"`
		} `json:"pane"`
	}
	if err := decode(out, &r); err != nil {
		return err
	}
	if r.Pane.ID != t.paneID || r.Pane.Tab != t.tabID || r.Pane.Workspace != t.workspaceID {
		return fmt.Errorf("herdr recorded pane no longer belongs to its tab and workspace: %w", ErrMoved)
	}
	return nil
}

// CloseOwnedTab closes a tab with ownership proof. Closing does not prove
// process termination; callers must establish separately when closure is safe.
func (c *Client) CloseOwnedTab(ctx context.Context, t OwnedTab) error {
	if err := c.checkOwned(t); err != nil {
		return err
	}
	_, err := c.call(ctx, "tab", "close", t.tabID)
	return err
}

// CloseOwnedWorkspace closes a workspace through the tab that proves it was
// created by this program: the owned tab must be in workspace ws. Closing does
// not prove process termination, and the client checks nothing about the
// workspace's other contents; callers must revalidate the whole workspace
// immediately before calling it.
func (c *Client) CloseOwnedWorkspace(ctx context.Context, t OwnedTab, ws string) error {
	if err := c.checkOwned(t); err != nil {
		return err
	}
	if err := checkID("workspace", ws); err != nil {
		return err
	}
	if t.workspaceID != ws {
		return fmt.Errorf("herdr: tab %s is in workspace %s, not %s", t.tabID, t.workspaceID, ws)
	}
	_, err := c.call(ctx, "workspace", "close", ws)
	return err
}

// Process is one foreground process reported for a pane.
type Process struct {
	PID  int      `json:"pid"`
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

// ProcessInfo is a pane's shell and foreground processes.
type ProcessInfo struct {
	ShellPID   int       `json:"shell_pid"`
	Foreground []Process `json:"foreground_processes"`
}

// ShellIdle reports whether the pane's shell is the only foreground process,
// so typed input reaches the shell prompt.
func (p ProcessInfo) ShellIdle() bool {
	return p.ShellPID > 0 && len(p.Foreground) == 1 && p.Foreground[0].PID == p.ShellPID
}

// PaneProcessInfo reads a pane's shell and foreground processes.
func (c *Client) PaneProcessInfo(ctx context.Context, paneID string) (ProcessInfo, error) {
	if err := checkID("pane", paneID); err != nil {
		return ProcessInfo{}, err
	}
	out, err := c.call(ctx, "pane", "process-info", "--pane", paneID)
	if err != nil {
		return ProcessInfo{}, err
	}
	var r struct {
		ProcessInfo ProcessInfo `json:"process_info"`
	}
	if err := decode(out, &r); err != nil {
		return ProcessInfo{}, err
	}
	return r.ProcessInfo, nil
}

// RunInOwnedTab types one command line and Enter into the root pane of a tab
// this client created. Success means Herdr accepted the input, not that the
// command ran.
func (c *Client) RunInOwnedTab(ctx context.Context, t OwnedTab, line string) error {
	if err := c.checkOwned(t); err != nil {
		return err
	}
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return errors.New("herdr pane run needs exactly one non-empty line")
	}
	_, err := c.call(ctx, "pane", "run", t.paneID, line)
	return err
}

// PaneTitle sets the title of the root pane of a tab this client created, as display-only metadata reported by
// source (`herdr pane report-metadata <pane> --source <source> --title <title>`), or clears it for "".
func (c *Client) PaneTitle(ctx context.Context, t OwnedTab, source, title string) error {
	if err := c.checkOwned(t); err != nil {
		return err
	}
	if !labelShape.MatchString(source) {
		return fmt.Errorf("herdr metadata source %q: need letters, digits, '.', '_' or '-'", source)
	}
	if strings.ContainsAny(title, "\r\n") {
		return errors.New("herdr pane title must be one line")
	}
	args := []string{"pane", "report-metadata", t.paneID, "--source", source, "--clear-title"}
	if title != "" {
		args = []string{"pane", "report-metadata", t.paneID, "--source", source, "--title", title}
	}
	_, err := c.call(ctx, args...)
	return err
}

// Notify shows a transient toast on this client's Herdr server (`herdr notification show <title> --body <body>`),
// outside every pane's input. title must be one non-empty line and body one line; an empty body is left out.
func (c *Client) Notify(ctx context.Context, title, body string) error {
	if title == "" || strings.ContainsAny(title, "\r\n") {
		return errors.New("herdr notification title must be one non-empty line")
	}
	if strings.ContainsAny(body, "\r\n") {
		return errors.New("herdr notification body must be one line")
	}
	args := []string{"notification", "show", title}
	if body != "" {
		// Joined to its flag, so a body starting with "-" is never read as an option.
		args = append(args, "--body="+body)
	}
	_, err := c.call(ctx, args...)
	return err
}

// InterruptOwnedTab sends Ctrl+C to the root pane of a tab this client created.
func (c *Client) InterruptOwnedTab(ctx context.Context, t OwnedTab) error {
	if err := c.checkOwned(t); err != nil {
		return err
	}
	_, err := c.call(ctx, "pane", "send-keys", t.paneID, "ctrl+c")
	return err
}

// ErrResumeArgv refuses a resume command Herdr would reject: an empty one is
// allowed (no resume), but a non-empty one must start with a plain command
// name, never a path or an option.
var ErrResumeArgv = errors.New("herdr resume_argv must start with a plain command name, not a path")

// AgentReport is what a pane reports about the agent running in it.
type AgentReport struct {
	// Source names the reporter, such as "guildhall"; Agent labels the agent,
	// such as "artisan". Both are letters, digits, '.', '_' or '-'.
	Source, Agent string
	// State is idle, working, blocked or unknown.
	State string
	// Resume is the command Herdr runs in the pane after a restart; it must
	// start with a plain command name (ErrResumeArgv).
	Resume []string
}

// ReportAgent reports the agent running in pane paneID with `herdr pane
// report-agent`, registering r.Resume as the pane's resume_argv. A pane
// reports itself, so the pane need not be one this client created. The
// report is checked before anything is sent: a malformed pane id, source,
// agent or state is refused, and a resume command Herdr would reject is
// ErrResumeArgv. The resume arguments follow `--`, after the explicit
// session, so none of them is read as a Herdr option.
func (c *Client) ReportAgent(ctx context.Context, paneID string, r AgentReport) error {
	_, err := c.ReportAgentOutcome(ctx, paneID, r)
	return err
}

// ReportOutcome is what Herdr answered to a report-agent call.
type ReportOutcome struct {
	// Exit is Herdr's exit code, or -1 when Herdr did not run, its context
	// ended, or the report was refused before anything was sent.
	Exit int
	// Stdout is Herdr's standard output, kept on success and failure.
	Stdout []byte
}

// ReportAgentOutcome is ReportAgent, also returning Herdr's exit code and
// stdout for diagnosis. Its checks, arguments and error are ReportAgent's.
func (c *Client) ReportAgentOutcome(ctx context.Context, paneID string, r AgentReport) (ReportOutcome, error) {
	refused := ReportOutcome{Exit: -1}
	if err := checkID("pane", paneID); err != nil {
		return refused, err
	}
	if !labelShape.MatchString(r.Source) || !labelShape.MatchString(r.Agent) {
		return refused, fmt.Errorf("herdr agent source %q and agent %q: need letters, digits, '.', '_' or '-'", r.Source, r.Agent)
	}
	if !agentStates[r.State] {
		return refused, fmt.Errorf("herdr agent state %q must be idle, working, blocked or unknown", r.State)
	}
	if len(r.Resume) > 0 {
		if cmd := r.Resume[0]; cmd == "" || strings.HasPrefix(cmd, "-") || strings.ContainsAny(cmd, `/\:`) {
			return refused, fmt.Errorf("%w: %q", ErrResumeArgv, cmd)
		}
	}
	res := c.runTail(ctx, []string{"pane", "report-agent", "--source", r.Source, "--agent", r.Agent, "--state", r.State, paneID}, r.Resume)
	return ReportOutcome{Exit: res.exit, Stdout: res.stdout}, res.err
}

// PaneReadBack is a pane as `herdr pane get` returns it, for diagnosis only.
type PaneReadBack struct {
	// AgentStatus is the pane's agent_status.
	AgentStatus string
	// Pane is the raw result.pane object: which of its fields, if any, names
	// a reported agent or resume command is not known, so none is assumed.
	Pane json.RawMessage
}

// PaneGet reads pane paneID with `herdr pane get`. It grants no ownership and
// is never evidence of liveness; a closed pane yields an error for which
// IsNotFound is true.
func (c *Client) PaneGet(ctx context.Context, paneID string) (PaneReadBack, error) {
	if err := checkID("pane", paneID); err != nil {
		return PaneReadBack{}, err
	}
	out, err := c.call(ctx, "pane", "get", paneID)
	if err != nil {
		return PaneReadBack{}, err
	}
	var r struct {
		Pane json.RawMessage `json:"pane"`
	}
	if err := decode(out, &r); err != nil {
		return PaneReadBack{}, err
	}
	var pane struct {
		AgentStatus string `json:"agent_status"`
	}
	if len(r.Pane) == 0 || bytes.Equal(r.Pane, []byte("null")) || json.Unmarshal(r.Pane, &pane) != nil {
		return PaneReadBack{}, errors.New("herdr pane get returned no pane object")
	}
	return PaneReadBack{AgentStatus: pane.AgentStatus, Pane: r.Pane}, nil
}

// labelShape is the accepted shape of an agent source or label.
var labelShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// agentStates are the pane agent states Herdr accepts.
var agentStates = map[string]bool{"idle": true, "working": true, "blocked": true, "unknown": true}
