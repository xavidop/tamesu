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
	Name string
	Pass bool
	// Score is the judge's score when the assertion has one (0 to 1 or 1 to
	// 5); zero otherwise.
	Score float64
	// Reason explains the outcome in one or two sentences.
	Reason string
	// Evidence quotes the turns or values the outcome rests on.
	Evidence []string
}

// RunResult is everything one run produced, the input of every assertion.
type RunResult struct {
	Transcript []Turn
	ToolCalls  []target.ToolCall
	Metrics    map[string]float64
	Artifacts  target.Artifacts
	// Status lists the HTTP status of every turn (rest).
	Status []int
	Events []target.Event
}

// Turn is one line of the transcript.
type Turn struct {
	// Role is "user" or "agent".
	Role string
	Text string
	At   time.Time
	// Index is the 1-based agent-turn number for agent turns, 0 for user turns.
	Index int
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
