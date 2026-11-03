package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/xavidop/tamesu/target"
)

// maxTreeLines caps the observation so prompts stay bounded.
const maxTreeLines = 400

// refNode is a node the agent may act on.
type refNode struct {
	backend cdp.BackendNodeID
	role    string
	name    string
}

// Roles the observation keeps. Everything else is structure.
var keepRoles = map[string]bool{
	"button": true, "link": true, "textbox": true, "searchbox": true, "checkbox": true, "radio": true,
	"combobox": true, "listbox": true, "option": true, "menuitem": true, "menuitemcheckbox": true,
	"menuitemradio": true, "tab": true, "switch": true, "slider": true, "spinbutton": true,
	"heading": true, "img": true, "image": true, "alert": true, "status": true, "dialog": true,
	"cell": true, "gridcell": true, "columnheader": true, "rowheader": true, "StaticText": true,
	"ListMarker": false, "generic": false, "none": false, "paragraph": true, "listitem": true,
	"navigation": true, "main": true, "banner": true, "contentinfo": true, "form": true, "region": true,
	"progressbar": true, "tooltip": true, "treeitem": true, "LabelText": true,
}

// interactive roles get refs even when they have no name.
var interactiveRoles = map[string]bool{
	"button": true, "link": true, "textbox": true, "searchbox": true, "checkbox": true, "radio": true,
	"combobox": true, "listbox": true, "option": true, "menuitem": true, "menuitemcheckbox": true,
	"menuitemradio": true, "tab": true, "switch": true, "slider": true, "spinbutton": true, "treeitem": true,
}

// URL returns the current location.
func (s *session) URL(ctx context.Context) (string, error) {
	var loc string
	if err := s.do(ctx, into(chromedp.Location(), &loc)); err != nil {
		return "", err
	}
	return loc, nil
}

// Observe builds the observation from the full accessibility tree.
func (s *session) Observe(ctx context.Context, withScreenshot bool) (target.Observation, error) {
	var obs target.Observation
	var nodes []*accessibility.Node
	var title string
	err := s.do(ctx,
		into(chromedp.Location(), &obs.URL),
		into(chromedp.Title(), &title),
		func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
			res, err := cdp.Call(ctx, t, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{})
			nodes = res.Nodes
			return chromedp.Void{}, err
		},
	)
	if err != nil {
		return obs, fmt.Errorf("observe: %w", err)
	}
	obs.Title = title
	obs.Tree = s.tree(nodes)
	s.mu.Lock()
	if n := len(s.console); n > 0 {
		obs.Console = append([]string(nil), s.console[max(0, n-10):]...)
	}
	s.mu.Unlock()
	if withScreenshot {
		if png, err := s.Screenshot(ctx); err == nil {
			obs.Screenshot = png
		}
	}
	return obs, nil
}

// tree prunes the accessibility nodes to the lines the agent needs and keeps
// refs stable per backend node.
func (s *session) tree(nodes []*accessibility.Node) string {
	byID := map[accessibility.NodeID]*accessibility.Node{}
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	var lines []string
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[accessibility.NodeID]bool{}
	var walk func(id accessibility.NodeID, depth int)
	walk = func(id accessibility.NodeID, depth int) {
		n := byID[id]
		if n == nil || seen[id] || len(lines) >= maxTreeLines {
			return
		}
		seen[id] = true
		role := valueString(n.Role)
		name := strings.TrimSpace(valueString(n.Name))
		if !n.Ignored && (interactiveRoles[role] || (keepRoles[role] && name != "")) {
			line := s.line(n, role, name, depth)
			if line != "" {
				lines = append(lines, line)
			}
		}
		for _, c := range n.ChildIDs {
			walk(c, depth+1)
		}
	}
	if len(nodes) > 0 {
		walk(nodes[0].NodeID, 0)
	}
	return strings.Join(lines, "\n")
}

