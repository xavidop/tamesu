package desktop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xavidop/tamesu/target"
)

// ActionNames are the deterministic step actions of a desktop test.
var ActionNames = []string{"click", "fill", "press", "wait", "screenshot", "assert_text"}

// Do runs one deterministic action: a map with exactly one of ActionNames as
// key. An element is named by its ref, by its accessible name, or by
// {role, name}:
//
//	click: 12 | "Save" | {role: button, name: Save}
//	fill: {ref | name | role and name, text}
//	press: "Enter" | "cmd+s"
//	wait: ms | {text}
//	screenshot: name
//	assert_text: text | {contains | equals | matches}
func (s *session) Do(ctx context.Context, action map[string]any) (string, error) {
	name, arg, err := splitAction(action)
	if err != nil {
		return "", err
	}
	switch name {
	case "click":
		ref, what, err := s.locate(ctx, arg)
		if err != nil {
			return "", fmt.Errorf("click: %w", err)
		}
		if err := s.cli.call(ctx, nil, nil, "click", ref); err != nil {
			return "", fmt.Errorf("click %s: %w", what, err)
		}
		s.acted("click", what)
		return "clicked " + what, nil
	case "fill":
		m, ok := arg.(map[string]any)
		if !ok {
			return "", fmt.Errorf("fill wants {ref or name, text}")
		}
		text, _ := m["text"].(string)
		if s.run != nil && s.run.Render != nil {
			if text, err = s.run.Render(text); err != nil {
				return "", err
			}
		}
		where := map[string]any{}
		for _, k := range []string{"ref", "name", "role"} {
			if v, ok := m[k]; ok {
				where[k] = v
			}
		}
		var target any = where
		if r, ok := where["ref"]; ok {
			target = r
		}
		ref, what, err := s.locate(ctx, target)
		if err != nil {
			return "", fmt.Errorf("fill: %w", err)
		}
		if err := s.typeInto(ctx, ref, text); err != nil {
			return "", fmt.Errorf("fill %s: %w", what, err)
		}
		s.acted("fill", what)
		return "filled " + what, nil
	case "press":
		key, ok := arg.(string)
		if !ok {
			return "", fmt.Errorf("press wants a key such as Enter or cmd+s")
		}
		return "pressed " + key, s.Press(ctx, key)
	case "wait":
		if ms, ok := asInt(arg); ok {
			return fmt.Sprintf("waited %d ms", ms), s.Wait(ctx, time.Duration(ms)*time.Millisecond)
		}
		m, _ := arg.(map[string]any)
		if text, _ := m["text"].(string); text != "" {
			return "waited for text " + strconv.Quote(text), s.waitText(ctx, text)
		}
		return "", fmt.Errorf("wait wants milliseconds or {text}")
	case "screenshot":
		file, _ := arg.(string)
		if file == "" {
			file = "screenshot"
		}
		png, err := s.Screenshot(ctx)
		if err != nil {
			return "", err
		}
		path, err := s.saveScreenshot(file, png)
		if err != nil {
			return "", err
		}
		return "screenshot " + path, nil
	case "assert_text":
		text, err := s.Text(ctx)
		if err != nil {
			return "", err
		}
		want := arg
		desc := fmt.Sprintf("text contains %q", arg)
		if m, ok := arg.(map[string]any); ok {
			want = map[string]any{"contains": m["contains"], "matches": m["matches"], "equals": m["equals"]}
			desc = "text matches " + describeMatch(m)
		}
		return desc, matchAssert(text, want)
	}
	return "", fmt.Errorf("unknown action %q on a desktop target (one of %s)", name, strings.Join(ActionNames, ", "))
}

