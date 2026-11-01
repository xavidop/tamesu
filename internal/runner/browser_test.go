package runner

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/report"
	"github.com/xavidop/tamesu/target"
)

// fakeAgent counts its calls and returns a fixed plan.
type fakeAgent struct {
	model.Provider
	calls int
}

func (a *fakeAgent) RunGoal(_ context.Context, req model.GoalRequest) (model.GoalResult, error) {
	a.calls++
	_ = req.Page.Click(context.Background(), 3)
	return model.GoalResult{Success: true, Reason: "agent: logged in", Steps: 1, Actions: []target.Action{{Name: "click", Args: map[string]any{"ref": 3, "element": `button "Log in"`}}}}, nil
}

func (a *fakeAgent) CheckExpect(context.Context, target.Page, string) (model.ExpectResult, error) {
	return model.ExpectResult{Pass: true, Reason: "ok"}, nil
}

func TestRunGoalUsesTheCache(t *testing.T) {
	dir := t.TempDir()
	agent := &fakeAgent{}
	newRun := func(replay, update bool) *run {
		return &run{o: Options{Provider: agent, CacheDir: dir, Replay: replay, UpdateCache: update}, logger: slog.New(slog.DiscardHandler), now: time.Now}
	}
	it := &item{target: config.Target{AllowedDomains: []string{"shop.test"}}, suite: &config.Suite{}}
	page := &cachePage{tree: loginTree, url: "http://shop.test/login"}

	// First run: a miss, the agent plans, the plan is saved.
	out := &report.RunOutcome{}
	res, hit, err := newRun(false, false).runGoal(context.Background(), it, page, agent, NewActionCache(dir), "Log in", out)
	require.NoError(t, err)
	assert.False(t, hit)
	assert.True(t, res.Success)
	assert.Equal(t, 1, agent.calls)
	assert.Equal(t, []string{"cache_miss", "cache_saved"}, kinds(out.Events))
	entries, _ := os.ReadDir(filepath.Join(dir, "actions"))
	assert.Len(t, entries, 1)

	// Second run: replayed, zero agent calls, even with --replay.
	page2 := &cachePage{offset: 20, tree: loginTree, url: "http://shop.test/login"}
	out = &report.RunOutcome{}
	res, hit, err = newRun(true, false).runGoal(context.Background(), it, page2, agent, NewActionCache(dir), "Log in", out)
	require.NoError(t, err)
	assert.True(t, hit)
	assert.True(t, res.Success)
	assert.Contains(t, res.Reason, "replayed 1 cached actions")
	assert.Equal(t, 1, agent.calls, "no model call on a hit")
	assert.Equal(t, []string{"click", itoa(23)}, page2.actions, "the element was matched on the new page")
	assert.Equal(t, []string{"cache_hit"}, kinds(out.Events))

	// A changed page under --replay: a miss fails the step without the agent.
	changed := &cachePage{tree: func(int) string { return "[1] button \"Sign in\"" }, url: "http://shop.test/login"}
	out = &report.RunOutcome{}
	res, hit, err = newRun(true, false).runGoal(context.Background(), it, changed, agent, NewActionCache(dir), "Log in", out)
	require.NoError(t, err)
	assert.False(t, hit)
	assert.False(t, res.Success)
	assert.Contains(t, res.Reason, "--replay: no cached plan")
	assert.Equal(t, 1, agent.calls)

	// Without --replay the same miss goes to the agent and is cached.
	out = &report.RunOutcome{}
	_, hit, err = newRun(false, false).runGoal(context.Background(), it, changed, agent, NewActionCache(dir), "Log in", out)
	require.NoError(t, err)
	assert.False(t, hit)
	assert.Equal(t, 2, agent.calls)

	// --update-cache ignores the hit and plans again.
	out = &report.RunOutcome{}
	_, hit, err = newRun(false, true).runGoal(context.Background(), it, page, agent, NewActionCache(dir), "Log in", out)
	require.NoError(t, err)
	assert.False(t, hit)
	assert.Equal(t, 3, agent.calls)
	assert.Equal(t, []string{"cache_saved"}, kinds(out.Events))
}

func kinds(events []target.Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}