func (s *session) line(n *accessibility.Node, role, name string, depth int) string {
	var b strings.Builder
	if interactiveRoles[role] && n.BackendDOMNodeID != 0 {
		ref, ok := s.byNode[int64(n.BackendDOMNodeID)]
		if !ok {
			s.nextRef++
			ref = s.nextRef
			s.byNode[int64(n.BackendDOMNodeID)] = ref
		}
		s.refs[ref] = refNode{backend: n.BackendDOMNodeID, role: role, name: name}
		fmt.Fprintf(&b, "[%d] ", ref)
	} else {
		b.WriteString("    ")
	}
	b.WriteString(strings.Repeat(" ", min(depth, 12)))
	if role == "StaticText" || role == "paragraph" || role == "LabelText" {
		if len(name) > 200 {
			name = name[:197] + "..."
		}
		b.WriteString(`text "` + name + `"`)
		return b.String()
	}
	b.WriteString(role)
	if name != "" {
		fmt.Fprintf(&b, " %q", name)
	}
	if v := strings.TrimSpace(valueString(n.Value)); v != "" && v != name {
		fmt.Fprintf(&b, " value=%q", v)
	}
	var states []string
	for _, p := range n.Properties {
		switch p.Name {
		case accessibility.PropertyNameFocused, accessibility.PropertyNameDisabled, accessibility.PropertyNameRequired, accessibility.PropertyNameSelected, accessibility.PropertyNameExpanded, accessibility.PropertyNameChecked, accessibility.PropertyNamePressed, accessibility.PropertyNameInvalid:
			v := valueString(p.Value)
			if v == "true" {
				states = append(states, string(p.Name))
			} else if v != "false" && v != "" {
				states = append(states, string(p.Name)+"="+v)
			}
		case accessibility.PropertyNameLevel:
			states = append(states, "h"+valueString(p.Value))
		}
	}
	if len(states) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(states, ", "))
	}
	return b.String()
}

func valueString(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var x any
	if err := json.Unmarshal(v.Value, &x); err != nil {
		return string(v.Value)
	}
	switch t := x.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%v", t), ".0")
	}
	return string(v.Value)
}

func (s *session) node(ref int) (refNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.refs[ref]
	if !ok {
		var known []int
		for r := range s.refs {
			known = append(known, r)
		}
		sort.Ints(known)
		return refNode{}, fmt.Errorf("unknown ref %d; observe the page first (known refs: %v)", ref, known)
	}
	return n, nil
}

// Click scrolls the node into view and clicks its centre.
func (s *session) Click(ctx context.Context, ref int) error {
	n, err := s.node(ref)
	if err != nil {
		return err
	}
	err = s.do(ctx, func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		_, _ = cdp.Call(ctx, t, dom.ScrollIntoViewIfNeeded, dom.ScrollIntoViewIfNeededParams{BackendNodeID: n.backend})
		res, err := cdp.Call(ctx, t, dom.GetContentQuads, dom.GetContentQuadsParams{BackendNodeID: n.backend})
		if err != nil || len(res.Quads) == 0 || len(res.Quads[0]) < 8 {
			// Not laid out (hidden, or no box): click through the DOM.
			return chromedp.Void{}, s.callOn(ctx, t, n.backend, "function() { this.click(); }", nil)
		}
		q := res.Quads[0]
		x := (q[0] + q[2] + q[4] + q[6]) / 4
		y := (q[1] + q[3] + q[5] + q[7]) / 4
		for _, typ := range []input.DispatchMouseEventType{input.DispatchMouseEventTypeMouseMoved, input.DispatchMouseEventTypeMousePressed, input.DispatchMouseEventTypeMouseReleased} {
			p := input.DispatchMouseEventParams{Type: typ, X: x, Y: y}
			if typ != input.DispatchMouseEventTypeMouseMoved {
				p.Button, p.ClickCount = input.MouseButtonLeft, 1
			}
			if _, err := cdp.Call(ctx, t, input.DispatchMouseEvent, p); err != nil {
				return chromedp.Void{}, err
			}
		}
		return chromedp.Void{}, nil
	})
	if err != nil {
		return fmt.Errorf("click [%d] %s %q: %w", ref, n.role, n.name, err)
	}
	s.settle(ctx)
	s.event("click", fmt.Sprintf("[%d] %s %q", ref, n.role, n.name))
	return nil
}

