package persona

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/target"
)

// ivrSession is an IVR: it greets on the first listening turn, answers digits
// with a menu branch and says nothing else.
type ivrSession struct {
	clock   *fakeClock
	pending *target.AgentTurn
	sent    []target.UserTurn
	silent  bool // never says anything
}

func (s *ivrSession) Send(_ context.Context, ut target.UserTurn) error {
	s.sent = append(s.sent, ut)
	start := s.clock.Advance(50 * time.Millisecond)
	end := s.clock.Advance(400 * time.Millisecond)
	text := ""
	switch {
	case s.silent:
	case len(s.sent) == 1:
		text = "Welcome to Mochi Shoes. Press 1 for orders, press 2 for billing."
	case ut.DTMF == "2":
		text = "Billing department, how can I help you today?"
	case ut.Text != "":
		text = "Noted: " + ut.Text
	}
	s.pending = &target.AgentTurn{Text: text, StartedAt: start, FirstTokenAt: start, EndedAt: end}
	return nil
}

func (s *ivrSession) Receive(context.Context) (target.AgentTurn, error) {
	t := *s.pending
	s.pending = nil
	return t, nil
}

func (s *ivrSession) Close(context.Context) (target.Artifacts, error) { return target.Artifacts{}, nil }

func TestScriptNavigatesAnIVR(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	sess := &ivrSession{clock: clock}
	sim := &scriptedSim{outputs: []model.SimOutput{{Say: "I was charged twice", Stop: false}, {Say: "", Stop: true, Reason: "done"}}}
	loop := &Loop{Sim: sim, Now: clock.Now, Scenario: config.Scenario{Goal: "reach billing", MaxTurns: 4, Script: []config.ScriptStep{
		{WaitFor: "press two for billing"},
		{DTMF: "2"},
		{WaitFor: "billing department"},
		{Say: "Hello, is this billing?"},
	}}}
	out, err := loop.Run(context.Background(), sess)
	require.NoError(t, err)
	assert.Nil(t, out.ScriptError)
	assert.Equal(t, "simulator: done", out.StopReason)
	var roles []string
	for _, tr := range out.Transcript {
		roles = append(roles, tr.Role+": "+tr.Text)
	}
	assert.Equal(t, []string{
		"agent: Welcome to Mochi Shoes. Press 1 for orders, press 2 for billing.",
		"user: [dtmf 2]",
		"agent: Billing department, how can I help you today?",
		"user: Hello, is this billing?",
		"agent: Noted: Hello, is this billing?",
		"user: I was charged twice",
		"agent: Noted: I was charged twice",
	}, roles, "listening turns add no user line; digits and say do")
	assert.Equal(t, "2", sess.sent[1].DTMF)
	require.Len(t, sim.inputs, 2)
	assert.Len(t, sim.inputs[0].History, 5, "the persona sees the scripted exchange")
	assert.Equal(t, 1, sim.inputs[0].Turn)
	assert.Len(t, out.AgentTurns, 4, "four agent turns recorded")
	assert.Len(t, out.LatenciesMs, 4)
}

func TestScriptOnlyScenarioStopsWithoutASimulator(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	sess := &ivrSession{clock: clock}
	loop := &Loop{Now: clock.Now, Scenario: config.Scenario{Script: []config.ScriptStep{
		{Say: "Where is my refund?"},
		{Say: "Order 1234"},
	}}}
	out, err := loop.Run(context.Background(), sess)
	require.NoError(t, err, "no goal, no simulator needed")
	assert.Nil(t, out.ScriptError)
	assert.Equal(t, StopScriptEnd, out.StopReason)
	assert.Len(t, out.AgentTurns, 2)
	assert.Equal(t, "Noted: Order 1234", out.Transcript[len(out.Transcript)-1].Text)

	_, err = (&Loop{Scenario: config.Scenario{Goal: "ask for a refund"}}).Run(context.Background(), sess)
	assert.ErrorIs(t, err, ErrNoSimulator, "a goal still needs the simulator")
}

func TestScriptWaitForTimesOutAsAFailure(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	sess := &ivrSession{clock: clock, silent: true}
	loop := &Loop{Sim: &scriptedSim{}, Now: clock.Now, ScriptTimeout: 2 * time.Second, Scenario: config.Scenario{Goal: "x", Script: []config.ScriptStep{{WaitFor: "press two for billing"}}}}
	out, err := loop.Run(context.Background(), sess)
	require.NoError(t, err, "a missed wait_for is a failure, not an error")
	require.NotNil(t, out.ScriptError)
	assert.Equal(t, StopScript, out.StopReason)
	assert.Contains(t, out.ScriptError.Error(), `wait_for "press two for billing"`)
	assert.Contains(t, out.ScriptError.Error(), "not heard within 2s (heard: nothing)")
	assert.GreaterOrEqual(t, len(sess.sent), 4, "it kept listening until the timeout")
}

func TestFuzzyContains(t *testing.T) {
	tests := []struct {
		name  string
		heard string
		want  string
		ok    bool
	}{
		{"exact", "Press 2 for billing", "press 2 for billing", true},
		{"punctuation and case", "Welcome! Press TWO, for billing.", "press two for billing", true},
		{"digit vs word", "press 2 for billing", "press two for billing", true},
		{"one slip in six words", "please press two four billing now", "please press two for billing now", true},
		{"too many slips", "press one for orders", "press two for billing", false},
		{"not there", "goodbye", "press two for billing", false},
		{"empty want", "anything", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.ok, fuzzyContains(tt.heard, tt.want))
		})
	}
}
