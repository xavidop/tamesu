package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// recorderBinding is the CDP binding the injected script reports through.
const recorderBinding = "__tamesuRecord"

// recorderJS is injected into every document: it turns the person's clicks,
// typing, selections and Enter presses into deterministic actions with a
// durable selector (id, name, aria-label, else a short path). Passwords are
// reported as a {{secrets.PASSWORD}} placeholder, never their value.
const recorderJS = `(() => {
  if (window.__tamesuRecorderInstalled) return;
  window.__tamesuRecorderInstalled = true;
  const esc = s => (window.CSS && CSS.escape) ? CSS.escape(s) : s;
  const sel = el => {
    if (!el || el.nodeType !== 1) return '';
    if (el.id) return '#' + esc(el.id);
    const tag = el.tagName.toLowerCase();
    const name = el.getAttribute('name');
    if (name) return tag + '[name="' + name + '"]';
    const al = el.getAttribute('aria-label');
    if (al) return tag + '[aria-label="' + al + '"]';
    const parts = [];
    let e = el;
    while (e && e.nodeType === 1 && parts.length < 6) {
      if (e.id) { parts.unshift('#' + esc(e.id)); break; }
      let s = e.tagName.toLowerCase();
      const p = e.parentElement;
      if (p) {
        const same = Array.from(p.children).filter(c => c.tagName === e.tagName);
        if (same.length > 1) s += ':nth-of-type(' + (same.indexOf(e) + 1) + ')';
      }
      parts.unshift(s);
      e = p;
    }
    return parts.join(' > ');
  };
  const send = o => { try { window.` + recorderBinding + `(JSON.stringify(o)); } catch (e) {} };
  const valueOf = t => t.type === 'password' ? '{{secrets.PASSWORD}}' : t.value;
  const isTextField = t => t.matches && t.matches('input:not([type=button]):not([type=submit]):not([type=reset]):not([type=checkbox]):not([type=radio]):not([type=file]), textarea');
  document.addEventListener('click', ev => {
    const t = ev.target.closest ? (ev.target.closest('a,button,input,select,summary,[role=button],[role=link],[role=tab],[onclick]') || ev.target) : ev.target;
    if (isTextField(t) || (t.matches && t.matches('select'))) return;
    send({type: 'click', selector: sel(t), text: ((t.innerText || t.value || t.getAttribute('aria-label') || '') + '').trim().slice(0, 60)});
  }, true);
  document.addEventListener('change', ev => {
    const t = ev.target;
    if (!t.matches) return;
    if (t.matches('select')) send({type: 'select', selector: sel(t), value: t.value});
    else if (isTextField(t)) send({type: 'fill', selector: sel(t), value: valueOf(t)});
  }, true);
  document.addEventListener('keydown', ev => {
    if (ev.key !== 'Enter') return;
    const t = ev.target;
    if (isTextField(t)) send({type: 'fill', selector: sel(t), value: valueOf(t)});
    send({type: 'press', key: 'Enter'});
  }, true);
})();`

// recorded is one event from the page.
type recorded struct {
	Type     string `json:"type"`
	Selector string `json:"selector"`
	Value    string `json:"value"`
	Key      string `json:"key"`
	Text     string `json:"text"`
}

// RecordOptions configure Record.
type RecordOptions struct {
	// ExecPath is the Chromium binary (see FindChromium).
	ExecPath string
	// Headless records without a window (tests); a person needs a window.
	Headless bool
	// OnReady is called once the page is open, with the browser context
	// (tests drive the page through it).
	OnReady func(ctx context.Context)
	// Log receives one line per recorded step, for live feedback.
	Log func(step map[string]any)
	// Timeout caps the session (0: none).
	Timeout time.Duration
}