// Type focuses the node, clears it and types text. Secrets are rendered
// here, so a {{secrets.X}} placeholder in text becomes the value.
func (s *session) Type(ctx context.Context, ref int, text string) error {
	n, err := s.node(ref)
	if err != nil {
		return err
	}
	if s.run != nil && s.run.Render != nil {
		if text, err = s.run.Render(text); err != nil {
			return fmt.Errorf("type [%d]: %w", ref, err)
		}
	}
	err = s.do(ctx, func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		if _, err := cdp.Call(ctx, t, dom.Focus, dom.FocusParams{BackendNodeID: n.backend}); err != nil {
			return chromedp.Void{}, err
		}
		if err := s.callOn(ctx, t, n.backend, `function() { if ('value' in this) { this.value = ''; this.dispatchEvent(new Event('input', {bubbles: true})); } else if (this.isContentEditable) { this.textContent = ''; } }`, nil); err != nil {
			return chromedp.Void{}, err
		}
		_, err := cdp.Call(ctx, t, input.InsertText, input.InsertTextParams{Text: text})
		return chromedp.Void{}, err
	})
	if err != nil {
		return fmt.Errorf("type into [%d] %s %q: %w", ref, n.role, n.name, err)
	}
	s.event("type", fmt.Sprintf("[%d] %s %q", ref, n.role, n.name))
	return nil
}

// Select picks an option of a select by value or label.
func (s *session) Select(ctx context.Context, ref int, value string) error {
	n, err := s.node(ref)
	if err != nil {
		return err
	}
	var picked bool
	err = s.do(ctx, func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		return chromedp.Void{}, s.callOn(ctx, t, n.backend, `function(v) {
			const el = this.tagName === 'SELECT' ? this : this.closest('select');
			if (!el) return false;
			for (const o of el.options) { if (o.value === v || o.text.trim() === v) { el.value = o.value;
				el.dispatchEvent(new Event('input', {bubbles: true})); el.dispatchEvent(new Event('change', {bubbles: true})); return true; } }
			return false; }`, &picked, value)
	})
	if err != nil {
		return fmt.Errorf("select %q in [%d]: %w", value, ref, err)
	}
	if !picked {
		return fmt.Errorf("select %q in [%d] %s %q: no such option", value, ref, n.role, n.name)
	}
	s.settle(ctx)
	s.event("select", fmt.Sprintf("[%d] %q", ref, value))
	return nil
}

// Scroll moves the viewport.
func (s *session) Scroll(ctx context.Context, direction string) error {
	js := map[string]string{
		"down": "window.scrollBy(0, Math.round(window.innerHeight * 0.8))", "up": "window.scrollBy(0, -Math.round(window.innerHeight * 0.8))",
		"top": "window.scrollTo(0, 0)", "bottom": "window.scrollTo(0, document.body.scrollHeight)",
	}[strings.ToLower(direction)]
	if js == "" {
		return fmt.Errorf("scroll: direction must be up, down, top or bottom, not %q", direction)
	}
	if err := s.do(ctx, eval(js)); err != nil {
		return fmt.Errorf("scroll %s: %w", direction, err)
	}
	s.event("scroll", direction)
	return nil
}

// Navigate loads a URL inside the allowed domains.
func (s *session) Navigate(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("navigate: %q is not an absolute URL", raw)
	}
	if !hostAllowed(u.Hostname(), s.allowed) {
		s.event("navigation_refused", raw)
		return fmt.Errorf("navigate: %s is outside allowed_domains %v; refusing", u.Hostname(), s.allowed)
	}
	if err := s.do(ctx, chromedp.Navigate(raw), chromedp.WaitReady(chromedp.CSS("body"))); err != nil {
		return fmt.Errorf("navigate to %s: %w", raw, err)
	}
	s.event("navigate", raw)
	return nil
}

