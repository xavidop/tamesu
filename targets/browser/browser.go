// Package browser is the browser target: Chromium over the DevTools protocol
// (chromedp). A session is one headless browser context on the target URL;
// the browser agent (the llm module) drives it through target.Page with
// numbered refs from the accessibility tree, deterministic steps run through
// Do, and every session leaves screenshots, a HAR and the console log.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/xavidop/tamesu/target"
)

// Defaults.
const (
	DefaultViewportWidth  = 1280
	DefaultViewportHeight = 800
	DefaultStepTimeout    = 30 * time.Second
	// Artifact names.
	HARFile     = "trace.har"
	ConsoleFile = "console.log"
)

// Config describes a browser target. URL is a template rendered with the
// run's scope.
type Config struct {
	URL string
	// Viewport is WIDTHxHEIGHT (default 1280x800); Locale an Accept-Language
	// tag such as en-US.
	Viewport string
	Locale   string
	// Headed shows the browser window (--headed).
	Headed bool
	// StorageState is a JSON file (Playwright's format: cookies and origins
	// with localStorage) loaded before the first navigation.
	StorageState string
	// AllowedDomains are the hosts the page may navigate to (suffix match,
	// `*.example.com` or `example.com`); empty means the URL's host.
	AllowedDomains []string
	// ExecPath is the Chromium binary; empty means TAMESU_CHROMIUM, then the
	// usual install locations, then a download of Chrome for Testing.
	ExecPath string
	// StepTimeout bounds one action (default 30 s).
	StepTimeout time.Duration
	// Download allows fetching Chromium when none is found (default true).
	NoDownload bool
}

// Target is the browser target.
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

// Start launches Chromium, opens the URL and returns the session (a
// target.BrowserSession that also satisfies target.Session).
func (t *Target) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	rawURL := t.cfg.URL
	if run.Render != nil {
		var err error
		if rawURL, err = run.Render(rawURL); err != nil {
			return nil, fmt.Errorf("browser %s: url: %w", t.name, err)
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("browser %s: url %q is not an absolute URL", t.name, rawURL)
	}
	exec, err := FindChromium(ctx, t.cfg.ExecPath, !t.cfg.NoDownload)
	if err != nil {
		return nil, fmt.Errorf("browser %s: %w", t.name, err)
	}
	w, h, err := parseViewport(t.cfg.Viewport)
	if err != nil {
		return nil, fmt.Errorf("browser %s: %w", t.name, err)
	}
	allowed := t.cfg.AllowedDomains
	if len(allowed) == 0 {
		allowed = []string{u.Hostname()}
	}
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(exec),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.DisableGPU,
		chromedp.WindowSize(w, h),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("mute-audio", true),
	}
	if !t.cfg.Headed {
		opts = append(opts, chromedp.Headless)
	}
	if t.cfg.Locale != "" {
		opts = append(opts, chromedp.Flag("lang", t.cfg.Locale))
	}
	if os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.WithoutCancel(ctx), opts...)
	logf := func(string, ...any) {}
	bctx, cancelBrowser := chromedp.NewContext(allocCtx, chromedp.WithLogf(logf), chromedp.WithErrorf(logf))
	s := &session{
		t: t, run: run, ctx: bctx, cancel: func() { cancelBrowser(); cancelAlloc() },
		allowed: allowed, refs: map[int]refNode{}, byNode: map[int64]int{}, startedAt: time.Now(),
		har: newHAR(),
	}
	s.listen()
	sctx, cancel := context.WithTimeout(ctx, t.cfg.StepTimeout)
	defer cancel()
	if err := s.runCtx(sctx, chromedp.Run(bctx, network.Enable(), runtime.Enable(), chromedp.EmulateViewport(int64(w), int64(h)))); err != nil {
		s.cancel()
		return nil, fmt.Errorf("browser %s: start chromium (%s): %w", t.name, exec, err)
	}
	if t.cfg.StorageState != "" {
		if err := s.loadStorageState(sctx, t.cfg.StorageState, u); err != nil {
			s.cancel()
			return nil, fmt.Errorf("browser %s: storage_state: %w", t.name, err)
		}
	}
	if err := s.Navigate(sctx, rawURL); err != nil {
		s.cancel()
		return nil, fmt.Errorf("browser %s: open %s: %w", t.name, rawURL, err)
	}
	s.event("opened", rawURL)
	return s, nil
}

// session is one browser context.
type session struct {
	t      *Target
	run    *target.RunContext
	ctx    context.Context
	cancel func()

	allowed []string

	mu        sync.Mutex
	refs      map[int]refNode // ref → node
	byNode    map[int64]int   // backend node id → ref
	nextRef   int
	console   []string
	events    []target.Event
	har       *har
	startedAt time.Time
	closed    bool
}

