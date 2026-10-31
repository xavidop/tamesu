package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestAssertionFlexibleForms(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
		check   func(t *testing.T, a Assertion)
	}{
		{
			name: "judge string",
			in:   `judge: "agent is polite"`,
			check: func(t *testing.T, a Assertion) {
				require.NotNil(t, a.Judge)
				assert.Equal(t, "agent is polite", a.Judge.Rubric)
				assert.Equal(t, "judge", a.Kind())
			},
		},
		{
			name: "judge object",
			in:   "judge:\n  rubric: r\n  scale: 1-5\n  min: 4",
			check: func(t *testing.T, a Assertion) {
				require.NotNil(t, a.Judge)
				assert.Equal(t, Judge{Rubric: "r", Scale: "1-5", Min: 4}, *a.Judge)
			},
		},
		{name: "judge unknown field", in: "judge:\n  rubrik: r", wantErr: "line 2: field rubrik not found in type Judge"},
		{
			name: "tool_called string",
			in:   `tool_called: reschedule_booking`,
			check: func(t *testing.T, a Assertion) {
				require.NotNil(t, a.ToolCalled)
				assert.Equal(t, "reschedule_booking", a.ToolCalled.Name)
			},
		},
		{
			name: "tool_called object",
			in:   "tool_called:\n  name: reschedule_booking\n  args:\n    day: saturday",
			check: func(t *testing.T, a Assertion) {
				require.NotNil(t, a.ToolCalled)
				assert.Equal(t, "reschedule_booking", a.ToolCalled.Name)
				assert.Equal(t, map[string]any{"day": "saturday"}, a.ToolCalled.Args)
			},
		},
		{
			name: "call_ended_by string",
			in:   `call_ended_by: agent`,
			check: func(t *testing.T, a Assertion) {
				assert.Equal(t, StringList{"agent"}, a.CallEndedBy)
				assert.Equal(t, "call_ended_by", a.Kind())
			},
		},
		{
			name: "call_ended_by list",
			in:   `call_ended_by: [agent, caller]`,
			check: func(t *testing.T, a Assertion) {
				assert.Equal(t, StringList{"agent", "caller"}, a.CallEndedBy)
			},
		},
		{
			name: "on_turn number",
			in:   "contains: hi\non_turn: 3",
			check: func(t *testing.T, a Assertion) {
				assert.Equal(t, OnTurn{N: 3}, a.OnTurn)
				assert.True(t, a.OnTurn.IsSet())
				assert.Equal(t, "contains", a.Kind())
			},
		},
		{
			name: "on_turn last",
			in:   "contains: hi\non_turn: last",
			check: func(t *testing.T, a Assertion) {
				assert.Equal(t, OnTurn{Last: true}, a.OnTurn)
			},
		},
		{name: "on_turn first", in: "contains: hi\non_turn: first", wantErr: `line 2: on_turn must be a turn number (1 or more) or "last", not "first"`},
		{
			name: "evaluator string",
			in:   `evaluator: genkitEval/faithfulness`,
			check: func(t *testing.T, a Assertion) {
				require.NotNil(t, a.Evaluator)
				assert.Equal(t, "genkitEval/faithfulness", a.Evaluator.Name)
			},
		},
		{
			name: "evaluator object",
			in:   "evaluator:\n  name: genkitEval/faithfulness\n  min: 0.8",
			check: func(t *testing.T, a Assertion) {
				require.NotNil(t, a.Evaluator)
				assert.Equal(t, Evaluator{Name: "genkitEval/faithfulness", Min: 0.8}, *a.Evaluator)
			},
		},
		{
			name: "metric",
			in:   "metric: { name: turn_latency_p95_ms, lt: 4000 }",
			check: func(t *testing.T, a Assertion) {
				require.NotNil(t, a.Metric)
				assert.Equal(t, []string{"lt"}, a.Metric.Comparators())
				ok, reason := a.Metric.Compare(3999)
				assert.True(t, ok)
				assert.Equal(t, "turn_latency_p95_ms = 3999, want < 4000", reason)
			},
		},
		{
			name: "turns",
			in:   "turns: { lte: 6 }",
			check: func(t *testing.T, a Assertion) {
				require.NotNil(t, a.Turns)
				ok, _ := a.Turns.Compare("turns", 7)
				assert.False(t, ok)
			},
		},
		{
			name: "two kinds",
			in:   "contains: a\nregex: b",
			check: func(t *testing.T, a Assertion) {
				assert.Equal(t, []string{"contains", "regex"}, a.Kinds())
				assert.Equal(t, "", a.Kind())
			},
		},
		{
			name: "no kind",
			in:   "on_turn: 2",
			check: func(t *testing.T, a Assertion) {
				assert.Empty(t, a.Kinds())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a Assertion
			err := yaml.Unmarshal([]byte(tt.in), &a)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			tt.check(t, a)
		})
	}
}

func TestStepKind(t *testing.T) {
	tests := []struct {
		name string
		step Step
		want string
	}{
		{"goal", Step{Goal: "log in"}, "goal"},
		{"expect", Step{Expect: "cart shows 1"}, "expect"},
		{"action", Step{Action: map[string]any{"screenshot": "cart"}}, "action"},
		{"two", Step{Goal: "a", Expect: "b"}, ""},
		{"none", Step{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.step.Kind())
		})
	}
}
