package persona

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/target"
)

// scriptedSim replays outputs in order and records its inputs.
type scriptedSim struct {
	outputs []model.SimOutput
	inputs  []model.SimInput
	err     error
}

func (s *scriptedSim) Simulate(_ context.Context, in model.SimInput) (model.SimOutput, error) {
	s.inputs = append(s.inputs, in)
	if s.err != nil {
		return model.SimOutput{}, s.err
	}
	if len(s.outputs) == 0 {
		return model.SimOutput{Stop: true, Reason: "script exhausted"}, nil
	}
	out := s.outputs[0]
	s.outputs = s.outputs[1:]
	return out, nil
}

// echoSession answers every turn with "echo: <text>", ending the session
// after doneAfter turns when doneAfter > 0.
type echoSession struct {
	clock     *fakeClock
	pending   *target.AgentTurn
	turns     int
	doneAfter int
	sendErr   error
	closed    int
}

func (e *echoSession) Send(_ context.Context, turn target.UserTurn) error {
	if e.sendErr != nil {
		return e.sendErr
	}
	e.turns++
	start := e.clock.Advance(100 * time.Millisecond)
	first := e.clock.Advance(50 * time.Millisecond)
	end := e.clock.Advance(350 * time.Millisecond)
	e.pending = &target.AgentTurn{Text: "echo: " + turn.Text + turn.DTMF, StartedAt: start, FirstTokenAt: first, EndedAt: end, Done: e.doneAfter > 0 && e.turns >= e.doneAfter, Status: 200}
	return nil
}

func (e *echoSession) Receive(context.Context) (target.AgentTurn, error) {
	if e.pending == nil {
		return target.AgentTurn{}, errors.New("receive before send")
	}
	t := *e.pending
	e.pending = nil
	return t, nil
}

func (e *echoSession) Close(context.Context) (target.Artifacts, error) {
	e.closed++
	return target.Artifacts{}, nil
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) Advance(d time.Duration) time.Time {
	c.t = c.t.Add(d)
	return c.t
}

func newLoop(sim model.Simulator, clock *fakeClock, maxTurns int) *Loop {
	return &Loop{Sim: sim, Persona: config.Persona{Name: "Marta"}, Scenario: config.Scenario{Goal: "ask about returns", MaxTurns: maxTurns}, Seed: 7, Mode: "text", Now: clock.Now}
}

func TestLoopStopsWhenSimulatorSaysSo(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 31, 14, 0, 0, 0, time.UTC)}
	sim := &scriptedSim{outputs: []model.SimOutput{
		{Say: "Can I return shoes?", TraceID: "t1"},
		{Say: "Thanks, bye", Stop: true, Reason: "answer received", TraceID: "t2"},
	}}
	sess := &echoSession{clock: clock}
	var seen []string
	loop := newLoop(sim, clock, 8)
	loop.OnTurn = func(turn tassert.Turn) { seen = append(seen, turn.Role) }
	out, err := loop.Run(context.Background(), sess)
	require.NoError(t, err)
	assert.Equal(t, "simulator: answer received", out.StopReason)
	require.Len(t, out.Transcript, 4)
	assert.Equal(t, "echo: Can I return shoes?", out.Transcript[1].Text)
	assert.Equal(t, 1, out.Transcript[1].Index)
	assert.Equal(t, 2, out.Transcript[3].Index)
	assert.Equal(t, []float64{500, 500}, out.LatenciesMs, "sent → agent turn complete, from transport timestamps")
	assert.Equal(t, []float64{150, 150}, out.FirstTokenMs)
	assert.Equal(t, []string{"t1", "t2"}, out.TraceIDs)
	assert.Equal(t, []string{"user", "agent", "user", "agent"}, seen)
	// The simulator saw the history grow and the scenario's turn budget.
	require.Len(t, sim.inputs, 2)
	assert.Empty(t, sim.inputs[0].History)
	assert.Len(t, sim.inputs[1].History, 2)
	assert.Equal(t, 2, sim.inputs[1].Turn)
	assert.Equal(t, 8, sim.inputs[1].MaxTurns)
	assert.Equal(t, int64(7), sim.inputs[1].Seed)
	assert.Equal(t, "Marta", sim.inputs[1].Persona.Name)
}

