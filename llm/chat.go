package llm

import (
	"context"
	"fmt"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"

	"github.com/xavidop/tamesu/target"
)

// Chat implements target.ChatModel: one model call for the model target
// with the system prompt, the conversation and the tool definitions. Tool
// calls come back as requests, never executed here: the target answers them
// from its configured results and calls again. The call is priced and
// counted like every other model call of the run.
func (c *Client) Chat(ctx context.Context, req target.ChatRequest) (target.ChatResponse, error) {
	modelName := req.Model
	if modelName == "" {
		modelName = c.cfg.Models.Default
	}
	if modelName == "" {
		return target.ChatResponse{}, fmt.Errorf("model target: no model given and models.default is not set")
	}
	opts := []ai.GenerateOption{ai.WithModelName(modelName), ai.WithReturnToolRequests(true)}
	if req.System != "" {
		opts = append(opts, ai.WithSystem(req.System))
	}
	msgs := make([]*ai.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			msgs = append(msgs, ai.NewUserTextMessage(m.Text))
		case "model":
			var parts []*ai.Part
			if m.Text != "" {
				parts = append(parts, ai.NewTextPart(m.Text))
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, ai.NewToolRequestPart(&ai.ToolRequest{Ref: tc.Ref, Name: tc.Name, Input: tc.Args}))
			}
			msgs = append(msgs, ai.NewModelMessage(parts...))
		case "tool":
			var parts []*ai.Part
			for _, tr := range m.ToolResults {
				parts = append(parts, ai.NewToolResponsePart(&ai.ToolResponse{Ref: tr.Ref, Name: tr.Name, Output: tr.Output}))
			}
			msgs = append(msgs, ai.NewMessage(ai.RoleTool, nil, parts...))
		}
	}
	if len(msgs) > 0 {
		opts = append(opts, ai.WithMessages(msgs...))
	}
	if len(req.Tools) > 0 {
		refs := make([]ai.ToolRef, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			// Never run: WithReturnToolRequests hands the requests back.
			refs = append(refs, ai.NewTool(t.Name, t.Description, func(*ai.ToolContext, any) (any, error) { return nil, nil }, ai.WithInputSchema(schema)))
		}
		opts = append(opts, ai.WithTools(refs...))
	}
	if req.Temperature != nil || req.MaxTokens > 0 {
		cfg := &ai.GenerationCommonConfig{}
		if req.Temperature != nil {
			cfg.Temperature = *req.Temperature
		}
		if req.MaxTokens > 0 {
			cfg.MaxOutputTokens = req.MaxTokens
		}
		opts = append(opts, ai.WithConfig(cfg))
	}
	resp, err := genkit.Generate(ctx, c.g, opts...)
	if err != nil {
		return target.ChatResponse{}, err
	}
	budgetErr := c.record(modelName, resp)
	out := target.ChatResponse{Text: resp.Text(), FinishReason: string(resp.FinishReason)}
	if u := resp.Usage; u != nil {
		out.InputTokens, out.OutputTokens = u.InputTokens, u.OutputTokens
	}
	for _, p := range resp.ToolRequests() {
		tr := p.ToolRequest
		if tr == nil {
			continue
		}
		args, _ := tr.Input.(map[string]any)
		out.ToolCalls = append(out.ToolCalls, target.ChatToolCall{Ref: tr.Ref, Name: tr.Name, Args: args})
	}
	return out, budgetErr
}
