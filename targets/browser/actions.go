package browser

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/xavidop/tamesu/target"
)

// ActionNames are the deterministic step actions.
var ActionNames = []string{"navigate", "click", "fill", "press", "wait", "screenshot", "eval_js", "assert_url", "assert_text"}

// Do runs one deterministic action: a map with exactly one of ActionNames
// as key (navigate: url; click: selector or ref; fill: {selector|ref, text};
// press: key; wait: ms, or {selector} to wait for; screenshot: name;
// eval_js: expression; assert_url: {contains|equals|matches};
// assert_text: text, or {contains, selector}).
func (s *session) Do(ctx context.Context, action map[string]any) (string, error) {
	name, arg, err := splitAction(action)
	if err != nil {
		return "", err
	}
	switch name {
	case "navigate":
		u, ok := arg.(string)
		if !ok {
			return "", fmt.Errorf("navigate wants a URL string")
		}
		if s.run != nil && s.run.Render != nil {
			if u, err = s.run.Render(u); err != nil {
				return "", err
			}
		}
		return "navigated to " + u, s.Navigate(ctx, u)
	case "click":
		if ref, ok := asInt(arg); ok {
			return fmt.Sprintf("clicked [%d]", ref), s.Click(ctx, ref)
		}
		sel, ok := arg.(string)
		if !ok {
			return "", fmt.Errorf("click wants a CSS selector or a ref")
		}
		if err := s.do(ctx, chromedp.WaitVisible(sel, chromedp.ByQuery), chromedp.Click(sel, chromedp.ByQuery)); err != nil {
			return "", fmt.Errorf("click %q: %w", sel, err)
		}
		s.settle(ctx)
		s.event("click", sel)
		return "clicked " + sel, nil
	case "fill":
		m, ok := arg.(map[string]any)
		if !ok {
			return "", fmt.Errorf("fill wants {selector or ref, text}")
		}
		text, _ := m["text"].(string)
		if s.run != nil && s.run.Render != nil {
			if text, err = s.run.Render(text); err != nil {
				return "", err
			}
		}
		if ref, ok := asInt(m["ref"]); ok {
			return fmt.Sprintf("filled [%d]", ref), s.Type(ctx, ref, text)
		}
		sel, _ := m["selector"].(string)
		if sel == "" {
			return "", fmt.Errorf("fill wants selector or ref")
		}
		if err := s.do(ctx, chromedp.WaitVisible(sel, chromedp.ByQuery), chromedp.Clear(sel, chromedp.ByQuery), chromedp.SendKeys(sel, text, chromedp.ByQuery)); err != nil {
			return "", fmt.Errorf("fill %q: %w", sel, err)
		}
		s.event("fill", sel)
		return "filled " + sel, nil
	case "press":
		key, ok := arg.(string)
		if !ok {
			return "", fmt.Errorf("press wants a key name")
		}
		return "pressed " + key, s.Press(ctx, key)
	case "wait":
		if ms, ok := asInt(arg); ok {
			return fmt.Sprintf("waited %d ms", ms), s.Wait(ctx, time.Duration(ms)*time.Millisecond)
		}
		m, _ := arg.(map[string]any)
		if sel, _ := m["selector"].(string); sel != "" {
			if err := s.do(ctx, chromedp.WaitVisible(sel, chromedp.ByQuery)); err != nil {
				return "", fmt.Errorf("wait for %q: %w", sel, err)
			}
			return "waited for " + sel, nil
		}
		if text, _ := m["text"].(string); text != "" {
			return "waited for text " + strconv.Quote(text), s.waitText(ctx, text)
		}
		return "", fmt.Errorf("wait wants milliseconds, {selector} or {text}")
	case "screenshot":
		name, _ := arg.(string)
		if name == "" {
			name = "screenshot"
		}
		png, err := s.Screenshot(ctx)
		if err != nil {
			return "", err
		}
		path, err := s.saveScreenshot(name, png)
		if err != nil {
			return "", err
		}
		return "screenshot " + path, nil
	case "eval_js":
		js, ok := arg.(string)
		if !ok {
			return "", fmt.Errorf("eval_js wants a JavaScript expression")
		}
		v, err := s.Eval(ctx, js)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("eval_js → %v", v), nil
	case "assert_url":
		loc, err := s.URL(ctx)
		if err != nil {
			return "", err
		}
		return "url is " + loc, matchAssert("url", loc, arg)
	case "assert_text":
		var text string
		m, isMap := arg.(map[string]any)
		if sel, _ := m["selector"].(string); isMap && sel != "" {
			if err := s.do(ctx, chromedp.Text(sel, &text, chromedp.ByQuery)); err != nil {
				return "", fmt.Errorf("assert_text %q: %w", sel, err)
			}
		} else if text, err = s.Text(ctx); err != nil {
			return "", err
		}
		want := arg
		desc := fmt.Sprintf("text contains %q", arg)
		if isMap {
			want = map[string]any{"contains": m["contains"], "matches": m["matches"], "equals": m["equals"]}
			desc = "text matches " + describeMatch(m)
		}
		return desc, matchAssert("text", text, want)
	}
	return "", fmt.Errorf("unknown action %q (one of %s)", name, strings.Join(ActionNames, ", "))
}

