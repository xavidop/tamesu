package report

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func summaryWith(tests ...*TestOutcome) *Summary {
	return &Summary{StartedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), OutDir: "base", Tests: tests}
}

func outcome(suite, name string, passed bool, metrics ...map[string]float64) *TestOutcome {
	t := &TestOutcome{Suite: suite, Name: name, Passed: passed, PassRate: 1}
	if !passed {
		t.PassRate = 0
	}
	for _, m := range metrics {
		t.Runs = append(t.Runs, &RunOutcome{Passed: passed, Metrics: m})
	}
	return t
}

func TestCompare(t *testing.T) {
	base := summaryWith(
		outcome("s", "refund", true, map[string]float64{"turn_latency_ms_p95": 1000, "llm_cost_usd": 0.01}),
		outcome("s", "slow", true, map[string]float64{"turn_latency_ms_p95": 1000}, map[string]float64{"turn_latency_ms_p95": 900}, map[string]float64{"turn_latency_ms_p95": 1100}),
		outcome("s", "fixed", false),
		outcome("s", "gone", true),
	)
	cur := &Summary{Tests: []*TestOutcome{
		outcome("s", "refund", false, map[string]float64{"turn_latency_ms_p95": 1100, "llm_cost_usd": 0.02}),
		outcome("s", "slow", true, map[string]float64{"turn_latency_ms_p95": 1300}, map[string]float64{"turn_latency_ms_p95": 1250}, map[string]float64{"turn_latency_ms_p95": 2000}),
		outcome("s", "fixed", true),
		outcome("s", "new", true),
	}}
	c := Compare(base, cur, DefaultThresholds)
	assert.Equal(t, "2026-11-01T00:00:00Z", c.BaselineAt)
	assert.Equal(t, []string{"s / refund"}, c.NewFailures)
	assert.Equal(t, []string{"s / fixed"}, c.Fixed)
	assert.Equal(t, []string{"s / new"}, c.Added)
	assert.Equal(t, []string{"s / gone"}, c.Removed)
	assert.True(t, c.Regressed)
	require.Len(t, c.Tests, 3)
	refund := c.Tests[0]
	assert.True(t, refund.Regressed, "a new failure is a regression")
	require.Len(t, refund.Metrics, 2)
	assert.InDelta(t, 10, refund.Metrics[0].IncreasePct, 0.01)
	assert.False(t, refund.Metrics[0].Regressed, "10% is under the 20% latency limit")
	assert.InDelta(t, 100, refund.Metrics[1].IncreasePct, 0.01)
	assert.True(t, refund.Metrics[1].Regressed, "cost doubled, over the 50% limit")
	slow := c.Tests[1]
	assert.InDelta(t, 30, slow.Metrics[0].IncreasePct, 0.01, "medians: 1000 → 1300")
	assert.True(t, slow.Regressed)

	tiny := Compare(summaryWith(outcome("s", "fast", true, map[string]float64{"turn_latency_ms_p95": 1, "llm_cost_usd": 0.0001})),
		&Summary{Tests: []*TestOutcome{outcome("s", "fast", true, map[string]float64{"turn_latency_ms_p95": 1.4, "llm_cost_usd": 0.0002})}}, DefaultThresholds)
	assert.False(t, tiny.Regressed, "growth under 50 ms or $0.001 is noise, whatever the percentage")

	lenient := Compare(base, cur, Thresholds{})
	assert.False(t, lenient.Regressed, "no thresholds, no regression")
	assert.Equal(t, []string{"s / refund"}, lenient.NewFailures, "but the new failure is still listed")

	var buf bytes.Buffer
	PrintComparison(&buf, c)
	assert.Contains(t, buf.String(), "vs baseline 2026-11-01T00:00:00Z: REGRESSED · 3 compared, 1 new failures, 1 fixed, 1 added, 1 removed")
	assert.Contains(t, buf.String(), "new failure  s / refund")
	assert.Contains(t, buf.String(), "regression   s / slow: turn_latency_ms_p95 1000 ms → 1300 ms (+30%, limit 20%)")
	assert.Contains(t, buf.String(), "llm_cost_usd $0.0100 → $0.0200 (+100%, limit 50%)")

	none := Compare(nil, cur, DefaultThresholds)
	assert.Len(t, none.Added, 4)
	assert.False(t, none.Regressed)
	dir := t.TempDir()
	require.NoError(t, WriteComparison(dir, c))
}
