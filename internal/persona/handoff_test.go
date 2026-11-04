package persona

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/target"
)

func TestLoopStopsOnHandoff(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)}
	sim := &scriptedSim{outputs: []model.SimOutput{
		{Say: "Hi, my shoes arrived damaged"},
		{Say: "I want to speak to a human"},
		{Say: "never sent: the loop stopped"},
	}}
	loop := newLoop(sim, clock, 6)
	loop.Handoff = func(agent target.AgentTurn, turn int) string {
		if strings.Contains(agent.Text, "human") {
			return `{"turn":` + itoa(turn) + `,"to":"support","via":"regex"}`
		}
		return ""
	}
	sess := &echoSession{clock: clock}
	out, err := loop.Run(context.Background(), sess)
	require.NoError(t, err)
	assert.Equal(t, StopHandoff, out.StopReason)
	assert.Len(t, out.Transcript, 4, "two exchanges; the third line was never sent")
	require.Len(t, out.Events, 1)
	assert.Equal(t, tassert.EventHandoff, out.Events[0].Kind)
	assert.Equal(t, `{"turn":2,"to":"support","via":"regex"}`, out.Events[0].Detail)
	assert.Equal(t, out.Transcript[3].At, out.Events[0].At, "the event is stamped with the agent turn's end")
	assert.Len(t, sim.inputs, 2)

	hs := tassert.Handoffs(&tassert.RunResult{Events: out.Events})
	require.Len(t, hs, 1)
	assert.Equal(t, 2, hs[0].Turn)
	assert.Equal(t, "support", hs[0].To)
}

func itoa(i int) string { return strconv.Itoa(i) }
