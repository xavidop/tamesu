// Package desktop is the desktop target: it drives a desktop application
// through the operating system's accessibility tree with the agent-desktop
// CLI (macOS, Windows and Linux). A session launches the app and exposes it
// as a target.Page, so a test's steps run on it the way they run on a web
// page: goals for the agent, expectations and deterministic actions.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xavidop/tamesu/internal/pinned"
	"github.com/xavidop/tamesu/target"
)

// EngineAgentDesktop is the engine of the desktop target.
const EngineAgentDesktop = "agent-desktop"

// Engines lists the engines.
var Engines = []string{EngineAgentDesktop}

// DefaultStepTimeout bounds one action.
const DefaultStepTimeout = 30 * time.Second

// TraceFile is agent-desktop's trace of the session, in the run directory.
const TraceFile = "agent-desktop.jsonl"

// Config describes a desktop target.
type Config struct {
	// App is the application: its name or bundle id, as agent-desktop
	// resolves it. A template rendered with the run's scope.
	App string
	// Args are passed to the application when it is launched.
	Args []string
	// Engine is EngineAgentDesktop (the default and only engine).
	Engine string
	// ExecPath is the agent-desktop binary; empty means TAMESU_AGENT_DESKTOP,
	// then the pinned version (downloaded when missing unless NoDownload).
	ExecPath   string
	NoDownload bool
	// StepTimeout bounds one action (default 30 s).
	StepTimeout time.Duration

	// run replaces the CLI in tests.
	run func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

// Target is the desktop target.
type Target struct {
	name string
	cfg  Config
}

// New returns the target.
func New(name string, cfg Config) target.Target {
	if cfg.StepTimeout <= 0 {
		cfg.StepTimeout = DefaultStepTimeout
	}
	return &Target{name: name, cfg: cfg}
}

// Name returns the target's name in the suite.
func (t *Target) Name() string { return t.name }

// Permission is one of the system permissions agent-desktop works with
// (accessibility, screen_recording, automation): its state (granted, denied,
// not_required, unknown) and what to do about a denied one.
type Permission struct {
	State      string `json:"state"`
	Suggestion string `json:"suggestion"`
}

// status is what `agent-desktop status` says about the permissions it needs.
type status struct {
	Permissions map[string]Permission `json:"permissions"`
	// Platform is macos, windows or linux.
	Platform string `json:"platform"`
}

// Permissions asks the agent-desktop binary at path what it may do on this
// machine. The answer is about the application that runs it (the terminal,
// the editor, the CI agent): that is what the system grants permissions to.
func Permissions(ctx context.Context, path string) (map[string]Permission, error) {
	c := &cli{path: path, timeout: DefaultStepTimeout}
	c.run = c.exec
	var st status
	if err := c.call(ctx, nil, &st, "status"); err != nil {
		return nil, err
	}
	return st.Permissions, nil
}

// Start checks that agent-desktop may read the desktop, launches the app
// (or joins the one that is running) and returns the session (a
// target.BrowserSession that also satisfies target.Session).
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	if e := t.cfg.Engine; e != "" && e != EngineAgentDesktop {
		return nil, fmt.Errorf("desktop %s: engine %q is not one of %s", t.name, e, strings.Join(Engines, ", "))
	}
	if run == nil {
		run = &target.RunContext{}
	}
	app := t.cfg.App
	if run.Render != nil {
		var err error
		if app, err = run.Render(app); err != nil {
			return nil, fmt.Errorf("desktop %s: app: %w", t.name, err)
		}
	}
	if strings.TrimSpace(app) == "" {
		return nil, fmt.Errorf("desktop %s: no app to launch", t.name)
	}
	c := &cli{timeout: t.cfg.StepTimeout, run: t.cfg.run}
	if c.run == nil {
		tool, err := pinned.Lookup(pinned.AgentDesktop)
		if err != nil {
			return nil, fmt.Errorf("desktop %s: %w", t.name, err)
		}
		found, err := tool.Find(ctx, pinned.Options{Explicit: t.cfg.ExecPath, NoDownload: t.cfg.NoDownload})
		if err != nil {
			return nil, fmt.Errorf("desktop %s: %w", t.name, err)
		}
		c.path, c.run = found.Path, c.exec
	}
	if run.Dir != "" {
		c.trace = filepath.Join(run.Dir, TraceFile)
	}

	var st status
	if err := c.call(ctx, nil, &st, "status"); err != nil {
		return nil, fmt.Errorf("desktop %s: %w", t.name, err)
	}
	if p := st.Permissions["accessibility"]; p.State == "denied" {
		return nil, fmt.Errorf("desktop %s: agent-desktop may not read the desktop: the Accessibility permission is denied. %s", t.name, p.Suggestion)
	}

	// An app that was already running is the user's: the session leaves it
	// open when it ends.
	var running struct {
		Apps []struct {
			Name string `json:"name"`
		} `json:"apps"`
	}
	wasRunning := c.call(ctx, nil, &running, "list-apps", "--app", app) == nil && len(running.Apps) > 0

	args := []string{"launch", app, "--activate"}
	for _, a := range t.cfg.Args {
		if run.Render != nil {
			var err error
			if a, err = run.Render(a); err != nil {
				return nil, fmt.Errorf("desktop %s: args: %w", t.name, err)
			}
		}
		args = append(args, "--arg="+a)
	}
	var launched struct {
		App    string `json:"app"`
		Window *struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"window"`
	}
	if err := c.call(ctx, nil, &launched, args...); err != nil {
		return nil, fmt.Errorf("desktop %s: launch %s: %w", t.name, app, err)
	}
	s := &session{t: t, run: run, cli: c, app: app, launched: !wasRunning, menubar: st.Platform == "macos", byKey: map[string]int{}, refs: map[int]element{}}
	if launched.App != "" {
		s.app = launched.App
	}
	if launched.Window != nil {
		s.windowID = launched.Window.ID
	}
	s.event("opened", s.app)
	return s, nil
}

// session is one run on a desktop app.
type session struct {
	t   *Target
	run *target.RunContext
	cli *cli
	// app is the application as agent-desktop names it.
	app string
	// launched is false when the app was running before the session.
	launched bool
	// menubar is true where the app's menu bar is outside its window (macOS)
	// and is read as a surface of its own.
	menubar bool

	mu       sync.Mutex
	windowID string
	// byKey keeps an element's number across observations; refs are the
	// elements of the last observation.
	byKey   map[string]int
	refs    map[int]element
	nextRef int
	events  []target.Event
	files   map[string]string
	closed  bool
	// noScreenshots is why the system refuses screenshots, once it has.
	noScreenshots error
	actions       int
	observations  int
}

var _ target.BrowserSession = (*session)(nil)
var _ target.Session = (*session)(nil)

// Page returns the app's window as a page.
func (s *session) Page() target.Page { return s }

// Send and Receive are not a desktop session's business.
func (s *session) Send(context.Context, target.UserTurn) error {
	return errors.New("desktop: a desktop session has no conversation turns; use steps")
}

func (s *session) Receive(context.Context) (target.AgentTurn, error) {
	return target.AgentTurn{}, errors.New("desktop: a desktop session has no conversation turns; use steps")
}

// Close quits the app when the session launched it and returns the trace.
func (s *session) Close(ctx context.Context) (target.Artifacts, error) {
	s.mu.Lock()
	already := s.closed
	s.closed = true
	s.mu.Unlock()
	if !already && s.launched {
		s.quit(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	files := map[string]string{}
	for k, v := range s.files {
		files[k] = v
	}
	if s.cli.trace != "" {
		if st, err := os.Stat(s.cli.trace); err == nil && st.Size() > 0 {
			files[TraceFile] = TraceFile
		}
	}
	m := map[string]float64{"desktop_actions": float64(s.actions), "observations": float64(s.observations)}
	return target.Artifacts{Files: files, Metrics: m, Events: append([]target.Event(nil), s.events...)}, nil
}

// quit closes the app the session launched. An app that will not quit (it
// asks to save what the test typed) is terminated: nothing of the run is
// meant to outlive it, and an app left open would be the next run's.
func (s *session) quit(ctx context.Context) {
	err := s.cli.call(ctx, nil, nil, "close-app", s.app)
	if err == nil {
		s.event("closed", s.app)
		return
	}
	if ferr := s.cli.call(ctx, nil, nil, "close-app", s.app, "--force"); ferr != nil {
		s.event("close_failed", ferr.Error())
		return
	}
	s.event("closed", s.app+" (terminated: "+err.Error()+")")
}

func (s *session) event(kind, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, target.Event{At: time.Now(), Kind: kind, Detail: detail})
}

// location names the app the way a page has a URL.
func (s *session) location() string { return "desktop://" + s.app }

// URL names the app: a desktop app has no address.
func (s *session) URL(context.Context) (string, error) { return s.location(), nil }

// element returns what a ref of the last observation is.
func (s *session) element(ref int) (element, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.refs[ref]
	if !ok {
		known := make([]int, 0, len(s.refs))
		for r := range s.refs {
			known = append(known, r)
		}
		sort.Ints(known)
		return element{}, fmt.Errorf("unknown ref %d; observe the window first (known refs: %v)", ref, known)
	}
	return e, nil
}

// reveal scrolls an element that is outside what the window shows into
// view: agent-desktop acts on what a user could see. Whether it worked is the
// action's to find out.
func (s *session) reveal(ctx context.Context, e element) {
	if e.offscreen {
		_ = s.cli.call(ctx, nil, nil, "scroll-to", e.ref)
	}
}

// acted counts an action and records it.
func (s *session) acted(kind, detail string) {
	s.mu.Lock()
	s.actions++
	s.mu.Unlock()
	s.event(kind, detail)
}

// Click presses the element through the accessibility API: the pointer does
// not move.
func (s *session) Click(ctx context.Context, ref int) error {
	e, err := s.element(ref)
	if err != nil {
		return err
	}
	s.reveal(ctx, e)
	if err := s.cli.call(ctx, nil, nil, "click", e.ref); err != nil {
		return fmt.Errorf("click [%d] %s %q: %w", ref, e.role, e.name, err)
	}
	s.acted("click", fmt.Sprintf("[%d] %s %q", ref, e.role, e.name))
	return nil
}

// Type clears the field and types text. Secrets are rendered here, so a
// {{secrets.X}} placeholder in text becomes the value; the text reaches
// agent-desktop on standard input, never as an argument.
func (s *session) Type(ctx context.Context, ref int, text string) error {
	e, err := s.element(ref)
	if err != nil {
		return err
	}
	if s.run != nil && s.run.Render != nil {
		if text, err = s.run.Render(text); err != nil {
			return fmt.Errorf("type [%d]: %w", ref, err)
		}
	}
	s.reveal(ctx, e)
	if err := s.typeInto(ctx, e.ref, text); err != nil {
		return fmt.Errorf("type into [%d] %s %q: %w", ref, e.role, e.name, err)
	}
	s.acted("type", fmt.Sprintf("[%d] %s %q", ref, e.role, e.name))
	return nil
}

// typeInto empties the field and inserts text. A field that cannot be
// cleared (it is empty, or the toolkit has no such action) is typed into as
// it is.
func (s *session) typeInto(ctx context.Context, ref, text string) error {
	if err := s.cli.call(ctx, nil, nil, "clear", ref); err != nil && hasCode(err, "STALE_REF", "AMBIGUOUS_TARGET", "SNAPSHOT_NOT_FOUND", "PERM_DENIED") {
		return err
	}
	if text == "" {
		return nil
	}
	return s.cli.call(ctx, []byte(text), nil, "type", ref, "--stdin")
}

// Select picks an option of a list or a dropdown.
func (s *session) Select(ctx context.Context, ref int, value string) error {
	e, err := s.element(ref)
	if err != nil {
		return err
	}
	s.reveal(ctx, e)
	if err := s.cli.call(ctx, nil, nil, "select", e.ref, value); err != nil {
		return fmt.Errorf("select %q in [%d] %s %q: %w", value, ref, e.role, e.name, err)
	}
	s.acted("select", fmt.Sprintf("[%d] %q", ref, value))
	return nil
}

// keys maps the key names of a browser step onto agent-desktop's.
var keys = map[string]string{
	"enter": "return", "esc": "escape", "arrowdown": "down", "arrowup": "up", "arrowleft": "left", "arrowright": "right",
	"control": "ctrl", "command": "cmd", "meta": "cmd", "option": "alt",
}

// combo turns "Enter" or "Cmd+S" into agent-desktop's "return" or "cmd+s".
func combo(key string) string {
	parts := strings.Split(strings.ToLower(strings.ReplaceAll(key, " ", "")), "+")
	for i, p := range parts {
		if k, ok := keys[p]; ok {
			parts[i] = k
		}
	}
	return strings.Join(parts, "+")
}

// Press sends a key or a combination (Enter, Escape, cmd+s) to the app.
func (s *session) Press(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("press: no key")
	}
	args := []string{"press", combo(key)}
	s.mu.Lock()
	if s.windowID != "" {
		args = append(args, "--window-id", s.windowID)
	}
	s.mu.Unlock()
	if err := s.cli.call(ctx, nil, nil, args...); err != nil {
		return fmt.Errorf("press %s: %w", key, err)
	}
	s.acted("press", key)
	return nil
}

// Wait pauses.
func (s *session) Wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ErrUnsupported is returned by what a desktop app has no equivalent of.
var ErrUnsupported = errors.New("not available on a desktop target")

// Scroll is not needed on a desktop target: the observation holds the
// elements that are off screen, and one is scrolled into view when it is
// acted on.
func (s *session) Scroll(context.Context, string) error {
	return fmt.Errorf("scroll: %w (elements that are off screen are in the observation already and are scrolled to when you act on their refs)", ErrUnsupported)
}

// Navigate has no meaning for a desktop app.
func (s *session) Navigate(context.Context, string) error {
	return fmt.Errorf("navigate: %w", ErrUnsupported)
}

// Eval has no meaning for a desktop app.
func (s *session) Eval(context.Context, string) (any, error) {
	return nil, fmt.Errorf("eval: %w", ErrUnsupported)
}
