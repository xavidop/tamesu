package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/tracing"
	"github.com/firebase/genkit/go/genkit"

	"github.com/xavidop/tamesu/internal/model"
)

// FlowGenerateScenarios drafts tests from a document.
const FlowGenerateScenarios = "generateScenarios"

// DefaultGenerateCount is how many tests are drafted when none is asked.
const DefaultGenerateCount = 5

var _ model.Generator = (*Client)(nil)

type generateInput struct {
	Source     string `json:"source"`
	SourceName string `json:"source_name,omitempty"`
	Target     string `json:"target"`
	Type       string `json:"type"`
	Count      int    `json:"count"`
	Model      string `json:"model,omitempty"`
}

// GenerateTests implements model.Generator through the generateScenarios flow.
func (c *Client) GenerateTests(ctx context.Context, req model.GenerateRequest) (model.Draft, error) {
	if c.overBudget() {
		return model.Draft{}, ErrBudgetExceeded
	}
	return c.generateFlow.Run(ctx, generateInput{Source: req.Source, SourceName: req.SourceName, Target: req.Target, Type: req.Type, Count: req.Count, Model: req.Model})
}

func (c *Client) generateScenarios(ctx context.Context, in generateInput) (model.Draft, error) {
	if strings.TrimSpace(in.Source) == "" {
		return model.Draft{}, fmt.Errorf("llm: %s: the document is empty", FlowGenerateScenarios)
	}
	modelName := c.modelFor("scenario_generator", in.Model)
	prompt := genkit.LookupPrompt(c.g, "scenario_gen")
	if prompt == nil {
		return model.Draft{}, fmt.Errorf("llm: prompt scenario_gen is not loaded")
	}
	count := in.Count
	if count <= 0 {
		count = DefaultGenerateCount
	}
	typ := in.Type
	if typ == "" {
		typ = "rest"
	}
	source := in.Source
	if len(source) > 60000 {
		source = source[:60000] + "\n[truncated]"
	}
	input := map[string]any{
		"source": source, "source_name": in.SourceName, "target": in.Target, "type": typ, "count": count,
		"conversational": typ != "browser", "open": "{{", "close": "}}",
	}
	resp, err := prompt.Execute(ctx, ai.WithInput(input), ai.WithModelName(modelName))
	budgetErr := c.record(modelName, resp)
	if err != nil {
		return model.Draft{}, fmt.Errorf("llm: %s with %s: %w", FlowGenerateScenarios, modelName, err)
	}
	var out model.Draft
	if err := resp.Output(&out); err != nil {
		return model.Draft{}, fmt.Errorf("llm: %s: the model did not return the expected JSON: %w (text: %s)", FlowGenerateScenarios, err, short(resp.Text()))
	}
	out.TraceID = tracing.SpanTraceInfo(ctx).TraceID
	if budgetErr != nil {
		return out, budgetErr
	}
	return out, nil
}