func TestLoopStopReasons(t *testing.T) {
	tests := []struct {
		name       string
		outputs    []model.SimOutput
		doneAfter  int
		maxTurns   int
		wantTurns  int
		wantReason string
	}{
		{"stop without saying", []model.SimOutput{{Say: "hi"}, {Stop: true, Reason: "done"}}, 0, 5, 2, "simulator: done"},
		{"hang up", []model.SimOutput{{Say: "hi", HangUp: true}}, 0, 5, 2, StopHangUp},
		{"agent done", []model.SimOutput{{Say: "a"}, {Say: "b"}, {Say: "c"}}, 2, 5, 4, StopAgentDone},
		{"max turns", []model.SimOutput{{Say: "a"}, {Say: "b"}, {Say: "c"}}, 0, 2, 4, StopMaxTurns},
		{"dtmf only counts as a turn", []model.SimOutput{{DTMF: "2"}, {Stop: true}}, 0, 5, 2, StopSimulator},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{t: time.Now()}
			out, err := newLoop(&scriptedSim{outputs: tt.outputs}, clock, tt.maxTurns).Run(context.Background(), &echoSession{clock: clock, doneAfter: tt.doneAfter})
			require.NoError(t, err)
			assert.Equal(t, tt.wantReason, out.StopReason)
			assert.Len(t, out.Transcript, tt.wantTurns)
		})
	}
}

func TestLoopErrors(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	_, err := (&Loop{}).Run(context.Background(), &echoSession{clock: clock})
	assert.ErrorIs(t, err, ErrNoSimulator)

	_, err = newLoop(&scriptedSim{err: errors.New("model down")}, clock, 3).Run(context.Background(), &echoSession{clock: clock})
	assert.EqualError(t, err, "simulator: model down")

	out, err := newLoop(&scriptedSim{outputs: []model.SimOutput{{Say: "hi"}}}, clock, 3).Run(context.Background(), &echoSession{clock: clock, sendErr: errors.New("connection refused")})
	assert.EqualError(t, err, "send turn 1: connection refused")
	assert.Len(t, out.Transcript, 1, "the user turn is kept for the report")
}

func TestLoopCounterpart(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)}
	sess := &echoSession{clock: clock}
	var prompts []string
	loop := newLoop(nil, clock, 3)
	loop.Opening = "We can offer 500 units at 12 euros"
	loop.Counterpart = func(_ context.Context, agentText string, turn int) (string, bool, error) {
		prompts = append(prompts, agentText)
		if turn == 3 {
			return "Deal.", true, nil
		}
		return "Counter: " + agentText, false, nil
	}
	out, err := loop.Run(context.Background(), sess)
	require.NoError(t, err)
	assert.Equal(t, StopCounterpart, out.StopReason)
	require.Len(t, out.Transcript, 6)
	assert.Equal(t, "We can offer 500 units at 12 euros", out.Transcript[0].Text, "the opening is sent as is")
	assert.Equal(t, "echo: We can offer 500 units at 12 euros", out.Transcript[1].Text)
	assert.Equal(t, "Counter: echo: We can offer 500 units at 12 euros", out.Transcript[2].Text, "the counterpart answers the agent's last words")
	assert.Equal(t, "Deal.", out.Transcript[4].Text)
	assert.Equal(t, []string{"echo: We can offer 500 units at 12 euros", "echo: Counter: echo: We can offer 500 units at 12 euros"}, prompts)

	// Without an opening the counterpart answers the goal first, and an
	// empty answer ends the conversation.
	sess = &echoSession{clock: clock}
	loop = newLoop(nil, clock, 3)
	loop.Counterpart = func(_ context.Context, agentText string, turn int) (string, bool, error) {
		if turn == 2 {
			return "", false, nil
		}
		return "about: " + agentText, false, nil
	}
	out, err = loop.Run(context.Background(), sess)
	require.NoError(t, err)
	assert.Equal(t, StopCounterpart, out.StopReason)
	require.Len(t, out.Transcript, 2)
	assert.Equal(t, "about: "+loop.Scenario.Goal, out.Transcript[0].Text)

	loop = newLoop(nil, clock, 3)
	loop.Counterpart = func(context.Context, string, int) (string, bool, error) { return "", false, errors.New("boom") }
	_, err = loop.Run(context.Background(), &echoSession{clock: clock})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "counterpart: boom")
}
