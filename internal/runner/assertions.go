package runner

import (
	"fmt"
	"time"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
)

// Build turns one YAML assertion into an Assertion. The validator has already
// checked the shape; an error here is a programming error or an unregistered
// custom assertion.
func Build(a config.Assertion) (assert.Assertion, error) {
	inner, err := buildKind(a)
	if err != nil {
		return nil, err
	}
	if a.OnTurn.IsSet() {
		return assert.OnTurn(a.OnTurn.N, a.OnTurn.Last, inner), nil
	}
	return inner, nil
}

func buildKind(a config.Assertion) (assert.Assertion, error) {
	switch kind := a.Kind(); kind {
	case "judge":
		var opts []assert.JudgeOption
		if a.Judge.Scale != "" {
			opts = append(opts, assert.WithScale(a.Judge.Scale))
		}
		if a.Judge.Min != 0 {
			opts = append(opts, assert.WithMin(a.Judge.Min))
		}
		if a.Judge.Model != "" {
			opts = append(opts, assert.WithModel(a.Judge.Model))
		}
		return assert.Judge(a.Judge.Rubric, opts...), nil
	case "contains":
		return assert.Contains(*a.Contains), nil
	case "not_contains":
		return assert.NotContains(*a.NotContains), nil
	case "regex":
		return assert.Regex(*a.Regex)
	case "not_regex":
		return assert.NotRegex(*a.NotRegex)
	case "json_path":
		return assert.JSONPath(a.JSONPath.Path, a.JSONPath.Equals)
	case "status":
		return assert.Status(*a.Status), nil
	case "similar_to":
		return assert.SimilarTo(a.SimilarTo.Text, a.SimilarTo.Min, a.SimilarTo.Model), nil
	case "tool_called":
		return assert.ToolCalledWithSchema(a.ToolCalled.Name, a.ToolCalled.Args, a.ToolCalled.Schema)
	case "tool_not_called":
		return assert.ToolNotCalled(a.ToolNotCalled.Name, a.ToolNotCalled.Args), nil
	case "tool_sequence":
		return assert.ToolSequence(a.ToolSequence.Calls, a.ToolSequence.Exact), nil
	case "captured":
		return assert.Captured(a.Captured.Name, a.Captured.Equals, a.Captured.Matches)
	case "golden":
		if len(a.Golden.Turns) == 0 {
			return nil, fmt.Errorf("golden %q: the runner loads golden files; Build takes inline turns only", a.Golden.File)
		}
		return assert.Golden(a.Golden.Turns, a.Golden.Mode, a.Golden.Min, ""), nil
	case "dtmf_received":
		return assert.DTMFReceived(a.DTMFReceived.Equals), nil
	case "call_ended_by":
		return assert.CallEndedBy([]string(a.CallEndedBy)), nil
	case "transferred_to":
		return assert.TransferredTo(a.TransferredTo.Matches)
	case "evaluator":
		return assert.Evaluate(a.Evaluator.Name, a.Evaluator.Min), nil
	case "metric":
		c, err := comparison(a.Metric.Comparators(), func(op string) *float64 {
			switch op {
			case "lt":
				return a.Metric.Lt
			case "lte":
				return a.Metric.Lte
			case "gt":
				return a.Metric.Gt
			case "gte":
				return a.Metric.Gte
			}
			return a.Metric.Eq
		})
		if err != nil {
			return nil, fmt.Errorf("metric %s: %w", a.Metric.Name, err)
		}
		return assert.Metric(a.Metric.Name).Compare(c), nil
	case "turns":
		ops := a.Turns.Comparators()
		if len(ops) != 1 {
			return nil, fmt.Errorf("turns: exactly one comparator is needed")
		}
		var v int
		switch ops[0] {
		case "lt":
			v = *a.Turns.Lt
		case "lte":
			v = *a.Turns.Lte
		case "gt":
			v = *a.Turns.Gt
		case "gte":
			v = *a.Turns.Gte
		default:
			v = *a.Turns.Eq
		}
		return assert.Turns(assert.Comparison{Op: ops[0], Value: float64(v)}), nil
	case "grounded":
		g := a.Grounded
		return assert.Grounded(assert.GroundedCheck{Context: assert.GroundContext{Tool: g.Context.Tool, JSONPath: g.Context.JSONPath, Text: g.Context.Text}, Min: g.Min, Model: g.Model})
	case "handoff":
		h := a.Handoff
		return assert.Handoff(assert.HandoffCheck{Expected: h.Expected, WithinTurns: h.WithinTurns, To: h.To, SummaryContains: h.SummaryContains, Summary: h.Summary})
	case "http":
		h := a.HTTP
		check := assert.HTTPCheck{URL: h.URL, Method: h.Method, Headers: h.Headers, Body: h.Body, Status: h.Status, Contains: h.Contains, Regex: h.Regex, Timeout: time.Duration(h.Timeout)}
		if h.JSONPath != nil {
			check.JSONPath, check.Equals = h.JSONPath.Path, h.JSONPath.Equals
		}
		return assert.HTTPState(check)
	case "command":
		c := a.Command
		return assert.CommandState(assert.CommandCheck{Run: c.Run, Exit: c.Exit, Contains: c.Contains, Regex: c.Regex, Timeout: time.Duration(c.Timeout)})
	case "custom":
		if c, ok := assert.Lookup(*a.Custom); ok {
			return c, nil
		}
		return nil, fmt.Errorf("custom assertion %q is not registered (have %v)", *a.Custom, assert.Registered())
	case "pack":
		return nil, fmt.Errorf("pack %q was not expanded: load the suite with config.LoadSuite or call config.ExpandPacks", *a.Pack)
	case "":
		return nil, fmt.Errorf("an assertion is exactly one of %v (have %v)", config.AssertionKinds, a.Kinds())
	default:
		return nil, fmt.Errorf("unknown assertion %q", kind)
	}
}