func splitAction(action map[string]any) (string, any, error) {
	if len(action) != 1 {
		var keys []string
		for k := range action {
			keys = append(keys, k)
		}
		return "", nil, fmt.Errorf("an action is exactly one of %s (have %v)", strings.Join(ActionNames, ", "), keys)
	}
	for k, v := range action {
		return k, v, nil
	}
	return "", nil, nil
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), n == float64(int(n))
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	}
	return 0, false
}

// matchAssert checks got against a string (contains) or {contains, equals,
// matches}; a mismatch is a StepError.
func matchAssert(what, got string, want any) error {
	switch w := want.(type) {
	case string:
		if strings.Contains(got, w) {
			return nil
		}
		return &target.StepError{Reason: fmt.Sprintf("%s does not contain %q (got %s)", what, w, short(got))}
	case map[string]any:
		if c, ok := w["contains"].(string); ok && c != "" {
			if !strings.Contains(got, c) {
				return &target.StepError{Reason: fmt.Sprintf("%s does not contain %q (got %s)", what, c, short(got))}
			}
		}
		if e, ok := w["equals"].(string); ok && e != "" {
			if got != e {
				return &target.StepError{Reason: fmt.Sprintf("%s is %s, want %q", what, short(got), e)}
			}
		}
		if m, ok := w["matches"].(string); ok && m != "" {
			re, err := regexp.Compile(m)
			if err != nil {
				return fmt.Errorf("assert_%s matches: %w", what, err)
			}
			if !re.MatchString(got) {
				return &target.StepError{Reason: fmt.Sprintf("%s does not match %s (got %s)", what, m, short(got))}
			}
		}
		return nil
	}
	return fmt.Errorf("assert_%s wants a string or {contains, equals, matches}", what)
}

func describeMatch(m map[string]any) string {
	var parts []string
	for _, k := range []string{"contains", "equals", "matches"} {
		if v, ok := m[k].(string); ok && v != "" {
			parts = append(parts, fmt.Sprintf("%s %q", k, v))
		}
	}
	return strings.Join(parts, ", ")
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return strconv.Quote(s)
}

// waitText polls the page text until it contains want or ctx ends.
func (s *session) waitText(ctx context.Context, want string) error {
	deadline := time.Now().Add(s.t.cfg.StepTimeout)
	for {
		text, err := s.Text(ctx)
		if err != nil {
			return err
		}
		if strings.Contains(text, want) {
			return nil
		}
		if time.Now().After(deadline) {
			return &target.StepError{Reason: fmt.Sprintf("text %q did not appear within %s", want, s.t.cfg.StepTimeout)}
		}
		if err := s.Wait(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
}
