package runner

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/ohler55/ojg/jp"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/target"
)

// capturer takes the test's capture values from the agent's turns as they
// arrive: the first turn that matches sets a value, later ones leave it.
type capturer struct {
	mu     sync.Mutex
	specs  []config.Capture
	res    []*regexp.Regexp
	paths  []jp.Expr
	values map[string]string
	// onSet publishes a value to the run scope as captured.<name>.
	onSet func(name, value string)
	// extract asks a model for a described value in the agent's text (a
	// capture with extract:); nil means no model is available.
	extractFn func(description, text string) (string, bool)
}

func newCapturer(specs []config.Capture, onSet func(name, value string), extract func(description, text string) (string, bool)) (*capturer, error) {
	c := &capturer{specs: specs, values: map[string]string{}, onSet: onSet, extractFn: extract}
	for _, sp := range specs {
		var re *regexp.Regexp
		var expr jp.Expr
		var err error
		if sp.Regex != "" {
			if re, err = regexp.Compile(sp.Regex); err != nil {
				return nil, fmt.Errorf("capture %s: %w", sp.Name, err)
			}
		}
		if sp.JSONPath != "" {
			if expr, err = jp.ParseString(sp.JSONPath); err != nil {
				return nil, fmt.Errorf("capture %s: %w", sp.Name, err)
			}
		}
		c.res = append(c.res, re)
		c.paths = append(c.paths, expr)
	}
	return c, nil
}

// turn looks at one agent turn.
func (c *capturer) turn(agent target.AgentTurn) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, sp := range c.specs {
		if _, done := c.values[sp.Name]; done {
			continue
		}
		if v, ok := c.extract(i, agent); ok {
			c.values[sp.Name] = v
			if c.onSet != nil {
				c.onSet(sp.Name, v)
			}
		}
	}
}

func (c *capturer) extract(i int, agent target.AgentTurn) (string, bool) {
	if re := c.res[i]; re != nil {
		if m := re.FindStringSubmatch(agent.Text); m != nil {
			if len(m) > 1 {
				return m[1], true
			}
			return m[0], true
		}
	}
	if expr := c.paths[i]; expr != nil && agent.Raw != nil {
		for _, raw := range rawList(agent.Raw) {
			if got := expr.Get(raw); len(got) > 0 {
				return scalarText(got[0]), true
			}
		}
	}
	if desc := c.specs[i].Extract; desc != "" && c.extractFn != nil && strings.TrimSpace(agent.Text) != "" {
		if v, ok := c.extractFn(desc, agent.Text); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// rawList is the turn's raw payload as a list: a websocket or realtime turn
// carries every message of the turn.
func rawList(raw any) []any {
	if l, ok := raw.([]any); ok {
		return l
	}
	return []any{raw}
}

func scalarText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
	}
	return fmt.Sprint(v)
}

// snapshot copies the values captured so far.
func (c *capturer) snapshot() map[string]string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.values) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}
