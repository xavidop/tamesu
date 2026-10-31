package assert

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

func sampleRun() *RunResult {
	t0 := time.Date(2026, 10, 31, 14, 0, 0, 0, time.UTC)
	return &RunResult{
		TestName: "refund",
		Transcript: []Turn{
			{Role: "user", Text: "Can I return shoes bought 40 days ago?", At: t0},
			{Role: "agent", Text: "Our return window is 30 days, so unfortunately not.", At: t0.Add(time.Second), Index: 1},
			{Role: "user", Text: "No exceptions?", At: t0.Add(2 * time.Second)},
			{Role: "agent", Text: "I'm sorry, the 30-day policy has no exceptions. Order #123456 is closed.", At: t0.Add(3 * time.Second), Index: 2},
		},
		ToolCalls: []target.ToolCall{{Name: "lookup_order", Args: map[string]any{"id": "123456", "days": 40}}},
		Metrics:   map[string]float64{"turn_latency_ms_p95": 1200, "turns": 2},
		Status:    []int{200, 200},
		Responses: []any{map[string]any{"status": "ok", "reply": "Our return window..."}, map[string]any{"status": "ok", "end_session": true}},
	}
}

type fakeJudger struct {
	verdict JudgeVerdict
	err     error
	got     JudgeRequest
}

func (f *fakeJudger) Judge(_ context.Context, req JudgeRequest) (JudgeVerdict, error) {
	f.got = req
	return f.verdict, f.err
}

type fakeEvaluator struct{ verdict EvalVerdict }

func (f fakeEvaluator) Evaluate(context.Context, EvalRequest) (EvalVerdict, error) {
	return f.verdict, nil
}

func TestBuiltins(t *testing.T) {
	jsonOK, err := JSONPath("$.status", "ok")
	require.NoError(t, err)
	jsonMissing, err := JSONPath("$.nope", nil)
	require.NoError(t, err)
	jsonExists, err := JSONPath("$.status", nil)
	require.NoError(t, err)
	tests := []struct {
		name       string
		a          Assertion
		wantPass   bool
		wantReason string
	}{
		{"contains hit", Contains("30 days"), true, `agent turn 1 contains "30 days"`},
		{"contains miss", Contains("refund approved"), false, `no agent turn contains "refund approved"`},
		{"not_contains hit", NotContains("I'm just an AI"), true, `no agent turn contains "I'm just an AI"`},
		{"not_contains miss", NotContains("no exceptions"), false, `agent turn 2 contains "no exceptions"`},
		{"regex", MustRegex(`Order #\d{6}`), true, `agent turn 2 matches "Order #\\d{6}"`},
		{"regex miss", MustRegex(`#\d{9}`), false, `no agent turn matches "#\\d{9}"`},
		{"json_path equals", jsonOK, true, "$.status holds on all 2 turns"},
		{"json_path exists", jsonExists, true, "$.status holds on all 2 turns"},
		{"json_path absent", jsonMissing, false, "turn 1: $.nope is absent"},
		{"status ok", Status(200), true, "all 2 turns returned HTTP 200"},
		{"status wrong", Status(201), false, "turn 1 returned HTTP 200, want 201"},
		{"tool_called", ToolCalled("lookup_order", nil), true, `the agent called lookup_order{"days":40,"id":"123456"}`},
		{"tool_called args int vs float", ToolCalled("lookup_order", map[string]any{"days": 40.0}), true, `the agent called lookup_order{"days":40,"id":"123456"}`},
		{"tool_called wrong args", ToolCalled("lookup_order", map[string]any{"id": "999"}), false, `the agent did not call lookup_order{"id":"999"} (tool calls: lookup_order)`},
		{"tool_called missing", ToolCalled("refund", nil), false, "the agent did not call refund (tool calls: lookup_order)"},
		{"tool_not_called", ToolNotCalled("refund", nil), true, "the agent did not call refund"},
		{"tool_not_called hit", ToolNotCalled("lookup_order", nil), false, `the agent called lookup_order{"days":40,"id":"123456"}`},
		{"metric lt", Metric("turn_latency_ms_p95").Lt(4000), true, "turn_latency_ms_p95 = 1200, want < 4000"},
		{"metric gte fail", Metric("turn_latency_ms_p95").Gte(2000), false, "turn_latency_ms_p95 = 1200, want >= 2000"},
		{"metric missing", Metric("time_to_first_audio_ms").Lt(1), false, "metric time_to_first_audio_ms was not collected (have turn_latency_ms_p95, turns)"},
		{"turns", Turns(Comparison{"lte", 6}), true, "the agent took 2 turns, want <= 6"},
		{"turns fail", Turns(Comparison{"lt", 2}), false, "the agent took 2 turns, want < 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := tt.a.Evaluate(context.Background(), sampleRun())
			assert.Equal(t, tt.wantPass, res.Pass, res.Reason)
			assert.Equal(t, tt.wantReason, res.Reason)
			assert.Equal(t, tt.a.Name(), res.Name)
		})
	}
}

