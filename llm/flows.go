package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/firebase/genkit/go/core/tracing"
	"github.com/firebase/genkit/go/genkit"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
)

// Flow names, as they appear in the Genkit Developer UI.
const (
	FlowSimulateUserTurn = "simulateUserTurn"
	FlowJudgeRun         = "judgeRun"
	FlowGroundRun        = "groundRun"
)

// defineFlows registers tamesu's flows and the judge evaluator on the
// Genkit instance.
func (c *Client) defineFlows() {
	c.simFlow = genkit.DefineFlow(c.g, FlowSimulateUserTurn, c.simulate)
	c.judgeFlow = genkit.DefineFlow(c.g, FlowJudgeRun, c.judge)
	c.groundFlow = genkit.DefineFlow(c.g, FlowGroundRun, c.ground)
	c.ttsFlow = genkit.DefineFlow(c.g, FlowSynthesize, c.synthesize)
	c.sttFlow = genkit.DefineFlow(c.g, FlowTranscribe, c.transcribe)
	c.defineBrowser()
	c.generateFlow = genkit.DefineFlow(c.g, FlowGenerateScenarios, c.generateScenarios)
	c.defineJudgeEvaluator()
}

// Simulate produces the simulated user's next turn through the
// simulateUserTurn flow.
func (c *Client) Simulate(ctx context.Context, in model.SimInput) (model.SimOutput, error) {
	if c.overBudget() {
		return model.SimOutput{}, ErrBudgetExceeded
	}
	return c.simFlow.Run(ctx, in)
}

func (c *Client) simulate(ctx context.Context, in model.SimInput) (model.SimOutput, error) {
	input := map[string]any{
		"persona":   describePersona(in),
		"goal":      in.Scenario.Goal,
		"stop_when": in.Scenario.StopWhen,
		"turn":      in.Turn,
		"max_turns": in.MaxTurns,
		"audio":     in.Mode == "audio",
		"history":   formatHistory(in.History),
		"seed":      in.Seed,
	}
	// An adversarial persona runs the attacker prompt, which adds the tactic
	// it used to every turn.
	promptName := "simulator"
	if in.Persona.Adversarial {
		promptName = "adversary"
		tactics := in.Persona.Tactics
		if len(tactics) == 0 {
			tactics = config.AdversarialTactics
		}
		input["tactics"] = "- " + strings.Join(tactics, "\n- ")
	}
	models := chain(c.modelFor("simulator", ""), c.fallbacksFor("simulator"))
	return attempt(ctx, c, "simulator", models, func(ctx context.Context, modelName string) (model.SimOutput, error) {
		resp, err := c.executePrompt(ctx, promptName, input, modelName)
		budgetErr := c.record(modelName, resp, err)
		if err != nil {
			return model.SimOutput{}, fmt.Errorf("llm: %s with %s: %w", FlowSimulateUserTurn, modelName, err)
		}
		var out model.SimOutput
		if err := resp.Output(&out); err != nil {
			return model.SimOutput{}, badOutput(FlowSimulateUserTurn, modelName, err, resp.Text())
		}
		out.TraceID = tracing.SpanTraceInfo(ctx).TraceID
		return out, budgetErr
	})
}

// Judge grades a run through the judgeRun flow.
func (c *Client) Judge(ctx context.Context, req assert.JudgeRequest) (assert.JudgeVerdict, error) {
	if c.overBudget() {
		return assert.JudgeVerdict{}, ErrBudgetExceeded
	}
	return c.judgeFlow.Run(ctx, req)
}

func (c *Client) judge(ctx context.Context, req assert.JudgeRequest) (assert.JudgeVerdict, error) {
	scale := req.Scale
	if scale == "" {
		scale = "binary"
	}
	input := map[string]any{
		"rubric":     req.Rubric,
		"scale":      scale,
		"min":        req.Min,
		"transcript": formatHistory(req.Transcript),
		"tool_calls": formatToolCalls(req),
		"metrics":    formatMetrics(req.Metrics),
		"language":   req.Language,
	}
	models := chain(c.modelFor("judge", req.Model), c.fallbacksFor("judge"))
	return attempt(ctx, c, "judge", models, func(ctx context.Context, modelName string) (assert.JudgeVerdict, error) {
		resp, err := c.executePrompt(ctx, "judge", input, modelName)
		budgetErr := c.record(modelName, resp, err)
		if err != nil {
			return assert.JudgeVerdict{}, fmt.Errorf("llm: %s with %s: %w", FlowJudgeRun, modelName, err)
		}
		var out assert.JudgeVerdict
		if err := resp.Output(&out); err != nil {
			return assert.JudgeVerdict{}, badOutput(FlowJudgeRun, modelName, err, resp.Text())
		}
		out.TraceID = tracing.SpanTraceInfo(ctx).TraceID
		out.PromptVersion = c.prompts.Version("judge")
		return out, budgetErr
	})
}

// Ground lists the agent's claims and whether the context supports each,
// through the groundRun flow, with the judge model.
func (c *Client) Ground(ctx context.Context, req assert.GroundRequest) (assert.GroundVerdict, error) {
	if c.overBudget() {
		return assert.GroundVerdict{}, ErrBudgetExceeded
	}
	return c.groundFlow.Run(ctx, req)
}

func (c *Client) ground(ctx context.Context, req assert.GroundRequest) (assert.GroundVerdict, error) {
	input := map[string]any{
		"context":    req.Context,
		"transcript": formatHistory(req.Transcript),
		"language":   req.Language,
	}
	models := chain(c.modelFor("judge", req.Model), c.fallbacksFor("judge"))
	return attempt(ctx, c, "judge", models, func(ctx context.Context, modelName string) (assert.GroundVerdict, error) {
		resp, err := c.executePrompt(ctx, "ground", input, modelName)
		budgetErr := c.record(modelName, resp, err)
		if err != nil {
			return assert.GroundVerdict{}, fmt.Errorf("llm: %s with %s: %w", FlowGroundRun, modelName, err)
		}
		var out assert.GroundVerdict
		if err := resp.Output(&out); err != nil {
			return assert.GroundVerdict{}, badOutput(FlowGroundRun, modelName, err, resp.Text())
		}
		out.TraceID = tracing.SpanTraceInfo(ctx).TraceID
		out.PromptVersion = c.prompts.Version("ground")
		return out, budgetErr
	})
}

func describePersona(in model.SimInput) string {
	p := in.Persona
	var b strings.Builder
	if p.Name != "" {
		fmt.Fprintf(&b, "- Name: %s\n", p.Name)
	}
	if p.Style != "" {
		fmt.Fprintf(&b, "- Style: %s\n", p.Style)
	}
	if p.Language != "" {
		fmt.Fprintf(&b, "- Language: %s (speak only this language)\n", p.Language)
	}
	if b.Len() == 0 {
		b.WriteString("- An ordinary customer, polite and direct.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatHistory(turns []assert.Turn) string {
	if len(turns) == 0 {
		return "(nothing yet)"
	}
	var b strings.Builder
	for _, t := range turns {
		fmt.Fprintf(&b, "%s: %s\n", t.Role, strings.TrimSpace(t.Text))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatToolCalls(req assert.JudgeRequest) string {
	if len(req.ToolCalls) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, tc := range req.ToolCalls {
		args, _ := json.Marshal(tc.Args)
		fmt.Fprintf(&b, "- %s %s\n", tc.Name, args)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatMetrics(m map[string]float64) string {
	if len(m) == 0 {
		return "(none)"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s: %g\n", k, m[k])
	}
	return strings.TrimRight(b.String(), "\n")
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		return s[:197] + "..."
	}
	return s
}
