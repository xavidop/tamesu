package model

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

// TestToolFuzz: a fuzzed tool answers the fuzz instead of its configured
// result, the run records a tool_fuzz event, and a delay holds the answer.
func TestToolFuzz(t *testing.T) {
	chat := &scriptedChat{replies: []string{"I'm sorry, I can't check the policy right now.", "Bye"}}
	tgt, rc := newTarget(chat)
	rc.ToolFuzz = map[string]target.ToolFuzz{"lookup_policy": {Mode: target.FuzzError, Delay: 30 * time.Millisecond}}
	sess, err := tgt.Start(context.Background(), rc)
	require.NoError(t, err)

	start := time.Now()
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "What is the return policy?"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond, "the answer waited for the delay")
	require.Len(t, turn.ToolCalls, 1)
	assert.Equal(t, "lookup_policy", turn.ToolCalls[0].Name)

	// The model saw the fuzzed result, not the configured one.
	var toolMsgs []target.ChatMessage
	for _, req := range chat.reqs {
		for _, m := range req.Messages {
			if m.Role == "tool" {
				toolMsgs = append(toolMsgs, m)
			}
		}
	}
	require.NotEmpty(t, toolMsgs)
	res := toolMsgs[len(toolMsgs)-1].ToolResults
	require.Len(t, res, 1)
	assert.Equal(t, map[string]any{"error": "internal error: the service is unavailable", "code": 503}, res[0].Output)

	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	var fuzz []string
	for _, e := range arts.Events {
		if e.Kind == target.EventToolFuzz {
			fuzz = append(fuzz, e.Detail)
		}
	}
	assert.Equal(t, []string{"lookup_policy: error after 30ms"}, fuzz)
}

// TestToolFuzzWithoutResult: a fuzzed tool that has no configured result is
// answered anyway, so the agent's reaction can be tested.
func TestToolFuzzWithoutResult(t *testing.T) {
	chat := &scriptedChat{replies: []string{"Done.", "Bye"}}
	tgt, rc := newTarget(chat)
	rc.ToolFuzz = map[string]target.ToolFuzz{"lookup_policy": {Mode: target.FuzzCustom, Result: "nothing here"}}
	sess, err := tgt.Start(context.Background(), rc)
	require.NoError(t, err)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "policy please"}))
	_, err = sess.Receive(context.Background())
	require.NoError(t, err)
	var outputs []any
	for _, req := range chat.reqs {
		for _, m := range req.Messages {
			for _, r := range m.ToolResults {
				outputs = append(outputs, r.Output)
			}
		}
	}
	assert.Contains(t, outputs, "nothing here")
}
