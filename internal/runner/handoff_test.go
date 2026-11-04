package runner

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/target"
)

func detail(t *testing.T, s string) tassert.HandoffDetail {
	t.Helper()
	var d tassert.HandoffDetail
	require.NoError(t, json.Unmarshal([]byte(s), &d), s)
	return d
}

func TestHandoffDetector(t *testing.T) {
	assert.Nil(t, handoffDetector(nil), "no signal, no detector")

	byTool := handoffDetector(&config.HandoffSignal{Tool: "transfer_to_human", To: "department", Summary: "summary"})
	assert.Empty(t, byTool(target.AgentTurn{Text: "Let me check", ToolCalls: []target.ToolCall{{Name: "lookup_order"}}}, 1))
	d := detail(t, byTool(target.AgentTurn{ToolCalls: []target.ToolCall{{Name: "transfer_to_human", Args: map[string]any{"department": "billing", "summary": "ORD-77 refund"}}}}, 3))
	assert.Equal(t, tassert.HandoffDetail{Turn: 3, To: "billing", Summary: "ORD-77 refund", Via: "tool transfer_to_human"}, d)

	byPath := handoffDetector(&config.HandoffSignal{JSONPath: "$.handoff", To: "$.handoff.queue", Summary: "$.handoff.note"})
	assert.Empty(t, byPath(target.AgentTurn{Raw: map[string]any{"reply": "hi", "handoff": false}}, 1), "false is not a handoff")
	assert.Empty(t, byPath(target.AgentTurn{Raw: map[string]any{"reply": "hi"}}, 1), "absent is not a handoff")
	d = detail(t, byPath(target.AgentTurn{Raw: map[string]any{"handoff": map[string]any{"queue": "sales", "note": "wants a quote"}}}, 2))
	assert.Equal(t, tassert.HandoffDetail{Turn: 2, To: "sales", Summary: "wants a quote", Via: "json_path $.handoff"}, d)
	d = detail(t, handoffDetector(&config.HandoffSignal{JSONPath: "$.transfer"})(target.AgentTurn{Raw: map[string]any{"transfer": "tier2"}}, 1))
	assert.Equal(t, "tier2", d.To, "a string value is the destination when none is named")

	byRegex := handoffDetector(&config.HandoffSignal{Regex: `(?i)transferring you to (\w+)`})
	assert.Empty(t, byRegex(target.AgentTurn{Text: "Our window is 30 days"}, 1))
	d = detail(t, byRegex(target.AgentTurn{Text: "Transferring you to billing now."}, 4))
	assert.Equal(t, tassert.HandoffDetail{Turn: 4, To: "billing", Via: "regex"}, d)

	// A tool signal with a response path for the summary.
	mixed := handoffDetector(&config.HandoffSignal{Tool: "escalate", Summary: "$.meta.summary"})
	d = detail(t, mixed(target.AgentTurn{ToolCalls: []target.ToolCall{{Name: "escalate"}}, Raw: map[string]any{"meta": map[string]any{"summary": "angry about delivery"}}}, 2))
	assert.Equal(t, "angry about delivery", d.Summary)
	assert.Empty(t, d.To)
}