func TestJSONPathRejectsBadPath(t *testing.T) {
	_, err := JSONPath("$[", nil)
	require.Error(t, err)
	_, err = Regex("(")
	require.Error(t, err)
}

func TestJudge(t *testing.T) {
	r := sampleRun()
	j := &fakeJudger{verdict: JudgeVerdict{Pass: true, Score: 1, Reason: "states the policy", Evidence: []string{"30 days"}, TraceID: "t1"}}
	r.Judger = j
	res := Judge("states the policy").Evaluate(context.Background(), r)
	assert.True(t, res.Pass)
	assert.Equal(t, "states the policy", res.Reason)
	assert.Equal(t, []string{"30 days"}, res.Evidence)
	assert.Equal(t, "t1", res.TraceID)
	assert.Equal(t, "binary", j.got.Scale)
	assert.Len(t, j.got.Transcript, 4)
	assert.Equal(t, r.Metrics, j.got.Metrics)

	j.verdict = JudgeVerdict{Pass: false, Score: 3, Reason: "misses the exception"}
	res = Judge("r", WithScale("1-5"), WithModel("x/y")).Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Equal(t, "score 3 (min 4): misses the exception", res.Reason)
	assert.Equal(t, "x/y", j.got.Model)
	assert.Equal(t, 4.0, j.got.Min)
	res = Judge("r", WithScale("1-5"), WithMin(3)).Evaluate(context.Background(), r)
	assert.True(t, res.Pass)

	j.err = errors.New("boom")
	res = Judge("r").Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Equal(t, "judge failed: boom", res.Reason)

	r.Judger = nil
	res = Judge("r").Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Equal(t, "no judge model is available in this run", res.Reason)
}

func TestEvaluate(t *testing.T) {
	r := sampleRun()
	r.Evaluator = fakeEvaluator{EvalVerdict{Pass: true, Score: 0.9}}
	res := Evaluate("genkitEval/faithfulness", 0.8).Evaluate(context.Background(), r)
	assert.True(t, res.Pass)
	assert.Equal(t, "genkitEval/faithfulness scored 0.9", res.Reason)
	res = Evaluate("genkitEval/faithfulness", 0.95).Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	r.Evaluator = nil
	assert.False(t, Evaluate("x", 0).Evaluate(context.Background(), r).Pass)
}

func TestOnTurn(t *testing.T) {
	r := sampleRun()
	res := OnTurn(1, false, Contains("no exceptions")).Evaluate(context.Background(), r)
	assert.False(t, res.Pass, "turn 1 does not say it")
	assert.Equal(t, `contains "no exceptions" (turn 1)`, res.Name)
	res = OnTurn(0, true, Contains("no exceptions")).Evaluate(context.Background(), r)
	assert.True(t, res.Pass)
	assert.Equal(t, `contains "no exceptions" (last turn)`, res.Name)
	res = OnTurn(5, false, Contains("x")).Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Equal(t, "the agent produced 2 turns, so there is no turn 5", res.Reason)
	view := r.Turn(2)
	assert.Len(t, view.Transcript, 2)
	assert.Equal(t, "No exceptions?", view.Transcript[0].Text)
	assert.Equal(t, []int{200}, view.Status)
	assert.Len(t, view.Responses, 1)
	status, _ := JSONPath("$.end_session", true)
	assert.True(t, OnTurn(0, true, status).Evaluate(context.Background(), r).Pass)
}

func TestRegistry(t *testing.T) {
	Register("always-pass", func() Assertion {
		return Func("always-pass", func(context.Context, *RunResult) Result { return Result{Pass: true, Reason: "ok"} })
	})
	a, ok := Lookup("always-pass")
	require.True(t, ok)
	assert.True(t, a.Evaluate(context.Background(), sampleRun()).Pass)
	_, ok = Lookup("nope")
	assert.False(t, ok)
	assert.Contains(t, Registered(), "always-pass")
}

func TestAgentText(t *testing.T) {
	r := sampleRun()
	assert.Equal(t, 2, r.AgentTurns())
	assert.Contains(t, r.AgentText(), "Our return window is 30 days")
	assert.True(t, deepEqual(r.Turn(99).Transcript, r.Turn(2).Transcript), "past the end means the last turn")
}
