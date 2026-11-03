package target

import "context"

// ChatTool is a tool definition offered to the model: a name, what it does
// and a JSON schema of its parameters.
type ChatTool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ChatToolCall is a tool the model asked to call; Ref ties the result back
// to the request when the provider uses call ids.
type ChatToolCall struct {
	Ref  string
	Name string
	Args map[string]any
}

// ChatToolResult answers a ChatToolCall.
type ChatToolResult struct {
	Ref    string
	Name   string
	Output any
}

// ChatMessage is one message of a chat: the user, the model (text and/or
// tool calls) or the tool results that answer the model.
type ChatMessage struct {
	// Role is user, model or tool.
	Role        string
	Text        string
	ToolCalls   []ChatToolCall
	ToolResults []ChatToolResult
}

// ChatRequest is one model call of the model target: the whole conversation
// so far, with the system prompt and the tools.
type ChatRequest struct {
	// Model is a Genkit model reference; "" uses the configured default.
	Model       string
	System      string
	Messages    []ChatMessage
	Tools       []ChatTool
	Temperature *float64
	MaxTokens   int
}

// ChatResponse is the model's answer: text, tool calls it wants made, or
// both, with the tokens the call used.
type ChatResponse struct {
	Text         string
	ToolCalls    []ChatToolCall
	InputTokens  int
	OutputTokens int
	FinishReason string
}

// ChatModel is a chat completion with tools, for the model target: the
// system prompt under test answers the simulated user directly, with no
// agent server in between. The llm module implements it; the runner hands
// it to targets through RunContext.Chat.
type ChatModel interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