// Record opens url in a browser, records what the person does as
// deterministic step actions, and returns them when the browser closes or
// ctx ends. The first step is the navigation to url; a navigation the person
// makes by hand (not caused by a recorded click) is recorded too.
func Record(ctx context.Context, rawURL string, opts RecordOptions) ([]map[string]any, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("record: %q is not an absolute URL", rawURL)
	}
	exec, err := FindChromium(ctx, opts.ExecPath, true)
	if err != nil {
		return nil, err
	}
	allocOpts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(exec), chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("disable-extensions", true), chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-dev-shm-usage", true), chromedp.WindowSize(1280, 860),
	}
	if opts.Headless {
		allocOpts = append(allocOpts, chromedp.Headless, chromedp.DisableGPU)
	}
	if os.Geteuid() == 0 {
		allocOpts = append(allocOpts, chromedp.NoSandbox)
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()
	quiet := func(string, ...any) {}
	bctx, cancelBrowser := chromedp.NewContext(allocCtx, chromedp.WithLogf(quiet), chromedp.WithErrorf(quiet))
	defer cancelBrowser()

	rec := &recorder{}
	// Open the tab before subscribing, so the subscriptions watch the tab
	// the actions drive.
	if err := chromedp.Do(bctx, chromedp.Sleep(0)); err != nil {
		return nil, fmt.Errorf("record: start chromium (%s): %w", exec, err)
	}
	go watch(bctx, runtime.BindingCalled, func(e *runtime.EventBindingCalled) {
		if e.Name != recorderBinding {
			return
		}
		var r recorded
		if json.Unmarshal([]byte(e.Payload), &r) == nil {
			rec.add(r, opts.Log)
		}
	})
	go watch(bctx, page.FrameNavigated, func(e *page.EventFrameNavigated) {
		if e.Frame != nil && e.Frame.ParentID == "" {
			rec.navigated(e.Frame.URL, opts.Log)
		}
	})
	start := []chromedp.Action[chromedp.Void]{
		call(runtime.Enable, cdp.Empty{}),
		call(page.Enable, page.EnableParams{}),
		func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
			if _, err := cdp.Call(ctx, t, runtime.AddBinding, runtime.AddBindingParams{Name: recorderBinding}); err != nil {
				return chromedp.Void{}, err
			}
			_, err := cdp.Call(ctx, t, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: recorderJS})
			return chromedp.Void{}, err
		},
	}
	if err := chromedp.Do(bctx, start...); err != nil {
		return nil, fmt.Errorf("record: start chromium (%s): %w", exec, err)
	}
	rec.expectNavigation(rawURL)
	if err := chromedp.Do(bctx, chromedp.Navigate(rawURL), chromedp.WaitReady(chromedp.CSS("body"))); err != nil {
		return nil, fmt.Errorf("record: open %s: %w", rawURL, err)
	}
	// Documents that were loading before the binding existed get the script too.
	_ = chromedp.Do(bctx, eval(recorderJS))
	if opts.OnReady != nil {
		go opts.OnReady(bctx)
	}
	<-bctx.Done()
	steps := rec.list()
	if len(steps) == 0 {
		return nil, errors.New("record: nothing was recorded")
	}
	return steps, nil
}

// recorder assembles the steps: fills of the same field collapse into the
// last value, and a navigation right after a click or Enter is the page
// reacting, not the person typing a URL.
type recorder struct {
	mu       sync.Mutex
	steps    []map[string]any
	lastAct  time.Time
	expected string
}

func (r *recorder) expectNavigation(u string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expected = u
}

func (r *recorder) add(e recorded, log func(map[string]any)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var step map[string]any
	switch e.Type {
	case "click":
		step = map[string]any{"click": e.Selector}
	case "fill":
		// The same value again (Enter after a change) is noise; a new value
		// right after the field's own fill replaces it.
		for i := len(r.steps) - 1; i >= 0; i-- {
			f, ok := r.steps[i]["fill"].(map[string]any)
			if !ok || f["selector"] != e.Selector {
				continue
			}
			if f["text"] == e.Value {
				r.lastAct = time.Now()
				return
			}
			if i == len(r.steps)-1 {
				f["text"] = e.Value
				r.lastAct = time.Now()
				return
			}
			break
		}
		step = map[string]any{"fill": map[string]any{"selector": e.Selector, "text": e.Value}}
	case "select":
		step = map[string]any{"select": map[string]any{"selector": e.Selector, "value": e.Value}}
	case "press":
		step = map[string]any{"press": e.Key}
	default:
		return
	}
	r.lastAct = time.Now()
	r.steps = append(r.steps, step)
	if log != nil {
		log(step)
	}
}

func (r *recorder) navigated(u string, log func(map[string]any)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u == "" || u == "about:blank" {
		return
	}
	if r.expected != "" {
		// The navigation Record itself made.
		if strings.TrimSuffix(u, "/") == strings.TrimSuffix(r.expected, "/") || r.expected != "" {
			step := map[string]any{"navigate": r.expected}
			r.expected = ""
			r.steps = append(r.steps, step)
			if log != nil {
				log(step)
			}
			return
		}
	}
	if time.Since(r.lastAct) < 3*time.Second {
		return // the page reacting to a click or Enter
	}
	step := map[string]any{"navigate": u}
	r.steps = append(r.steps, step)
	if log != nil {
		log(step)
	}
}

func (r *recorder) list() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.steps...)
}

// StepsYAML renders recorded steps as the `steps:` block of a test: one
// `- action: {...}` line per step, flow style, ready to paste into a suite.
func StepsYAML(steps []map[string]any) string {
	var b strings.Builder
	b.WriteString("steps:\n")
	for _, st := range steps {
		for k, v := range st {
			b.WriteString("  - action: { " + k + ": " + flow(v) + " }\n")
		}
	}
	return b.String()
}

func flow(v any) string {
	switch x := v.(type) {
	case string:
		return quoteYAML(x)
	case map[string]any:
		keys := []string{"selector", "text", "value", "key"}
		var parts []string
		for _, k := range keys {
			if val, ok := x[k]; ok {
				parts = append(parts, k+": "+flow(val))
			}
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	}
	return fmt.Sprint(v)
}

func quoteYAML(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
