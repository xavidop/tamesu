package model

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
	"github.com/xavidop/tamesu/targettest"
)

// scriptedChat answers from replies in order; when the newest user text
// contains "policy" it first asks for the lookup_policy tool, and when it
// contains "bye" it calls end_session with the final text.
type scriptedChat struct {
	mu      sync.Mutex
	replies []string
	i       int
	reqs    []target.ChatRequest
}

func (c *scriptedChat) Chat(_ context.Context, req target.ChatRequest) (target.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
	last := req.Messages[len(req.Messages)-1]
	lastUser := ""
	for _, m := range req.Messages {
		if m.Role == "user" {
			lastUser = m.Text
		}
	}
	if last.Role == "user" && strings.Contains(last.Text, "policy") {
		return target.ChatResponse{ToolCalls: []target.ChatToolCall{{Ref: "c1", Name: "lookup_policy", Args: map[string]any{"topic": "returns"}}}, InputTokens: 10, OutputTokens: 2}, nil
	}
	if strings.Contains(lastUser, "loop") {
		return target.ChatResponse{ToolCalls: []target.ChatToolCall{{Name: "unknown_tool"}}}, nil
	}
	if c.i >= len(c.replies) {
		return target.ChatResponse{}, errors.New("no reply left")
	}
	text := c.replies[c.i]
	c.i++
	resp := target.ChatResponse{Text: text, InputTokens: 20, OutputTokens: 5, FinishReason: "stop"}
	if c.i == len(c.replies) {
		// The last reply ends the session, as the conformance suite expects.
		resp.ToolCalls = []target.ChatToolCall{{Name: "end_session"}}
	}
	return resp, nil
}

func newTarget(chat *scriptedChat) (*Target, *target.RunContext) {
	t := New("prompt", Config{
		Model:  "fake/text",
		System: "You are the {{vars.brand}} support agent.",
		Tools: []Tool{
			{Name: "lookup_policy", Description: "The policy for a topic", Parameters: map[string]any{"type": "object", "properties": map[string]any{"topic": map[string]any{"type": "string"}}}, Result: map[string]any{"topic": "{{args.topic}}", "days": 30}},
			{Name: "no_result"},
		},
	})
	rc := &target.RunContext{TestName: "t", Chat: chat, Render: func(s string) (string, error) { return strings.ReplaceAll(s, "{{vars.brand}}", "Mochi"), nil }}
	return t, rc
}

func TestConformance(t *testing.T) {
	targettest.Run(t, targettest.Harness{New: func(t *testing.T, replies []string) target.Target {
		chat := &scriptedChat{replies: replies}
		return &conformanceTarget{Target: New("prompt", Config{System: "sys"}), chat: chat}
	}})
}

// conformanceTarget injects the chat model, which the harness's RunContext
// does not carry.
type conformanceTarget struct {
	*Target
	chat *scriptedChat
}

func (c *conformanceTarget) Start(ctx context.Context, run *target.RunContext) (target.Session, error) {
	run.Chat = c.chat
	return c.Target.Start(ctx, run)
}

func TestToolRoundsAndMetrics(t *testing.T) {
	chat := &scriptedChat{replies: []string{"Our window is 30 days.", "Bye!"}}
	tgt, rc := newTarget(chat)
	sess, err := tgt.Start(context.Background(), rc)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "What is the return policy?"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Our window is 30 days.", turn.Text)
	require.Len(t, turn.ToolCalls, 1)
	assert.Equal(t, "lookup_policy", turn.ToolCalls[0].Name)
	assert.False(t, turn.Done)
	assert.False(t, turn.StartedAt.IsZero())
	assert.False(t, turn.FirstTokenAt.Before(turn.StartedAt))

	// The second call carried the system prompt, the tools and the tool result.
	require.Len(t, chat.reqs, 2)
	assert.Equal(t, "You are the Mochi support agent.", chat.reqs[1].System)
	assert.Equal(t, "fake/text", chat.reqs[1].Model)
	assert.Len(t, chat.reqs[1].Tools, 2)
	msgs := chat.reqs[1].Messages
	require.Len(t, msgs, 3, "user, model (tool call), tool (result)")
	assert.Equal(t, "tool", msgs[2].Role)
	assert.Equal(t, map[string]any{"topic": "returns", "days": 30}, msgs[2].ToolResults[0].Output, "{{args.topic}} rendered")

	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "thanks, bye"}))
	turn, err = sess.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Bye!", turn.Text)
	assert.True(t, turn.Done, "end_session ends the session")
	assert.ErrorContains(t, sess.Send(ctx, target.UserTurn{Text: "more"}), "ended")

	arts, err := sess.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2.0, arts.Metrics["turns"])
	assert.Equal(t, 2.0, arts.Metrics["tool_calls"], "lookup_policy and end_session")
	assert.Equal(t, 50.0, arts.Metrics["agent_input_tokens"])
	assert.Equal(t, 12.0, arts.Metrics["agent_output_tokens"])
	require.NotEmpty(t, arts.Events)
	assert.Equal(t, "tool_call", arts.Events[0].Kind)
	assert.Contains(t, arts.Events[0].Detail, `lookup_policy {"topic":"returns"}`)
}

func TestUnknownToolAndRoundLimit(t *testing.T) {
	chat := &scriptedChat{}
	tgt, rc := newTarget(chat)
	tgt.cfg.MaxToolRounds = 2
	sess, err := tgt.Start(context.Background(), rc)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, sess.Send(ctx, target.UserTurn{Text: "loop forever"}))
	turn, err := sess.Receive(ctx)
	require.NoError(t, err, "a model that keeps calling tools is stopped, not an error")
	assert.Len(t, turn.ToolCalls, 2)
	assert.Equal(t, "", turn.Text)
	last := chat.reqs[len(chat.reqs)-1].Messages
	assert.Equal(t, map[string]any{"error": "unknown tool unknown_tool"}, last[len(last)-1].ToolResults[0].Output)
	arts, _ := sess.Close(ctx)
	kinds := []string{}
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind)
	}
	assert.Contains(t, kinds, "tool_rounds")

	_, err = New("p", Config{}).Start(context.Background(), &target.RunContext{})
	assert.ErrorIs(t, err, ErrNoModel)
	_, err = sess.Receive(ctx)
	assert.ErrorContains(t, err, "without a Send")
}
