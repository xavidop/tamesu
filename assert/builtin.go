package assert

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/ohler55/ojg/jp"
)

// funcAssertion adapts a function to Assertion.
type funcAssertion struct {
	name string
	fn   func(ctx context.Context, r *RunResult) Result
}

func (a funcAssertion) Name() string { return a.name }

func (a funcAssertion) Evaluate(ctx context.Context, r *RunResult) Result {
	res := a.fn(ctx, r)
	if res.Name == "" {
		res.Name = a.name
	}
	return res
}

// Func makes an Assertion from a function.
func Func(name string, fn func(ctx context.Context, r *RunResult) Result) Assertion {
	return funcAssertion{name: name, fn: fn}
}

func quote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return fmt.Sprintf("%q", s)
}

// Contains passes when the agent transcript contains s.
func Contains(s string) Assertion {
	return Func("contains "+quote(s), func(_ context.Context, r *RunResult) Result {
		for _, t := range r.Transcript {
			if t.Role == "agent" && strings.Contains(t.Text, s) {
				return Result{Pass: true, Reason: fmt.Sprintf("agent turn %d contains %s", t.Index, quote(s)), Evidence: []string{t.Text}}
			}
		}
		return Result{Pass: false, Reason: fmt.Sprintf("no agent turn contains %s", quote(s))}
	})
}

// NotContains passes when no agent turn contains s.
func NotContains(s string) Assertion {
	return Func("not_contains "+quote(s), func(_ context.Context, r *RunResult) Result {
		for _, t := range r.Transcript {
			if t.Role == "agent" && strings.Contains(t.Text, s) {
				return Result{Pass: false, Reason: fmt.Sprintf("agent turn %d contains %s", t.Index, quote(s)), Evidence: []string{t.Text}}
			}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("no agent turn contains %s", quote(s))}
	})
}

// Regex passes when the agent transcript matches pattern.
func Regex(pattern string) (Assertion, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("regex %q: %w", pattern, err)
	}
	return Func("regex "+quote(pattern), func(_ context.Context, r *RunResult) Result {
		for _, t := range r.Transcript {
			if t.Role == "agent" {
				if m := re.FindString(t.Text); m != "" || re.MatchString(t.Text) {
					return Result{Pass: true, Reason: fmt.Sprintf("agent turn %d matches %s", t.Index, quote(pattern)), Evidence: []string{m}}
				}
			}
		}
		return Result{Pass: false, Reason: fmt.Sprintf("no agent turn matches %s", quote(pattern))}
	}), nil
}

// NotRegex passes when no agent turn matches pattern.
func NotRegex(pattern string) (Assertion, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("not_regex %q: %w", pattern, err)
	}
	return Func("not_regex "+quote(pattern), func(_ context.Context, r *RunResult) Result {
		for _, t := range r.Transcript {
			if t.Role == "agent" {
				if m := re.FindString(t.Text); m != "" || re.MatchString(t.Text) {
					return Result{Pass: false, Reason: fmt.Sprintf("agent turn %d matches %s", t.Index, quote(pattern)), Evidence: []string{m}}
				}
			}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("no agent turn matches %s", quote(pattern))}
	}), nil
}

// MustRegex is Regex that panics on a bad pattern, for Go tests.
func MustRegex(pattern string) Assertion {
	a, err := Regex(pattern)
	if err != nil {
		panic(err)
	}
	return a
}

// JSONPath passes when path exists in every agent response and, when equals
// is not nil, equals it (numbers and strings compared after JSON normalisation).
func JSONPath(path string, equals any) (Assertion, error) {
	expr, err := jp.ParseString(path)
	if err != nil {
		return nil, fmt.Errorf("json_path %q: %w", path, err)
	}
	name := "json_path " + path
	if equals != nil {
		name += fmt.Sprintf(" == %v", equals)
	}
	return Func(name, func(_ context.Context, r *RunResult) Result {
		if len(r.Responses) == 0 {
			return Result{Pass: false, Reason: "no responses were captured (json_path applies to rest targets)"}
		}
		for i, resp := range r.Responses {
			got := expr.Get(resp)
			if len(got) == 0 {
				return Result{Pass: false, Reason: fmt.Sprintf("turn %d: %s is absent", i+1, path)}
			}
			if equals != nil && !jsonEqual(got[0], equals) {
				return Result{Pass: false, Reason: fmt.Sprintf("turn %d: %s = %s, want %s", i+1, path, jsonString(got[0]), jsonString(equals))}
			}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("%s holds on all %d turns", path, len(r.Responses))}
	}), nil
}

