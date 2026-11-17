package runner

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/report"
	"github.com/xavidop/tamesu/target"
)

// deskTarget is a target with a page, as the desktop one is; it records how
// many sessions are open at once.
type deskTarget struct {
	open, peak atomic.Int32
}

func (d *deskTarget) Name() string { return "desk" }

func (d *deskTarget) Start(context.Context, *target.RunContext) (target.Session, error) {
	n := d.open.Add(1)
	for {
		p := d.peak.Load()
		if n <= p || d.peak.CompareAndSwap(p, n) {
			break
		}
	}
	return &deskSession{t: d}, nil
}

type deskSession struct {
	cachePage
	t *deskTarget
}

func (s *deskSession) Page() target.Page                           { return s }
func (s *deskSession) Send(context.Context, target.UserTurn) error { return nil }
func (s *deskSession) Receive(context.Context) (target.AgentTurn, error) {
	return target.AgentTurn{}, nil
}
func (s *deskSession) Close(context.Context) (target.Artifacts, error) {
	s.t.open.Add(-1)
	return target.Artifacts{}, nil
}

// Do takes a while, as driving an app does.
func (s *deskSession) Do(context.Context, map[string]any) (string, error) {
	time.Sleep(60 * time.Millisecond)
	return "done", nil
}

// TestDesktopRunsTakeTurns: a machine has one desktop, so the runs of desktop
// targets never overlap whatever --parallel is, and a run that waits its turn
// is not charged the wait against its timeout.
func TestDesktopRunsTakeTurns(t *testing.T) {
	desk := &deskTarget{}
	s := &config.Suite{Suite: "s", Path: "s.yaml", Targets: map[string]config.Target{"notes": {Type: config.TargetDesktop, App: "Notes"}}}
	timeout := config.Duration(200 * time.Millisecond)
	for i := 0; i < 5; i++ {
		s.Tests = append(s.Tests, config.Test{
			Name: fmt.Sprintf("t%d", i), Target: "notes", Timeout: &timeout,
			Steps: []config.Step{{Action: map[string]any{"wait": 1}}},
		})
	}
	o, _ := newOptions(t, s, &fakeProvider{})
	o.Parallel = 5
	o.Targets = func(context.Context, TargetSpec) (target.Target, error) { return desk, nil }
	sum, err := Run(context.Background(), o)
	require.NoError(t, err)
	assert.Equal(t, 0, sum.ExitCode(), "five 60 ms runs with a 200 ms timeout each pass only if the wait for a turn is not on the clock")
	assert.Equal(t, int32(1), desk.peak.Load(), "desktop runs are one at a time")
}

// surfaceAgent records what it was asked.
type surfaceAgent struct {
	model.Provider
	desktop []bool
}

func (a *surfaceAgent) RunGoal(_ context.Context, req model.GoalRequest) (model.GoalResult, error) {
	a.desktop = append(a.desktop, req.Desktop)
	return model.GoalResult{Success: true, Reason: "ok"}, nil
}

func (a *surfaceAgent) CheckExpect(context.Context, target.Page, string) (model.ExpectResult, error) {
	return model.ExpectResult{Pass: true}, nil
}

func TestAGoalOnADesktopTargetSaysSo(t *testing.T) {
	agent := &surfaceAgent{}
	r := &run{o: Options{Provider: agent, CacheDir: t.TempDir()}, now: time.Now}
	page := &cachePage{tree: loginTree, url: "desktop://Notes"}
	for _, typ := range []string{config.TargetDesktop, config.TargetBrowser} {
		it := &item{target: config.Target{Type: typ}, suite: &config.Suite{}}
		_, _, err := r.runGoal(context.Background(), it, page, agent, NewActionCache(t.TempDir()), "Write a note", &report.RunOutcome{})
		require.NoError(t, err)
	}
	assert.Equal(t, []bool{true, false}, agent.desktop)
}

func TestReplayPressesKeys(t *testing.T) {
	page := &cachePage{tree: loginTree, url: "desktop://Notes"}
	n, err := Replay(context.Background(), page, &Plan{Actions: []target.Action{
		{Name: "click", Args: map[string]any{"element": `button "Log in"`}},
		{Name: "press", Args: map[string]any{"key": "cmd+s"}},
	}})
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []string{"click", "3", "press", "cmd+s"}, page.actions)
}