func comparison(ops []string, value func(op string) *float64) (assert.Comparison, error) {
	if len(ops) != 1 {
		return assert.Comparison{}, fmt.Errorf("exactly one comparator is needed")
	}
	return assert.Comparison{Op: ops[0], Value: *value(ops[0])}, nil
}

// renderAssertion renders the templated strings of an assertion (contains,
// not_contains, regex, not_regex, the judge rubric) through the run scope,
// so {{vars.x}} and {{captured.x}} work in assertions; a value that does
// not render is left as written and the assertion reports it.
func renderAssertion(a config.Assertion, render func(string) (string, error)) config.Assertion {
	str := func(p *string) *string {
		if p == nil {
			return nil
		}
		if r, err := render(*p); err == nil {
			return &r
		}
		return p
	}
	a.Contains = str(a.Contains)
	a.NotContains = str(a.NotContains)
	a.Regex = str(a.Regex)
	a.NotRegex = str(a.NotRegex)
	if a.Judge != nil {
		j := *a.Judge
		if r, err := render(j.Rubric); err == nil {
			j.Rubric = r
		}
		a.Judge = &j
	}
	return a
}

// renderStateAssertion renders the http and command state assertions with
// the full run scope (secrets included: they go to your backend, not to a
// model): the url, headers, body and the command.
func renderStateAssertion(a config.Assertion, render func(string) (string, error)) config.Assertion {
	str := func(s string) string {
		if r, err := render(s); err == nil {
			return r
		}
		return s
	}
	if a.HTTP != nil {
		h := *a.HTTP
		h.URL = str(h.URL)
		if len(h.Headers) > 0 {
			headers := make(map[string]string, len(h.Headers))
			for k, v := range h.Headers {
				headers[k] = str(v)
			}
			h.Headers = headers
		}
		h.Body = renderAny(h.Body, str)
		a.HTTP = &h
	}
	if a.Command != nil {
		c := *a.Command
		c.Run = str(c.Run)
		a.Command = &c
	}
	return a
}

// renderAny renders every string leaf of v.
func renderAny(v any, str func(string) string) any {
	switch x := v.(type) {
	case string:
		return str(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = renderAny(e, str)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = renderAny(e, str)
		}
		return out
	}
	return v
}