// jsonEqual compares two values after a JSON round trip, so an int and a
// float64, or a map[string]any and a struct, compare by value.
func jsonEqual(a, b any) bool {
	return jsonString(a) == jsonString(b)
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// Status passes when every agent turn had the HTTP status code.
func Status(code int) Assertion {
	return Func(fmt.Sprintf("status %d", code), func(_ context.Context, r *RunResult) Result {
		if len(r.Status) == 0 {
			return Result{Pass: false, Reason: "no HTTP statuses were captured (status applies to rest targets)"}
		}
		for i, s := range r.Status {
			if s != code {
				return Result{Pass: false, Reason: fmt.Sprintf("turn %d returned HTTP %d, want %d", i+1, s, code)}
			}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("all %d turns returned HTTP %d", len(r.Status), code)}
	})
}

// ToolCalled passes when the agent called the tool, with args matching every
// key of args (a subset of the call's arguments) when args is not empty.
func ToolCalled(name string, args map[string]any) Assertion {
	return Func("tool_called "+name, func(_ context.Context, r *RunResult) Result {
		var seen []string
		for _, c := range r.ToolCalls {
			seen = append(seen, c.Name)
			if c.Name == name && argsMatch(c.Args, args) {
				return Result{Pass: true, Reason: fmt.Sprintf("the agent called %s", describeCall(c.Name, c.Args)), Evidence: []string{jsonString(c.Args)}}
			}
		}
		if len(args) > 0 {
			return Result{Pass: false, Reason: fmt.Sprintf("the agent did not call %s (tool calls: %s)", describeCall(name, args), listOr(seen))}
		}
		return Result{Pass: false, Reason: fmt.Sprintf("the agent did not call %s (tool calls: %s)", name, listOr(seen))}
	})
}

// ToolNotCalled passes when the agent never called the tool (with those args).
func ToolNotCalled(name string, args map[string]any) Assertion {
	return Func("tool_not_called "+name, func(_ context.Context, r *RunResult) Result {
		for _, c := range r.ToolCalls {
			if c.Name == name && argsMatch(c.Args, args) {
				return Result{Pass: false, Reason: fmt.Sprintf("the agent called %s", describeCall(c.Name, c.Args)), Evidence: []string{jsonString(c.Args)}}
			}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("the agent did not call %s", name)}
	})
}

// SLO passes when the metric is at most max, or was not measured in this
// run (an audio metric on a text test), so one SLO applies to every test.
func SLO(metric string, max float64) Assertion {
	return Func(fmt.Sprintf("slo/%s <= %s", metric, formatNumber(max)), func(_ context.Context, r *RunResult) Result {
		v, ok := r.Metrics[metric]
		if !ok {
			return Result{Pass: true, Reason: fmt.Sprintf("%s was not measured in this run", metric)}
		}
		if v > max {
			return Result{Pass: false, Reason: fmt.Sprintf("%s = %s, over the limit of %s", metric, formatNumber(v), formatNumber(max))}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("%s = %s, within %s", metric, formatNumber(v), formatNumber(max))}
	})
}

// formatNumber prints a metric value without trailing noise.
func formatNumber(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
}

// Captured passes when the run captured name and, when given, the value
// equals or matches the expectation.
func Captured(name string, equals, matches *string) (Assertion, error) {
	var re *regexp.Regexp
	if matches != nil {
		var err error
		if re, err = regexp.Compile(*matches); err != nil {
			return nil, fmt.Errorf("captured %s: %w", name, err)
		}
	}
	label := "captured " + name
	switch {
	case equals != nil:
		label += " == " + *equals
	case matches != nil:
		label += " ~ " + *matches
	}
	return Func(label, func(_ context.Context, r *RunResult) Result {
		v, ok := r.Captures[name]
		if !ok {
			return Result{Pass: false, Reason: fmt.Sprintf("%s was not captured (captured: %s)", name, listOr(captureNames(r.Captures)))}
		}
		switch {
		case equals != nil && v != *equals:
			return Result{Pass: false, Reason: fmt.Sprintf("%s = %q, want %q", name, v, *equals), Evidence: []string{v}}
		case re != nil && !re.MatchString(v):
			return Result{Pass: false, Reason: fmt.Sprintf("%s = %q does not match %s", name, v, *matches), Evidence: []string{v}}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("%s = %q", name, v), Evidence: []string{v}}
	}), nil
}

func captureNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ToolSequence passes when the tool calls happened in the given order: as a
// subsequence of the run's calls, or exactly that list with exact.
func ToolSequence(names []string, exact bool) Assertion {
	label := "tool_sequence " + strings.Join(names, " → ")
	return Func(label, func(_ context.Context, r *RunResult) Result {
		var seen []string
		for _, c := range r.ToolCalls {
			seen = append(seen, c.Name)
		}
		if exact {
			for i := 0; i < len(names) || i < len(seen); i++ {
				switch {
				case i >= len(names):
					return Result{Pass: false, Reason: fmt.Sprintf("call %d was %s, beyond the expected sequence (tool calls: %s)", i+1, seen[i], listOr(seen))}
				case i >= len(seen):
					return Result{Pass: false, Reason: fmt.Sprintf("call %d (%s) never happened (tool calls: %s)", i+1, names[i], listOr(seen))}
				case names[i] != seen[i]:
					return Result{Pass: false, Reason: fmt.Sprintf("call %d was %s, want %s (tool calls: %s)", i+1, seen[i], names[i], listOr(seen))}
				}
			}
			return Result{Pass: true, Reason: fmt.Sprintf("the agent called exactly %s", strings.Join(names, ", then "))}
		}
		next := 0
		for _, name := range seen {
			if next < len(names) && name == names[next] {
				next++
			}
		}
		if next < len(names) {
			if next == 0 {
				return Result{Pass: false, Reason: fmt.Sprintf("the agent never called %s (tool calls: %s)", names[0], listOr(seen))}
			}
			return Result{Pass: false, Reason: fmt.Sprintf("%s was not called after %s (tool calls: %s)", names[next], names[next-1], listOr(seen))}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("the agent called %s in order", strings.Join(names, ", then "))}
	})
}

func argsMatch(have, want map[string]any) bool {
	for k, v := range want {
		got, ok := have[k]
		if !ok || !jsonEqual(got, v) {
			return false
		}
	}
	return true
}

func describeCall(name string, args map[string]any) string {
	if len(args) == 0 {
		return name
	}
	return name + jsonString(args)
}

func listOr(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

// Comparison is one of lt, lte, gt, gte, eq against a number.
type Comparison struct {
	Op    string
	Value float64
}

// Holds evaluates the comparison.
func (c Comparison) Holds(v float64) bool {
	switch c.Op {
	case "lt":
		return v < c.Value
	case "lte":
		return v <= c.Value
	case "gt":
		return v > c.Value
	case "gte":
		return v >= c.Value
	case "eq":
		return v == c.Value
	}
	return false
}

// Symbol is the comparison operator for people.
func (c Comparison) Symbol() string {
	return map[string]string{"lt": "<", "lte": "<=", "gt": ">", "gte": ">=", "eq": "=="}[c.Op]
}

// MetricBuilder builds a metric assertion: assert.Metric("time_to_first_audio_ms").Lt(1500).
type MetricBuilder struct{ name string }

// Metric starts a metric assertion on the named built-in metric.
func Metric(name string) MetricBuilder { return MetricBuilder{name: name} }

// Lt, Lte, Gt, Gte and Eq finish the metric assertion.
func (m MetricBuilder) Lt(v float64) Assertion  { return m.cmp(Comparison{"lt", v}) }
func (m MetricBuilder) Lte(v float64) Assertion { return m.cmp(Comparison{"lte", v}) }
func (m MetricBuilder) Gt(v float64) Assertion  { return m.cmp(Comparison{"gt", v}) }
func (m MetricBuilder) Gte(v float64) Assertion { return m.cmp(Comparison{"gte", v}) }
func (m MetricBuilder) Eq(v float64) Assertion  { return m.cmp(Comparison{"eq", v}) }

// Compare finishes the metric assertion with an explicit comparison.
func (m MetricBuilder) Compare(c Comparison) Assertion { return m.cmp(c) }

func (m MetricBuilder) cmp(c Comparison) Assertion {
	return Func(fmt.Sprintf("metric %s %s %g", m.name, c.Symbol(), c.Value), func(_ context.Context, r *RunResult) Result {
		v, ok := r.Metrics[m.name]
		if !ok {
			return Result{Pass: false, Reason: fmt.Sprintf("metric %s was not collected (have %s)", m.name, listOr(metricNames(r.Metrics)))}
		}
		return Result{Pass: c.Holds(v), Reason: fmt.Sprintf("%s = %g, want %s %g", m.name, v, c.Symbol(), c.Value)}
	})
}

func metricNames(m map[string]float64) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Turns compares the number of agent turns: assert.Turns(assert.Comparison{Op: "lte", Value: 6}).
func Turns(c Comparison) Assertion {
	return Func(fmt.Sprintf("turns %s %g", c.Symbol(), c.Value), func(_ context.Context, r *RunResult) Result {
		n := r.AgentTurns()
		return Result{Pass: c.Holds(float64(n)), Reason: fmt.Sprintf("the agent took %d turns, want %s %g", n, c.Symbol(), c.Value)}
	})
}

// JudgeOption configures a judge assertion.
type JudgeOption func(*JudgeRequest)

// WithScale sets the judge scale: "binary" or "1-5".
func WithScale(scale string) JudgeOption { return func(j *JudgeRequest) { j.Scale = scale } }

// WithMin sets the minimum score to pass on the 1-5 scale (default 4).
func WithMin(min float64) JudgeOption { return func(j *JudgeRequest) { j.Min = min } }

// WithModel sets the judge model (a Genkit reference).
func WithModel(model string) JudgeOption { return func(j *JudgeRequest) { j.Model = model } }

// Judge grades the run against a rubric with the run's Judger (an LLM).
func Judge(rubric string, opts ...JudgeOption) Assertion {
	base := JudgeRequest{Rubric: rubric, Scale: "binary"}
	for _, o := range opts {
		o(&base)
	}
	if base.Scale == "1-5" && base.Min == 0 {
		base.Min = 4
	}
	return Func("judge "+quote(rubric), func(ctx context.Context, r *RunResult) Result {
		if r.Judger == nil {
			return Result{Pass: false, Reason: "no judge model is available in this run"}
		}
		req := base
		req.Transcript = r.Transcript
		req.ToolCalls = r.ToolCalls
		req.Metrics = r.Metrics
		v, err := r.Judger.Judge(ctx, req)
		if err != nil {
			return Result{Pass: false, Reason: "judge failed: " + err.Error()}
		}
		pass := v.Pass
		if req.Scale == "1-5" {
			pass = v.Score >= req.Min
		}
		reason := v.Reason
		if req.Scale == "1-5" {
			reason = fmt.Sprintf("score %g (min %g): %s", v.Score, req.Min, v.Reason)
		}
		return Result{Pass: pass, Score: v.Score, Reason: reason, Evidence: v.Evidence, TraceID: v.TraceID}
	})
}

// Evaluate runs a registered Genkit evaluator on the run (input: the first
// user turn, output: the agent transcript) and passes when its score is at
// least min (or it reports PASS when it has no numeric score).
func Evaluate(evaluator string, min float64) Assertion {
	return Func("evaluator "+evaluator, func(ctx context.Context, r *RunResult) Result {
		if r.Evaluator == nil {
			return Result{Pass: false, Reason: "no evaluator is available in this run"}
		}
		input := ""
		for _, t := range r.Transcript {
			if t.Role == "user" {
				input = t.Text
				break
			}
		}
		v, err := r.Evaluator.Evaluate(ctx, EvalRequest{Evaluator: evaluator, Input: input, Output: r.AgentText()})
		if err != nil {
			return Result{Pass: false, Reason: "evaluator failed: " + err.Error()}
		}
		pass := v.Pass
		if min > 0 {
			pass = v.Score >= min
		}
		reason := v.Reason
		if reason == "" {
			reason = fmt.Sprintf("%s scored %g", evaluator, v.Score)
		}
		return Result{Pass: pass, Score: v.Score, Reason: reason}
	})
}

// OnTurn evaluates inner on one agent turn only: the 1-based turn n, or the
// last turn when last is set.
func OnTurn(n int, last bool, inner Assertion) Assertion {
	label := fmt.Sprintf("turn %d", n)
	if last {
		label = "last turn"
	}
	return Func(inner.Name()+" ("+label+")", func(ctx context.Context, r *RunResult) Result {
		turns := r.AgentTurns()
		if turns == 0 {
			return Result{Pass: false, Reason: "the agent produced no turns"}
		}
		idx := n
		if last {
			idx = turns
		}
		if idx > turns {
			return Result{Pass: false, Reason: fmt.Sprintf("the agent produced %d turns, so there is no turn %d", turns, idx)}
		}
		res := inner.Evaluate(ctx, r.Turn(idx))
		res.Name = inner.Name() + " (" + label + ")"
		return res
	})
}

var (
	customMu sync.RWMutex
	custom   = map[string]func() Assertion{}
)

// Register makes a Go assertion available to suites as `custom: <name>`.
func Register(name string, factory func() Assertion) {
	customMu.Lock()
	defer customMu.Unlock()
	custom[name] = factory
}

// Lookup returns the registered custom assertion.
func Lookup(name string) (Assertion, bool) {
	customMu.RLock()
	defer customMu.RUnlock()
	f, ok := custom[name]
	if !ok {
		return nil, false
	}
	return f(), true
}

// Registered lists the custom assertion names, sorted.
func Registered() []string {
	customMu.RLock()
	defer customMu.RUnlock()
	names := make([]string, 0, len(custom))
	for n := range custom {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// deepEqual is reflect.DeepEqual, kept behind a name for the tests.
var deepEqual = reflect.DeepEqual
