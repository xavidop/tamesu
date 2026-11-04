package assert

import (
	"context"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/xavidop/tamesu/target"
)

func TestDelegatedTo(t *testing.T) {
	r := &RunResult{ToolCalls: []target.ToolCall{
		{Name: "lookup_order", Args: map[string]any{"id": "ORD-1"}},
		{Name: "billing", Kind: target.ToolCallDelegation, Args: map[string]any{"message": "Refund order ORD-1 please"}, Result: "Refund issued"},
		{Name: "shipping", Kind: target.ToolCallDelegation, Args: map[string]any{"message": "Where is ORD-1?"}},
	}}
	ctx := context.Background()
	res := DelegatedTo("billing", "", nil).Evaluate(ctx, r)
	assert.True(t, res.Pass)
	assert.Contains(t, res.Reason, `delegated to billing: "Refund order ORD-1 please"`)
	assert.True(t, DelegatedTo("billing", "refund", nil).Evaluate(ctx, r).Pass, "contains is case-insensitive")
	assert.True(t, DelegatedTo("billing", "", regexp.MustCompile(`ORD-\d+`)).Evaluate(ctx, r).Pass)
	res = DelegatedTo("billing", "cancel", nil).Evaluate(ctx, r)
	assert.False(t, res.Pass)
	assert.Contains(t, res.Reason, `no message contained "cancel"`)
	res = DelegatedTo("payments", "", nil).Evaluate(ctx, r)
	assert.False(t, res.Pass)
	assert.Equal(t, "the agent did not delegate to payments (delegations: billing, shipping)", res.Reason)
	assert.Len(t, Delegations(r), 2, "plain tool calls are not delegations")
	assert.Equal(t, "delegated_to billing", DelegatedTo("billing", "", nil).Name())
}