var _ target.BrowserSession = (*session)(nil)
var _ target.Session = (*session)(nil)

// Page returns the page.
func (s *session) Page() target.Page { return s }

// Send and Receive are not a browser session's business.
func (s *session) Send(context.Context, target.UserTurn) error {
	return errors.New("browser: a browser session has no conversation turns; use steps")
}

func (s *session) Receive(context.Context) (target.AgentTurn, error) {
	return target.AgentTurn{}, errors.New("browser: a browser session has no conversation turns; use steps")
}

// Close quits the browser and writes the HAR and console log into the run
// directory.
func (s *session) Close(context.Context) (target.Artifacts, error) {
	s.mu.Lock()
	already := s.closed
	s.closed = true
	s.mu.Unlock()
	if !already {
		s.cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	files := map[string]string{}
	if dir := s.run.Dir; dir != "" && !already {
		if b, err := json.MarshalIndent(s.har.document(), "", "  "); err == nil {
			if err := os.WriteFile(filepath.Join(dir, HARFile), b, 0o644); err == nil {
				files[HARFile] = HARFile
			}
		}
		if len(s.console) > 0 {
			if err := os.WriteFile(filepath.Join(dir, ConsoleFile), []byte(strings.Join(s.console, "\n")+"\n"), 0o644); err == nil {
				files[ConsoleFile] = ConsoleFile
			}
		}
	}
	m := map[string]float64{"requests": float64(len(s.har.entries)), "console_errors": float64(len(s.console))}
	return target.Artifacts{Files: files, Metrics: m, Events: append([]target.Event(nil), s.events...)}, nil
}

func (s *session) event(kind, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, target.Event{At: time.Now(), Kind: kind, Detail: detail})
}

// runCtx runs a chromedp action with ctx's deadline on the browser context.
func (s *session) runCtx(ctx context.Context, err error) error {
	if err != nil {
		return err
	}
	return ctx.Err()
}

// do runs chromedp actions bounded by ctx (or the step timeout).
func (s *session) do(ctx context.Context, actions ...chromedp.Action) error {
	tctx, cancel := context.WithTimeout(s.ctx, s.t.cfg.StepTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if err := chromedp.Run(tctx, actions...); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// listen captures console messages, exceptions and network traffic.
func (s *session) listen() {
	chromedp.ListenTarget(s.ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			if e.Type != runtime.APITypeError && e.Type != runtime.APITypeWarning && e.Type != runtime.APITypeAssert {
				return
			}
			var parts []string
			for _, a := range e.Args {
				parts = append(parts, remoteString(a))
			}
			s.mu.Lock()
			s.console = append(s.console, fmt.Sprintf("%s %s: %s", time.Now().Format(time.RFC3339), e.Type, strings.Join(parts, " ")))
			s.mu.Unlock()
		case *runtime.EventExceptionThrown:
			msg := e.ExceptionDetails.Text
			if e.ExceptionDetails.Exception != nil {
				msg += " " + remoteString(e.ExceptionDetails.Exception)
			}
			s.mu.Lock()
			s.console = append(s.console, fmt.Sprintf("%s exception: %s", time.Now().Format(time.RFC3339), strings.TrimSpace(msg)))
			s.mu.Unlock()
		case *network.EventRequestWillBeSent:
			s.har.request(e)
		case *network.EventResponseReceived:
			s.har.response(e)
		case *network.EventLoadingFinished:
			s.har.finished(e)
		case *network.EventLoadingFailed:
			s.har.failed(e)
		}
	})
}

func remoteString(o *runtime.RemoteObject) string {
	if o == nil {
		return ""
	}
	if o.Description != "" && o.Type == runtime.TypeObject {
		return o.Description
	}
	if len(o.Value) > 0 {
		var v any
		if json.Unmarshal(o.Value, &v) == nil {
			if str, ok := v.(string); ok {
				return str
			}
			return string(o.Value)
		}
	}
	return o.Description
}

func parseViewport(v string) (int, int, error) {
	if v == "" {
		return DefaultViewportWidth, DefaultViewportHeight, nil
	}
	var w, h int
	if _, err := fmt.Sscanf(strings.ToLower(v), "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("viewport %q must be WIDTHxHEIGHT", v)
	}
	return w, h, nil
}

// hostAllowed reports whether host is one of the allowed domains or a
// subdomain of one.
func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(a), "*."))
		if a == "" {
			continue
		}
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}
