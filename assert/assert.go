package assert

import (
	"context"
	"time"

	"github.com/xavidop/tamesu/target"
)

// Assertion is one check over a finished run.
type Assertion interface {
	Name() string
	Evaluate(ctx context.Context, r *RunResult) Result
}

// Result is the outcome of one assertion.
type Result struct {
	Name string `json:"name"`
	Pass bool   `json:"pass"`
	// Score is the judge's score when the assertion has one (0 to 1 or 1 to
	// 5); zero otherwise.
	Score float64 `json:"score,omitempty"`
	// Reason explains the outcome in one or two sentences.
	Reason string `json:"reason"`
	// Evidence quotes the turns or values the outcome rests on.
	Evidence []string `json:"evidence,omitempty"`
	// TraceID is the Genkit trace of a judge or evaluator call.
	TraceID string `json:"trace_id,omitempty"`
}

// RunResult is everything one run produced, the input of every assertion.
type RunResult struct {
	TestName   string
	Target     string
	Transcript []Turn
	ToolCalls  []target.ToolCall
	Metrics    map[string]float64
	Artifacts  target.Artifacts
	// Status lists the HTTP status of every agent turn (rest).
	Status []int
	// Responses holds the raw payload of every agent turn (the JSON body for
	// rest), in order, for json_path.
	Responses []any
	Events    []target.Event
	// Judger and Evaluator are the AI hooks the runner sets; nil means no
	// model is available and judge or evaluator assertions fail.
	Judger    Judger
	Evaluator Evaluator
}

// Turn is one line of the transcript.
type Turn struct {
	// Role is "user" or "agent".
	Role string    `json:"role"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	// Index is the 1-based agent-turn number for agent turns, 0 for user turns.
	Index int `json:"index,omitempty"`
}

// AgentText joins the agent's turns, newline separated: what contains, regex
// and the judges read.
func (r *RunResult) AgentText() string {
	out := ""
	for _, t := range r.Transcript {
		if t.Role == "agent" {
			if out != "" {
				out += "\n"
			}
			out += t.Text
		}
	}
	return out
}

// AgentTurns counts the agent's turns.
func (r *RunResult) AgentTurns() int {
	n := 0
	for _, t := range r.Transcript {
		if t.Role == "agent" {
			n++
		}
	}
	return n
}

// Turn returns a view of the run restricted to one agent turn (1-based; 0 or
// a number past the last turn means the last turn): its transcript holds the
// user turn that caused it and the agent turn, and Status and Responses hold
// that turn's entries. The metrics, tool calls and hooks are shared.
func (r *RunResult) Turn(n int) *RunResult {
	agentIdx := -1
	count := 0
	for i, t := range r.Transcript {
		if t.Role == "agent" {
			count++
			agentIdx = i
			if count == n {
				break
			}
		}
	}
	view := *r
	view.Transcript = nil
	view.Status = nil
	view.Responses = nil
	if agentIdx < 0 {
		return &view
	}
	start := agentIdx
	if start > 0 && r.Transcript[start-1].Role == "user" {
		start--
	}
	view.Transcript = r.Transcript[start : agentIdx+1]
	if count-1 < len(r.Status) {
		view.Status = r.Status[count-1 : count]
	}
	if count-1 < len(r.Responses) {
		view.Responses = r.Responses[count-1 : count]
	}
	return &view
}
