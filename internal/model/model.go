// Package model holds the types the runner exchanges with the AI layer: the
// simulated user's input and output, usage, and the Provider interface the
// llm module implements. It has no model SDK dependency, so the root module
// stays light and the llm module stays the only place a model is called.
package model

import (
	"context"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
)

// SimInput is what the simulated user needs to produce its next turn.
type SimInput struct {
	Persona  config.Persona  `json:"persona"`
	Scenario config.Scenario `json:"scenario"`
	// History is the conversation so far, user and agent turns in order.
	History []assert.Turn `json:"history,omitempty"`
	// Turn is the 1-based number of the user turn about to be produced.
	Turn int `json:"turn"`
	// MaxTurns is the scenario's turn budget (0 means the default).
	MaxTurns int `json:"max_turns"`
	// Mode is text or audio; audio phrasing is spoken, not written.
	Mode string `json:"mode,omitempty"`
	// Seed makes the simulator as repeatable as the provider allows.
	Seed int64 `json:"seed,omitempty"`
}

// SimOutput is the simulated user's decision for one turn.
type SimOutput struct {
	// Say is the next user utterance; empty with Stop set ends the
	// conversation without another turn.
	Say string `json:"say"`
	// DTMF digits to send instead of, or before, speaking (sip and livekit audio).
	DTMF string `json:"dtmf,omitempty"`
	// HangUp ends the call after this turn.
	HangUp bool `json:"hang_up,omitempty"`
	// Stop reports that the scenario's goal or stop condition is met (or
	// cannot be met); the conversation ends after this turn.
	Stop bool `json:"stop"`
	// Reason explains Stop or HangUp.
	Reason string `json:"reason,omitempty"`
	// TraceID is the Genkit trace of the call, for the report.
	TraceID string `json:"trace_id,omitempty"`
}

// Usage is what tamesu's own model calls have consumed so far.
type Usage struct {
	Calls        int     `json:"calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Total is input plus output tokens.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// Simulator produces the simulated user's turns.
type Simulator interface {
	Simulate(ctx context.Context, in SimInput) (SimOutput, error)
}

// Provider is the AI layer a run needs: the simulator, the judge, Genkit
// evaluators and the usage meter. The llm module implements it.
type Provider interface {
	Simulator
	assert.Judger
	assert.Evaluator
	Usage() Usage
	Close(ctx context.Context) error
}