// locate resolves what a step names to agent-desktop's ref: a ref of the
// last observation, an accessible name, or {role, name}.
func (s *session) locate(ctx context.Context, arg any) (ref, what string, err error) {
	if n, ok := asInt(arg); ok {
		e, err := s.element(n)
		if err != nil {
			return "", "", err
		}
		return e.ref, fmt.Sprintf("[%d] %s %q", n, e.role, e.name), nil
	}
	var role, name string
	switch v := arg.(type) {
	case string:
		name = v
	case map[string]any:
		role, _ = v["role"].(string)
		name, _ = v["name"].(string)
	}
	if name == "" && role == "" {
		return "", "", fmt.Errorf("wants a ref, a name or {role, name}")
	}
	what = strings.TrimSpace(role + " " + strconv.Quote(name))
	if name == "" {
		what = role
	}
	// What is open on top of the window answers first, so it is searched
	// first; then the window; then the menu bar where it is outside the window.
	surfaces := append(s.open(ctx), "window")
	if s.menubar {
		surfaces = append(surfaces, menubar)
	}
	var roles []string
	var windowErr error
	for _, surface := range surfaces {
		args := []string{"find", "--app", s.app, "--exact", "--first"}
		if surface != "window" {
			args = append(args, "--surface", surface)
		}
		if role != "" {
			args = append(args, "--role", role)
		}
		if name != "" {
			args = append(args, "--name", name)
		}
		var found struct {
			Match *struct {
				RefID string `json:"ref_id"`
			} `json:"match"`
			RolesPresent []string `json:"roles_present"`
		}
		err := s.cli.call(ctx, nil, &found, args...)
		switch {
		case err == nil && found.Match != nil && found.Match.RefID != "":
			return found.Match.RefID, what, nil
		case surface == "window":
			roles, windowErr = found.RolesPresent, err
		}
	}
	if windowErr != nil {
		return "", "", fmt.Errorf("%s: %w", what, windowErr)
	}
	hint := ""
	if len(roles) > 0 {
		hint = " (roles in the window: " + strings.Join(roles, ", ") + ")"
	}
	return "", "", fmt.Errorf("no element is %s%s", what, hint)
}

// waitText polls the window's text until it holds want or the step's time is up.
func (s *session) waitText(ctx context.Context, want string) error {
	deadline := time.Now().Add(s.t.cfg.StepTimeout)
	for {
		text, err := s.Text(ctx)
		if err == nil && strings.Contains(text, want) {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("wait for text %q: %w", want, err)
			}
			return fmt.Errorf("wait for text %q: not there after %s", want, s.t.cfg.StepTimeout)
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// saveScreenshot writes a named screenshot into the run directory.
func (s *session) saveScreenshot(name string, png []byte) (string, error) {
	if s.run == nil || s.run.Dir == "" {
		return "", fmt.Errorf("screenshot: no run directory")
	}
	rel := filepath.Join("screenshots", slug(name)+".png")
	path := filepath.Join(s.run.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.files == nil {
		s.files = map[string]string{}
	}
	s.files[rel] = rel
	s.mu.Unlock()
	return rel, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	out := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if out == "" {
		return "screenshot"
	}
	return out
}

func splitAction(action map[string]any) (string, any, error) {
	if len(action) != 1 {
		keys := make([]string, 0, len(action))
		for k := range action {
			keys = append(keys, k)
		}
		sort.Strings(keys)
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
	case uint64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

// matchAssert checks text against a string (contains) or {contains, equals,
// matches}; a mismatch is a target.StepError, which fails the step without
// being an error of the run.
func matchAssert(got string, want any) error {
	fail := func(format string, a ...any) error {
		return &target.StepError{Reason: fmt.Sprintf(format, a...)}
	}
	switch w := want.(type) {
	case string:
		if !strings.Contains(got, w) {
			return fail("text does not contain %q (have %s)", w, short(got))
		}
		return nil
	case map[string]any:
		if v, _ := w["contains"].(string); v != "" && !strings.Contains(got, v) {
			return fail("text does not contain %q (have %s)", v, short(got))
		}
		if v, _ := w["equals"].(string); v != "" && strings.TrimSpace(got) != v {
			return fail("text is not %q (have %s)", v, short(got))
		}
		if v, _ := w["matches"].(string); v != "" {
			re, err := regexp.Compile(v)
			if err != nil {
				return fmt.Errorf("assert_text: invalid regular expression %q: %w", v, err)
			}
			if !re.MatchString(got) {
				return fail("text does not match %q (have %s)", v, short(got))
			}
		}
		return nil
	}
	return fmt.Errorf("assert_text wants a string or {contains, equals, matches}")
}

func describeMatch(m map[string]any) string {
	var parts []string
	for _, k := range []string{"contains", "equals", "matches"} {
		if v, _ := m[k].(string); v != "" {
			parts = append(parts, fmt.Sprintf("%s %q", k, v))
		}
	}
	return strings.Join(parts, ", ")
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		return strconv.Quote(s[:157] + "...")
	}
	return strconv.Quote(s)
}
