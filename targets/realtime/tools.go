package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xavidop/tamesu/target"
)

// maxToolRounds bounds the function-call answers within one user turn, so an
// agent that keeps calling tools cannot hold a turn open forever.
const maxToolRounds = 5

// toolOutput renders the configured result of a function call as the string
// a function_call_output item carries: a string result as is, anything else
// as JSON. String leaves may use {{args.<key>}}. The done tool and unknown
// functions have no answer.
func (s *session) toolOutput(ctx context.Context, call target.ToolCall) (string, bool) {
	if call.Name == s.t.cfg.DoneTool {
		return "", false
	}
	result, ok := s.t.cfg.ToolResults[call.Name]
	var rendered any
	if ok {
		rendered = renderArgs(result, call.Args)
	}
	if f, fuzzed := s.run.ToolFuzz[call.Name]; fuzzed {
		// The test replaces this tool's answer; a tool with no configured
		// result is answered too, since the point is the agent's reaction.
		if err := f.Wait(ctx); err != nil {
			return "", false
		}
		rendered, ok = f.Output(rendered), true
		s.event(target.EventToolFuzz, f.Describe(call.Name))
	}
	if !ok {
		return "", false
	}
	if str, ok := rendered.(string); ok {
		return str, true
	}
	b, err := json.Marshal(rendered)
	if err != nil {
		return fmt.Sprint(rendered), true
	}
	return string(b), true
}

// renderArgs returns v with every {{args.<key>}} in its string leaves
// replaced by the call's argument; maps and slices are copied, not modified.
func renderArgs(v any, args map[string]any) any {
	switch x := v.(type) {
	case string:
		if !strings.Contains(x, "{{args.") {
			return x
		}
		out := x
		for k, a := range args {
			out = strings.ReplaceAll(out, "{{args."+k+"}}", argString(a))
		}
		return out
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = renderArgs(e, args)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = renderArgs(e, args)
		}
		return l
	}
	return v
}

func argString(a any) string {
	switch v := a.(type) {
	case string:
		return v
	case nil:
		return ""
	}
	if b, err := json.Marshal(a); err == nil {
		return string(b)
	}
	return fmt.Sprint(a)
}

// mergeTurn appends a follow-up response (after tool answers) to the turn:
// text with a space between, tool calls and raw events in order, the first
// token time kept, Done and Interrupted carried over.
func mergeTurn(agent *target.AgentTurn, next target.AgentTurn) {
	switch {
	case agent.Text == "":
		agent.Text = next.Text
	case next.Text != "":
		agent.Text = strings.TrimRight(agent.Text, " ") + " " + strings.TrimLeft(next.Text, " ")
	}
	agent.ToolCalls = append(agent.ToolCalls, next.ToolCalls...)
	if raw, ok := agent.Raw.([]any); ok {
		if more, ok := next.Raw.([]any); ok {
			agent.Raw = append(raw, more...)
		}
	}
	if agent.FirstTokenAt.IsZero() {
		agent.FirstTokenAt = next.FirstTokenAt
	}
	agent.Done = agent.Done || next.Done
	agent.Interrupted = agent.Interrupted || next.Interrupted
}
