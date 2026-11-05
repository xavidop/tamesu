package assert

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/xavidop/tamesu/target"
)

// DelegatedTo passes when the agent sent a message to the remote agent
// (one of the suite's remote_agents) and, when given, the message contains
// the text or matches the expression.
func DelegatedTo(agent, contains string, re *regexp.Regexp) Assertion {
	return Func("delegated_to "+agent, func(_ context.Context, r *RunResult) Result {
		var seen []string
		var toAgent []string
		for _, c := range r.ToolCalls {
			if c.Kind != target.ToolCallDelegation {
				continue
			}
			seen = append(seen, c.Name)
			if c.Name != agent {
				continue
			}
			msg, _ := c.Args["message"].(string)
			toAgent = append(toAgent, msg)
			if contains != "" && !strings.Contains(strings.ToLower(msg), strings.ToLower(contains)) {
				continue
			}
			if re != nil && !re.MatchString(msg) {
				continue
			}
			return Result{Pass: true, Reason: fmt.Sprintf("the agent delegated to %s: %q", agent, msg), Evidence: []string{msg}}
		}
		switch {
		case len(toAgent) == 0:
			return Result{Pass: false, Reason: fmt.Sprintf("the agent did not delegate to %s (delegations: %s)", agent, listOr(seen))}
		case contains != "":
			return Result{Pass: false, Reason: fmt.Sprintf("the agent delegated to %s but no message contained %q", agent, contains), Evidence: toAgent}
		default:
			return Result{Pass: false, Reason: fmt.Sprintf("the agent delegated to %s but no message matched %s", agent, re), Evidence: toAgent}
		}
	})
}

// Delegations lists the messages the agent sent to remote agents.
func Delegations(r *RunResult) []target.ToolCall {
	var out []target.ToolCall
	for _, c := range r.ToolCalls {
		if c.Kind == target.ToolCallDelegation {
			out = append(out, c)
		}
	}
	return out
}
