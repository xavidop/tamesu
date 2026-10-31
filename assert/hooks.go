package assert

import (
	"context"

	"github.com/xavidop/tamesu/target"
)

// JudgeRequest is what an LLM judge receives: the rubric and the whole run.
type JudgeRequest struct {
	Rubric string `json:"rubric"`
	// Scale is "binary" (pass or fail) or "1-5" (a score compared with Min).
	Scale string  `json:"scale,omitempty"`
	Min   float64 `json:"min,omitempty"`
	// Model overrides the configured judge model (a Genkit reference).
	Model      string             `json:"model,omitempty"`
	Transcript []Turn             `json:"transcript,omitempty"`
	ToolCalls  []target.ToolCall  `json:"tool_calls,omitempty"`
	Metrics    map[string]float64 `json:"metrics,omitempty"`
}

// JudgeVerdict is the judge's structured answer.
type JudgeVerdict struct {
	Pass     bool     `json:"pass"`
	Score    float64  `json:"score"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence,omitempty"`
	// TraceID and PromptVersion make judge drift traceable.
	TraceID       string `json:"trace_id,omitempty"`
	PromptVersion string `json:"prompt_version,omitempty"`
}

// Judger grades a run against a rubric. The llm module implements it; the
// runner sets it on every RunResult.
type Judger interface {
	Judge(ctx context.Context, req JudgeRequest) (JudgeVerdict, error)
}

// EvalRequest runs a registered Genkit evaluator on one example.
type EvalRequest struct {
	Evaluator string `json:"evaluator"`
	Input     any    `json:"input"`
	Output    any    `json:"output,omitempty"`
	Context   []any  `json:"context,omitempty"`
	Reference any    `json:"reference,omitempty"`
}

// EvalVerdict is an evaluator's score.
type EvalVerdict struct {
	Pass   bool    `json:"pass"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason,omitempty"`
}

// Evaluator runs Genkit evaluators by name.
type Evaluator interface {
	Evaluate(ctx context.Context, req EvalRequest) (EvalVerdict, error)
}
