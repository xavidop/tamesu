package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"

	"github.com/xavidop/tamesu/assert"
)

// JudgeEvaluator is the name of tamesu's judge as a Genkit evaluator. Its
// options are {"rubric": "...", "scale": "binary|1-5", "min": n}; the example's
// Output is the transcript (a string or []assert.Turn) and Reference may hold
// the rubric instead.
const JudgeEvaluator = "tamesu/judge"

// defineJudgeEvaluator registers the judge so any Genkit tooling (and the
// evaluator: assertion) can use it.
func (c *Client) defineJudgeEvaluator() {
	genkit.DefineEvaluatorAction[any](c.g, JudgeEvaluator, &ai.EvaluatorOptions{
		DisplayName: "tamesu judge",
		Definition:  "Grades a conversation transcript against a rubric with structured output (pass, score, reason, evidence).",
		IsBilled:    true,
	}, func(ctx context.Context, req *ai.EvaluatorCallbackRequest, _ any) (*ai.EvaluatorCallbackResponse, error) {
		jr := assert.JudgeRequest{Scale: "binary"}
		if opts, ok := req.Options.(map[string]any); ok {
			if r, ok := opts["rubric"].(string); ok {
				jr.Rubric = r
			}
			if s, ok := opts["scale"].(string); ok {
				jr.Scale = s
			}
			if m, ok := opts["min"].(float64); ok {
				jr.Min = m
			}
		}
		if jr.Rubric == "" {
			if r, ok := req.Input.Reference.(string); ok {
				jr.Rubric = r
			}
		}
		if jr.Rubric == "" {
			return nil, fmt.Errorf("%s: a rubric is needed (options.rubric or the example's reference)", JudgeEvaluator)
		}
		jr.Transcript = turnsOf(req.Input.Input, req.Input.Output)
		v, err := c.Judge(ctx, jr)
		if err != nil {
			return nil, err
		}
		status := "FAIL"
		if v.Pass {
			status = "PASS"
		}
		return &ai.EvaluationResult{TestCaseId: req.Input.TestCaseId, Evaluation: []ai.Score{{
			Id: "judge", Score: v.Score, Status: status,
			Details: map[string]any{"reasoning": v.Reason, "evidence": v.Evidence, "prompt_version": v.PromptVersion},
		}}}, nil
	})
}

// turnsOf builds a transcript from an evaluator example: Output as the
// agent's text (or a list of turns) and Input as the user's question.
func turnsOf(input, output any) []assert.Turn {
	if turns, ok := output.([]assert.Turn); ok {
		return turns
	}
	var out []assert.Turn
	if s, ok := input.(string); ok && s != "" {
		out = append(out, assert.Turn{Role: "user", Text: s})
	}
	switch o := output.(type) {
	case string:
		out = append(out, assert.Turn{Role: "agent", Text: o, Index: 1})
	case []any:
		for i, t := range o {
			if m, ok := t.(map[string]any); ok {
				role, _ := m["role"].(string)
				text, _ := m["text"].(string)
				out = append(out, assert.Turn{Role: role, Text: text, Index: i + 1})
			}
		}
	default:
		if o != nil {
			out = append(out, assert.Turn{Role: "agent", Text: fmt.Sprint(o), Index: 1})
		}
	}
	return out
}

// Evaluate runs a registered Genkit evaluator (tamesu/judge, the evaluators
// plugin's genkitEval/deep_equal, genkitEval/regex, genkitEval/jsonata, or
// any the caller's Genkit app defines) on one example.
func (c *Client) Evaluate(ctx context.Context, req assert.EvalRequest) (assert.EvalVerdict, error) {
	if c.overBudget() {
		return assert.EvalVerdict{}, ErrBudgetExceeded
	}
	name := req.Evaluator
	if !strings.Contains(name, "/") {
		name = "genkitEval/" + name
	}
	if genkit.LookupEvaluator(c.g, name) == nil {
		return assert.EvalVerdict{}, fmt.Errorf("llm: evaluator %q is not registered (is its plugin in genkit.plugins?)", req.Evaluator)
	}
	resp, err := genkit.Evaluate(ctx, c.g, ai.WithEvaluatorName(name), ai.WithDataset(&ai.Example{
		TestCaseId: "tamesu", Input: req.Input, Output: req.Output, Context: req.Context, Reference: req.Reference,
	}))
	if err != nil {
		return assert.EvalVerdict{}, fmt.Errorf("llm: evaluator %s: %w", req.Evaluator, err)
	}
	if resp == nil || len(*resp) == 0 || len((*resp)[0].Evaluation) == 0 {
		return assert.EvalVerdict{}, fmt.Errorf("llm: evaluator %s returned no score", req.Evaluator)
	}
	s := (*resp)[0].Evaluation[0]
	v := assert.EvalVerdict{Pass: s.Status == "PASS"}
	switch sc := s.Score.(type) {
	case float64:
		v.Score = sc
	case int:
		v.Score = float64(sc)
	case bool:
		if sc {
			v.Score = 1
		}
		v.Pass = v.Pass || sc
	}
	if s.Status == "" && v.Score > 0 {
		v.Pass = true
	}
	if r, ok := s.Details["reasoning"].(string); ok {
		v.Reason = r
	}
	if s.Error != "" {
		v.Reason = s.Error
	}
	return v, nil
}
