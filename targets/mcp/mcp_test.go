package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

// scriptedChat is the model: it calls lookup_order when the user mentions an
// order, then speaks what the tool said.
type scriptedChat struct {
	mu   sync.Mutex
	reqs []target.ChatRequest
}

func (c *scriptedChat) Chat(_ context.Context, req target.ChatRequest) (target.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
	last := req.Messages[len(req.Messages)-1]
	switch {
	case last.Role == "user" && strings.Contains(last.Text, "order"):
		return target.ChatResponse{ToolCalls: []target.ChatToolCall{{Ref: "c1", Name: "lookup_order", Args: map[string]any{"id": "ORD-7"}}}}, nil
	case last.Role == "user" && strings.Contains(last.Text, "explode"):
		return target.ChatResponse{ToolCalls: []target.ChatToolCall{{Ref: "c2", Name: "explode", Args: map[string]any{}}}}, nil
	case last.Role == "tool":
		return target.ChatResponse{Text: fmt.Sprintf("The tool said: %v", last.ToolResults[0].Output), FinishReason: "stop"}, nil
	}
	return target.ChatResponse{Text: "Hello!", FinishReason: "stop"}, nil
}

func fakeServer() *server.MCPServer {
	s := server.NewMCPServer("shop", "1.0.0")
	s.AddTool(mcpgo.NewTool("lookup_order", mcpgo.WithDescription("An order by id"), mcpgo.WithString("id", mcpgo.Required(), mcpgo.Description("The order id"))),
		func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			id, _ := req.GetArguments()["id"].(string)
			return mcpgo.NewToolResultText(fmt.Sprintf(`{"id":%q,"status":"shipped"}`, id)), nil
		})
	s.AddTool(mcpgo.NewTool("explode", mcpgo.WithDescription("Always fails")),
		func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultError("the warehouse is on fire"), nil
		})
	s.AddTool(mcpgo.NewTool("secret", mcpgo.WithDescription("Not for the model")),
		func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("42"), nil
		})
	return s
}

func newTarget(t *testing.T, cfg Config) (*Target, *target.RunContext) {
	t.Helper()
	srv := fakeServer()
	cfg.Connect = func(context.Context) (*mcpclient.Client, error) { return mcpclient.NewInProcessClient(srv) }
	return New("shop", cfg), &target.RunContext{TestName: t.Name(), Chat: &scriptedChat{}}
}

func TestToolsAreListedAndExecuted(t *testing.T) {
	tgt, rc := newTarget(t, Config{Tools: []string{"lookup_order", "explode"}})
	sess, err := tgt.Start(context.Background(), rc)
	require.NoError(t, err)
	assert.Equal(t, []string{"explode", "lookup_order"}, sess.(*session).ToolNames(), "the server's tools, restricted and sorted")

	chat := rc.Chat.(*scriptedChat)
	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "Where is my order?"}))
	turn, err := sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "The tool said: map[id:ORD-7 status:shipped]", turn.Text, "the model read the server's JSON as an object")
	require.Len(t, turn.ToolCalls, 1)
	assert.Equal(t, "lookup_order", turn.ToolCalls[0].Name)
	assert.Equal(t, map[string]any{"id": "ORD-7", "status": "shipped"}, turn.ToolCalls[0].Result, "the result is kept on the call")

	chat.mu.Lock()
	first := chat.reqs[0]
	chat.mu.Unlock()
	require.Len(t, first.Tools, 2)
	assert.Equal(t, DefaultSystem, first.System)
	var lookup target.ChatTool
	for _, tl := range first.Tools {
		if tl.Name == "lookup_order" {
			lookup = tl
		}
	}
	assert.Equal(t, "An order by id", lookup.Description)
	assert.Equal(t, "object", lookup.Parameters["type"])
	assert.Contains(t, lookup.Parameters["properties"], "id", "the server's input schema is the tool's parameters")

	require.NoError(t, sess.Send(context.Background(), target.UserTurn{Text: "please explode"}))
	turn, err = sess.Receive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "The tool said: map[error:explode: the warehouse is on fire]", turn.Text, "a server error reaches the model as an error result")

	arts, err := sess.Close(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2.0, arts.Metrics["mcp_tools"])
	var kinds []string
	for _, e := range arts.Events {
		kinds = append(kinds, e.Kind+": "+e.Detail)
	}
	assert.Contains(t, kinds, "mcp_tools: explode, lookup_order")
	assert.Contains(t, kinds, "tool_error: explode: explode: the warehouse is on fire")
}

func TestStartErrors(t *testing.T) {
	tgt, rc := newTarget(t, Config{Tools: []string{"nope"}})
	_, err := tgt.Start(context.Background(), rc)
	assert.ErrorContains(t, err, "offers no tools named nope")

	noModel := New("shop", Config{URL: "http://127.0.0.1:1/mcp"})
	_, err = noModel.Start(context.Background(), &target.RunContext{})
	assert.ErrorContains(t, err, "needs a model provider")

	bare := New("shop", Config{})
	_, err = bare.Start(context.Background(), &target.RunContext{Chat: &scriptedChat{}})
	assert.ErrorContains(t, err, "url or command is required")

	failing := New("shop", Config{Connect: func(context.Context) (*mcpclient.Client, error) { return nil, errors.New("refused") }})
	_, err = failing.Start(context.Background(), &target.RunContext{Chat: &scriptedChat{}})
	assert.ErrorContains(t, err, "connect: refused")
}
