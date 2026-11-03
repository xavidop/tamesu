//go:build llmfake

package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

func TestChat(t *testing.T) {
	c := client(t)
	before := c.Usage()
	temp := 0.2
	resp, err := c.Chat(context.Background(), target.ChatRequest{
		System: "You are the support agent.",
		Messages: []target.ChatMessage{
			{Role: "user", Text: "Can I return shoes bought 40 days ago?"},
			{Role: "model", ToolCalls: []target.ChatToolCall{{Ref: "1", Name: "lookup_policy", Args: map[string]any{"topic": "returns"}}}},
			{Role: "tool", ToolResults: []target.ChatToolResult{{Ref: "1", Name: "lookup_policy", Output: map[string]any{"window_days": 30}}}},
		},
		Tools:       []target.ChatTool{{Name: "lookup_policy", Description: "policy", Parameters: map[string]any{"type": "object", "properties": map[string]any{"topic": map[string]any{"type": "string"}}}}, {Name: "end_session"}},
		Temperature: &temp,
		MaxTokens:   200,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Text, "the fake text model answers")
	assert.Greater(t, resp.InputTokens, 0)
	assert.Equal(t, "stop", resp.FinishReason)
	after := c.Usage()
	assert.Equal(t, before.Calls+1, after.Calls, "the call is counted like every model call")

	_, err = (&Client{cfg: Config{}}).Chat(context.Background(), target.ChatRequest{})
	assert.ErrorContains(t, err, "models.default")
}
