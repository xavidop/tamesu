// Package persona runs the simulated-user loop of a conversational test: the
// simulator produces the next user turn, the target sends it and returns the
// agent's turn, and the loop stops on the scenario's conditions. Timestamps
// come from the transport, never from a model.
package persona

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/target"
)

// DefaultMaxTurns bounds a scenario that sets no max_turns.
const DefaultMaxTurns = 10

// Stop reasons.
const (
	StopSimulator = "simulator"  // the simulator reported the goal met (or unreachable)
	StopHangUp    = "hang_up"    // the persona hung up
	StopAgentDone = "agent_done" // the agent ended the session
	StopMaxTurns  = "max_turns"  // the turn budget ran out
	StopScript    = "script"     // a scripted step failed (Outcome.ScriptError)
	StopScriptEnd = "script_end" // the script played out and the scenario has no goal
)

// ErrNoSimulator is returned when the loop has no model to play the user.
var ErrNoSimulator = errors.New("no simulator model is available: conversational tests need the llm module")

// Loop drives one conversation.
type Loop struct {
	Sim      model.Simulator
	Persona  config.Persona
	Scenario config.Scenario
	Seed     int64
	Mode     string
	Logger   *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// OnTurn is called after every turn is appended (optional, for live output).
	OnTurn func(assert.Turn)
	// OnAgentTurn is called with every agent turn as the transport returned
	// it (raw payload included), before the next user turn is produced.
	OnAgentTurn func(target.AgentTurn)
	// Render expands templates in script say steps at send time, so values
	// captured earlier in the conversation are available; nil sends as is.
	Render func(string) (string, error)
	// ScriptTimeout bounds each wait_for step (default 30 s).
	ScriptTimeout time.Duration
}

// Outcome is what a finished conversation produced.
type Outcome struct {
	Transcript []assert.Turn
	AgentTurns []target.AgentTurn
	// LatenciesMs[i] is user turn i sent → agent turn i complete.
	LatenciesMs []float64
	// FirstTokenMs[i] is user turn i sent → first delta, when the transport
	// reported one.
	FirstTokenMs []float64
	TraceIDs     []string
	StopReason   string
	// ScriptError is set when a scripted step failed (a wait_for that was
	// never heard); the conversation ends there and the run fails.
	ScriptError *ScriptError
}

// Run plays the conversation on sess until a stop condition. A transport or
// simulator error ends the conversation and is returned with the partial
// outcome.
func (l *Loop) Run(ctx context.Context, sess target.Session) (*Outcome, error) {
	scriptOnly := strings.TrimSpace(l.Scenario.Goal) == "" && scriptSteps(l.Scenario) > 0
	if l.Sim == nil && !scriptOnly {
		return &Outcome{}, ErrNoSimulator
	}
	now := l.Now
	if now == nil {
		now = time.Now
	}
	logger := l.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	maxTurns := l.Scenario.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	out := &Outcome{}
	if scriptSteps(l.Scenario) > 0 {
		if err := l.runScript(ctx, sess, out, now); err != nil {
			var se *ScriptError
			if errors.As(err, &se) {
				out.ScriptError = se
				out.StopReason = StopScript
				return out, nil
			}
			return out, err
		}
		if out.StopReason != "" {
			return out, nil
		}
		if scriptOnly {
			out.StopReason = StopScriptEnd
			return out, nil
		}
	}
	agentIndex := len(out.AgentTurns)
	for turn := 1; ; turn++ {
		if turn > maxTurns {
			out.StopReason = StopMaxTurns
			return out, nil
		}
		sim, err := l.Sim.Simulate(ctx, model.SimInput{
			Persona: l.Persona, Scenario: l.Scenario, History: out.Transcript,
			Turn: turn, MaxTurns: maxTurns, Mode: l.Mode, Seed: l.Seed,
		})
		if err != nil {
			return out, fmt.Errorf("simulator: %w", err)
		}
		if sim.TraceID != "" {
			out.TraceIDs = append(out.TraceIDs, sim.TraceID)
		}
		if sim.Say == "" && sim.DTMF == "" {
			out.StopReason = stopReason(sim, StopSimulator)
			logger.Debug("persona stopped without a turn", "reason", sim.Reason)
			return out, nil
		}
		sentAt := now()
		userTurn := assert.Turn{Role: "user", Text: sim.Say, At: sentAt}
		out.Transcript = append(out.Transcript, userTurn)
		l.notify(userTurn)
		if err := sess.Send(ctx, target.UserTurn{Text: sim.Say, DTMF: sim.DTMF, At: sentAt}); err != nil {
			return out, fmt.Errorf("send turn %d: %w", turn, err)
		}
		agent, err := sess.Receive(ctx)
		if err != nil {
			return out, fmt.Errorf("receive turn %d: %w", turn, err)
		}
		endedAt := agent.EndedAt
		if endedAt.IsZero() {
			endedAt = now()
		}
		agentIndex++
		agentTurn := assert.Turn{Role: "agent", Text: agent.Text, At: endedAt, Index: agentIndex}
		out.Transcript = append(out.Transcript, agentTurn)
		out.AgentTurns = append(out.AgentTurns, agent)
		out.LatenciesMs = append(out.LatenciesMs, ms(endedAt.Sub(sentAt)))
		if !agent.FirstTokenAt.IsZero() {
			out.FirstTokenMs = append(out.FirstTokenMs, ms(agent.FirstTokenAt.Sub(sentAt)))
		}
		l.notify(agentTurn)
		if l.OnAgentTurn != nil {
			l.OnAgentTurn(agent)
		}
		logger.Debug("turn", "n", turn, "latency_ms", out.LatenciesMs[len(out.LatenciesMs)-1], "done", agent.Done)
		switch {
		case sim.HangUp:
			out.StopReason = StopHangUp
			return out, nil
		case agent.Done:
			out.StopReason = StopAgentDone
			return out, nil
		case sim.Stop:
			out.StopReason = stopReason(sim, StopSimulator)
			return out, nil
		}
	}
}

func (l *Loop) notify(t assert.Turn) {
	if l.OnTurn != nil {
		l.OnTurn(t)
	}
}

func stopReason(sim model.SimOutput, base string) string {
	if sim.HangUp {
		return StopHangUp
	}
	if sim.Reason != "" {
		return base + ": " + sim.Reason
	}
	return base
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
