package replay

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/target"
)

type fakeProvider struct {
	sims, judges int
}

func (f *fakeProvider) Simulate(_ context.Context, in model.SimInput) (model.SimOutput, error) {
	f.sims++
	return model.SimOutput{Say: "turn " + string(rune('0'+in.Turn)), Reason: "fake"}, nil
}

func (f *fakeProvider) Judge(context.Context, tassert.JudgeRequest) (tassert.JudgeVerdict, error) {
	f.judges++
	return tassert.JudgeVerdict{Pass: true, Reason: "fine", TraceID: "t1"}, nil
}

func (f *fakeProvider) Evaluate(context.Context, tassert.EvalRequest) (tassert.EvalVerdict, error) {
	return tassert.EvalVerdict{Pass: true, Score: 1}, nil
}

func (f *fakeProvider) Usage() model.Usage          { return model.Usage{Calls: f.sims + f.judges} }
func (f *fakeProvider) Close(context.Context) error { return nil }

func TestReplayCachesSimulatorAndJudge(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	inner := &fakeProvider{}
	p := New(inner, dir, ModeAuto)
	in := model.SimInput{Persona: config.Persona{Name: "Marta"}, Scenario: config.Scenario{Goal: "g"}, Turn: 1, MaxTurns: 4, Seed: 7,
		History: []tassert.Turn{{Role: "agent", Text: "hi"}}}
	out, err := p.Simulate(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, "turn 1", out.Say)
	out2, err := p.Simulate(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, out, out2)
	assert.Equal(t, 1, inner.sims, "the second call came from the cache")
	in.History[0].Text = "hello there"
	_, err = p.Simulate(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, 2, inner.sims, "a different conversation is a different key")

	req := tassert.JudgeRequest{Rubric: "polite", Scale: "binary", Transcript: in.History, Metrics: map[string]float64{"turn_latency_ms": 123}}
	v, err := p.Judge(ctx, req)
	require.NoError(t, err)
	assert.True(t, v.Pass)
	req.Metrics["turn_latency_ms"] = 999
	_, err = p.Judge(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 1, inner.judges, "metrics are not part of the judge key")
	req.Rubric = "rude"
	_, err = p.Judge(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 2, inner.judges)
	assert.Equal(t, Stats{Hits: 2, Misses: 4}, p.Stats())

	// Replay mode: simulator turns from the cache only; a judge miss still
	// asks the model and is recorded.
	rinner := &fakeProvider{}
	rp := New(rinner, dir, ModeReplay)
	_, err = rp.Judge(ctx, req)
	require.NoError(t, err, "cached by the first provider")
	assert.Equal(t, 0, rinner.judges)
	_, err = rp.Judge(ctx, tassert.JudgeRequest{Rubric: "brand new"})
	require.NoError(t, err)
	assert.Equal(t, 1, rinner.judges, "a judge miss calls the model under replay")
	_, err = rp.Simulate(ctx, model.SimInput{Turn: 9})
	assert.ErrorIs(t, err, ErrMiss)

	// Update mode ignores the cache and refreshes it.
	up := &fakeProvider{}
	u := New(up, dir, ModeUpdate)
	_, err = u.Judge(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 1, up.judges)

	// Off never touches the disk.
	off := &fakeProvider{}
	o := New(off, t.TempDir(), ModeOff)
	_, _ = o.Simulate(ctx, in)
	_, _ = o.Simulate(ctx, in)
	assert.Equal(t, 2, off.sims)
	assert.Equal(t, model.Usage{Calls: 2}, o.Usage())
	assert.NoError(t, o.Close(ctx))
	_, err = o.Synthesize(ctx, target.SynthesizeRequest{Text: "x"})
	assert.Error(t, err, "no speech on the fake")
}

func TestReplayForwardsRolesItLacks(t *testing.T) {
	p := New(&fakeProvider{}, t.TempDir(), ModeAuto)
	assert.False(t, p.HasSpeech())
	_, err := p.RunGoal(context.Background(), model.GoalRequest{})
	assert.True(t, errors.Is(err, err) && err != nil)
	_, err = p.GenerateTests(context.Background(), model.GenerateRequest{})
	assert.Error(t, err)
}

// Ground implements assert.Grounder: every claim is supported.
func (f *fakeProvider) Ground(_ context.Context, req tassert.GroundRequest) (tassert.GroundVerdict, error) {
	return tassert.GroundVerdict{Claims: []tassert.Claim{{Text: "fake claim", Supported: true, Evidence: "(fake)"}}, Reason: "fake grounder"}, nil
}
