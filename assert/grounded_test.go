package assert

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

type fakeGrounder struct {
	verdict GroundVerdict
	err     error
	got     GroundRequest
	calls   int
}

func (f *fakeGrounder) Ground(_ context.Context, req GroundRequest) (GroundVerdict, error) {
	f.calls++
	f.got = req
	return f.verdict, f.err
}

func groundedRun(g Grounder) *RunResult {
	return &RunResult{
		Transcript: []Turn{{Role: "user", Text: "Can I return after 40 days?"}, {Role: "agent", Text: "The window is 30 days, so no; I can offer a voucher.", Index: 1}},
		ToolCalls:  []target.ToolCall{{Name: "lookup_policy", Args: map[string]any{"topic": "returns"}, Result: map[string]any{"window_days": 30, "voucher": "10%"}}, {Name: "other"}},
		Responses:  []any{map[string]any{"reply": "...", "sources": []any{map[string]any{"text": "Returns within 30 days."}, map[string]any{"text": "A 10% voucher after."}}}},
		Grounder:   g,
		Language:   "en",
	}
}

func TestGrounded(t *testing.T) {
	g := &fakeGrounder{verdict: GroundVerdict{Claims: []Claim{
		{Text: "The return window is 30 days", Supported: true, Evidence: "window_days: 30"},
		{Text: "A voucher is offered", Supported: true, Evidence: "voucher 10%"},
	}, Reason: "both claims come from the policy lookup", TraceID: "tr"}}

	a, err := Grounded(GroundedCheck{Context: GroundContext{Tool: "lookup_policy"}})
	require.NoError(t, err)
	assert.Equal(t, "grounded in lookup_policy", a.Name())
	res := a.Evaluate(context.Background(), groundedRun(g))
	assert.True(t, res.Pass, res.Reason)
	assert.Equal(t, 1.0, res.Score)
	assert.Equal(t, "2 of 2 claims are supported by the context: both claims come from the policy lookup", res.Reason)
	assert.Equal(t, []string{"✓ The return window is 30 days — window_days: 30", "✓ A voucher is offered — voucher 10%"}, res.Evidence)
	assert.Equal(t, "tr", res.TraceID)
	assert.Equal(t, []string{`{"voucher":"10%","window_days":30}`}, g.got.Context, "the tool's result is the context, as JSON")
	assert.Equal(t, "en", g.got.Language)
	assert.Len(t, g.got.Transcript, 2)

	a, _ = Grounded(GroundedCheck{Context: GroundContext{JSONPath: "$.sources[*].text", Text: []string{"Shipping takes 2 to 4 days."}}})
	assert.Equal(t, "grounded in $.sources[*].text", a.Name())
	res = a.Evaluate(context.Background(), groundedRun(g))
	assert.True(t, res.Pass)
	assert.Equal(t, []string{"Shipping takes 2 to 4 days.", "Returns within 30 days.", "A 10% voucher after."}, g.got.Context, "given text first, then every response's passages")

	// Partial support against min.
	g.verdict = GroundVerdict{Claims: []Claim{{Text: "a", Supported: true}, {Text: "b", Supported: false, Evidence: "nothing about b"}, {Text: "c", Supported: true}}}
	a, _ = Grounded(GroundedCheck{Context: GroundContext{Tool: "lookup_policy"}})
	res = a.Evaluate(context.Background(), groundedRun(g))
	assert.False(t, res.Pass, "all claims must be supported by default")
	assert.InDelta(t, 2.0/3, res.Score, 0.001)
	assert.Contains(t, res.Evidence[1], "✗ b — nothing about b")
	a, _ = Grounded(GroundedCheck{Context: GroundContext{Tool: "lookup_policy"}, Min: 0.6})
	assert.True(t, a.Evaluate(context.Background(), groundedRun(g)).Pass, "two thirds passes a 0.6 minimum")

	// No claims at all passes.
	g.verdict = GroundVerdict{}
	res = a.Evaluate(context.Background(), groundedRun(g))
	assert.True(t, res.Pass)
	assert.Equal(t, "the agent made no factual claims", res.Reason)
}

func TestGroundedWithoutContextOrJudge(t *testing.T) {
	g := &fakeGrounder{}
	a, _ := Grounded(GroundedCheck{Context: GroundContext{Tool: "never_called"}})
	res := a.Evaluate(context.Background(), groundedRun(g))
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "never_called was never called or returned nothing")
	assert.Equal(t, 0, g.calls, "no model call without context")

	a, _ = Grounded(GroundedCheck{Context: GroundContext{JSONPath: "$.nothing"}})
	res = a.Evaluate(context.Background(), groundedRun(g))
	assert.Contains(t, res.Reason, "$.nothing matched nothing")

	r := groundedRun(nil)
	a, _ = Grounded(GroundedCheck{Context: GroundContext{Tool: "lookup_policy"}})
	res = a.Evaluate(context.Background(), r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "no judge model")

	g.err = errors.New("model down")
	res = a.Evaluate(context.Background(), groundedRun(g))
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, "model down")

	_, err := Grounded(GroundedCheck{Context: GroundContext{JSONPath: "$["}})
	assert.Error(t, err)
	_, err = Grounded(GroundedCheck{Context: GroundContext{Text: []string{"x"}}, Min: 2})
	assert.Error(t, err)
}