var keys = map[string]string{
	"enter": kb.Enter, "return": kb.Enter, "tab": kb.Tab, "escape": kb.Escape, "esc": kb.Escape,
	"backspace": kb.Backspace, "delete": kb.Delete, "space": " ", "arrowdown": kb.ArrowDown, "arrowup": kb.ArrowUp,
	"arrowleft": kb.ArrowLeft, "arrowright": kb.ArrowRight, "home": kb.Home, "end": kb.End, "pageup": kb.PageUp, "pagedown": kb.PageDown,
}

// Press sends a key to the focused element.
func (s *session) Press(ctx context.Context, key string) error {
	k, ok := keys[strings.ToLower(strings.ReplaceAll(key, " ", ""))]
	if !ok {
		if len([]rune(key)) == 1 {
			k = key
		} else {
			return fmt.Errorf("press: unknown key %q (Enter, Tab, Escape, Backspace, Delete, Space, Arrow*, Home, End, PageUp, PageDown or one character)", key)
		}
	}
	if err := s.do(ctx, chromedp.KeyEvent(k)); err != nil {
		return fmt.Errorf("press %s: %w", key, err)
	}
	s.settle(ctx)
	s.event("press", key)
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

// Screenshot captures the viewport as PNG.
func (s *session) Screenshot(ctx context.Context) ([]byte, error) {
	var png []byte
	if err := s.do(ctx, into(chromedp.CaptureScreenshot(), &png)); err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}
	return png, nil
}

// Eval runs JavaScript.
func (s *session) Eval(ctx context.Context, js string) (any, error) {
	var out any
	if err := s.do(ctx, into(chromedp.Evaluate[any](js), &out)); err != nil {
		return nil, fmt.Errorf("eval: %w", err)
	}
	return out, nil
}

// Text is the page's visible text.
func (s *session) Text(ctx context.Context) (string, error) {
	var text string
	if err := s.do(ctx, into(chromedp.Evaluate[string]("document.body ? document.body.innerText : ''"), &text)); err != nil {
		return "", fmt.Errorf("text: %w", err)
	}
	return text, nil
}

// callOn calls a JS function with the node as `this`, decoding a by-value
// result into out when out is not nil.
func (s *session) callOn(ctx context.Context, t *chromedp.Target, id cdp.BackendNodeID, fn string, out any, args ...any) error {
	resolved, err := cdp.Call(ctx, t, dom.ResolveNode, dom.ResolveNodeParams{BackendNodeID: id})
	if err != nil {
		return err
	}
	obj := resolved.Object
	if obj == nil || obj.ObjectID == "" {
		return errors.New("node is gone")
	}
	var cargs []*runtime.CallArgument
	for _, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			return err
		}
		cargs = append(cargs, &runtime.CallArgument{Value: b})
	}
	called, err := cdp.Call(ctx, t, runtime.CallFunctionOn, runtime.CallFunctionOnParams{FunctionDeclaration: fn, ObjectID: obj.ObjectID, Arguments: cargs, ReturnByValue: ptr(true)})
	if err != nil {
		return err
	}
	if called.ExceptionDetails != nil {
		return fmt.Errorf("page script: %s", called.ExceptionDetails.Text)
	}
	if res := called.Result; out != nil && res != nil && len(res.Value) > 0 {
		return json.Unmarshal(res.Value, out)
	}
	return nil
}

// settle gives the page a moment after an action that may navigate.
func (s *session) settle(ctx context.Context) {
	_ = s.do(ctx, chromedp.Sleep(150*time.Millisecond), chromedp.WaitReady(chromedp.CSS("body")))
}
