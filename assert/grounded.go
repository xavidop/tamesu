package assert

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ohler55/ojg/jp"
)

// GroundRequest asks whether the agent's claims are supported by the context
// it was given (retrieved passages, tool results).
type GroundRequest struct {
	// Context are the passages the agent could legitimately draw on.
	Context []string `json:"context"`
	// Model overrides the configured judge model (a Genkit reference).
	Model string `json:"model,omitempty"`
	// Language is the conversation's language (BCP 47), when the persona has one.
	Language   string `json:"language,omitempty"`
	Transcript []Turn `json:"transcript"`
}

// Claim is one factual statement the agent made.
type Claim struct {
	Text      string `json:"claim"`
	Supported bool   `json:"supported"`
	// Evidence quotes the context that supports the claim, or says what is
	// missing.
	Evidence string `json:"evidence,omitempty"`
}

// GroundVerdict is the grounding check's structured answer.
type GroundVerdict struct {
	Claims []Claim `json:"claims"`
	Reason string  `json:"reason"`
	// TraceID and PromptVersion identify the model call.
	TraceID       string `json:"trace_id,omitempty"`
	PromptVersion string `json:"prompt_version,omitempty"`
}

// Supported counts the supported claims and the total.
func (v GroundVerdict) Supported() (supported, total int) {
	for _, c := range v.Claims {
		if c.Supported {
			supported++
		}
	}
	return supported, len(v.Claims)
}

// Grounder checks the agent's claims against context.
type Grounder interface {
	Ground(ctx context.Context, req GroundRequest) (GroundVerdict, error)
}

// GroundContext says where the retrieval context of a run is.
type GroundContext struct {
	// JSONPath selects passages from every raw response (rest / websocket /
	// realtime events): strings, or lists and objects rendered as JSON.
	JSONPath string
	// Tool names a tool whose results are the context (model / realtime /
	// pipecat targets, where tamesu sees the results).
	Tool string
	// Text are passages given in the suite.
	Text []string
}

// GroundedCheck is the grounding assertion: every factual claim the agent
// makes must be supported by the context, or at least Min of them.
type GroundedCheck struct {
	Context GroundContext
	// Min is the fraction of claims that must be supported; 0 means all.
	Min   float64
	Model string
}

// Grounded builds the assertion for a GroundedCheck.
func Grounded(c GroundedCheck) (Assertion, error) {
	var expr jp.Expr
	if c.Context.JSONPath != "" {
		var err error
		if expr, err = jp.ParseString(c.Context.JSONPath); err != nil {
			return nil, fmt.Errorf("grounded json_path %q: %w", c.Context.JSONPath, err)
		}
	}
	if c.Min < 0 || c.Min > 1 {
		return nil, fmt.Errorf("grounded min must be between 0 and 1")
	}
	name := "grounded"
	switch {
	case c.Context.Tool != "":
		name += " in " + c.Context.Tool
	case c.Context.JSONPath != "":
		name += " in " + c.Context.JSONPath
	}
	return Func(name, func(ctx context.Context, r *RunResult) Result {
		passages := append([]string(nil), c.Context.Text...)
		if expr != nil {
			for _, resp := range r.Responses {
				for _, v := range expr.Get(resp) {
					passages = append(passages, passage(v)...)
				}
			}
		}
		if c.Context.Tool != "" {
			for _, call := range r.ToolCalls {
				if call.Name == c.Context.Tool && call.Result != nil {
					passages = append(passages, passage(call.Result)...)
				}
			}
		}
		if len(passages) == 0 {
			return Result{Pass: false, Reason: "no context to ground the answer in: " + describeContext(c.Context)}
		}
		if r.AgentText() == "" {
			return Result{Pass: false, Reason: "the agent said nothing to check"}
		}
		if r.Grounder == nil {
			return Result{Pass: false, Reason: "no judge model is configured for the grounding check"}
		}
		v, err := r.Grounder.Ground(ctx, GroundRequest{Context: passages, Model: c.Model, Language: r.Language, Transcript: r.Transcript})
		if err != nil {
			return Result{Pass: false, Reason: "grounding check: " + err.Error()}
		}
		supported, total := v.Supported()
		var evidence []string
		for _, cl := range v.Claims {
			mark := "✓"
			if !cl.Supported {
				mark = "✗"
			}
			line := mark + " " + cl.Text
			if cl.Evidence != "" {
				line += " — " + cl.Evidence
			}
			evidence = append(evidence, line)
		}
		if total == 0 {
			return Result{Pass: true, Score: 1, Reason: "the agent made no factual claims", TraceID: v.TraceID}
		}
		rate := float64(supported) / float64(total)
		min := c.Min
		if min == 0 {
			min = 1
		}
		reason := fmt.Sprintf("%d of %d claims are supported by the context", supported, total)
		if v.Reason != "" {
			reason += ": " + v.Reason
		}
		return Result{Pass: rate >= min, Score: rate, Reason: reason, Evidence: evidence, TraceID: v.TraceID}
	}), nil
}

// passage renders a context value as passages: a string as is, a list one
// per element, anything else as JSON.
func passage(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, passage(e)...)
		}
		return out
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []string{fmt.Sprint(v)}
	}
	return []string{string(b)}
}

func describeContext(c GroundContext) string {
	var parts []string
	if c.Tool != "" {
		parts = append(parts, fmt.Sprintf("the tool %s was never called or returned nothing", c.Tool))
	}
	if c.JSONPath != "" {
		parts = append(parts, fmt.Sprintf("%s matched nothing in the responses", c.JSONPath))
	}
	if len(parts) == 0 {
		return "no text, tool or json_path context was given"
	}
	return strings.Join(parts, "; ")
}
