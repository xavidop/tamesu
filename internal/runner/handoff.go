package runner

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/ohler55/ojg/jp"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/target"
)

// handoffDetector turns a target's handoff signal into the loop hook: it
// inspects every agent turn and, when the signal fires, returns the handoff
// event's detail (a HandoffDetail as JSON). nil without a signal.
func handoffDetector(sig *config.HandoffSignal) func(target.AgentTurn, int) string {
	if sig == nil {
		return nil
	}
	var re *regexp.Regexp
	if sig.Regex != "" {
		re, _ = regexp.Compile(sig.Regex) // the validator rejected a bad pattern
	}
	var path jp.Expr
	if sig.JSONPath != "" {
		path, _ = jp.ParseString(sig.JSONPath)
	}
	lookup := func(raw any, args map[string]any, where string) string {
		if where == "" {
			return ""
		}
		if args != nil {
			if v, ok := args[where]; ok {
				return stringOf(v)
			}
		}
		if raw != nil && strings.HasPrefix(where, "$") {
			if expr, err := jp.ParseString(where); err == nil {
				if got := expr.Get(raw); len(got) > 0 {
					return stringOf(got[0])
				}
			}
		}
		return ""
	}
	return func(agent target.AgentTurn, turn int) string {
		var d *assert.HandoffDetail
		switch {
		case sig.Tool != "":
			for _, c := range agent.ToolCalls {
				if c.Name == sig.Tool {
					d = &assert.HandoffDetail{Via: "tool " + c.Name, To: lookup(agent.Raw, c.Args, sig.To), Summary: lookup(agent.Raw, c.Args, sig.Summary)}
					break
				}
			}
		}
		if d == nil && path != nil && agent.Raw != nil {
			if got := path.Get(agent.Raw); len(got) > 0 && truthy(got[0]) {
				d = &assert.HandoffDetail{Via: "json_path " + sig.JSONPath, To: lookup(agent.Raw, nil, sig.To), Summary: lookup(agent.Raw, nil, sig.Summary)}
				if d.To == "" {
					if s, ok := got[0].(string); ok {
						d.To = s
					}
				}
			}
		}
		if d == nil && re != nil {
			if m := re.FindStringSubmatch(agent.Text); m != nil {
				d = &assert.HandoffDetail{Via: "regex", To: lookup(agent.Raw, nil, sig.To), Summary: lookup(agent.Raw, nil, sig.Summary)}
				if d.To == "" && len(m) > 1 {
					d.To = m[1]
				}
			}
		}
		if d == nil {
			return ""
		}
		d.Turn = turn
		b, err := json.Marshal(d)
		if err != nil {
			return fmt.Sprintf(`{"turn":%d}`, turn)
		}
		return string(b)
	}
}

// truthy is how a JSONPath value means "handed off": present and not false,
// null, zero or empty.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != "" && x != "false" && x != "0"
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	}
	return true
}

func stringOf(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
