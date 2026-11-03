package report

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/target"
)

func TestBuildCoverage(t *testing.T) {
	s := &Summary{Tests: []*TestOutcome{
		{Name: "refund", Target: "api", Type: "rest", Tags: []string{"smoke", "returns"}, Passed: true, Runs: []*RunOutcome{{
			Assertions: []tassert.Result{{Name: `judge "polite"`}, {Name: "tool_called lookup_policy"}, {Name: "red-team/not_contains \"x\""}, {Name: "slo/turn_latency_ms_p95 <= 3000"}},
			ToolCalls:  []target.ToolCall{{Name: "lookup_policy"}, {Name: "lookup_policy"}},
			StopReason: "simulator: the goal is met",
		}}},
		{Name: "attack", Target: "api", Type: "rest", Tags: []string{"red-team"}, Passed: false, Runs: []*RunOutcome{{
			Assertions: []tassert.Result{{Name: `judge "stays in role"`}},
			ToolCalls:  []target.ToolCall{{Name: "lookup_policy"}},
			Events:     []target.Event{{Kind: "tactic", Detail: "turn 1: role-play"}, {Kind: "tactic", Detail: "turn 2: claimed authority"}, {Kind: "tool_call", Detail: "x"}},
			StopReason: "max_turns",
		}, {
			Events:     []target.Event{{Kind: "tactic", Detail: "turn 1: role-play"}},
			StopReason: "max_turns",
		}}},
		{Name: "shop", Target: "web", Type: "browser", Passed: true, Runs: []*RunOutcome{{}}},
	}}
	c := BuildCoverage(s, map[string][]string{"api": {"lookup_policy", "refund"}})
	assert.Equal(t, 3, c.Tests)
	assert.Equal(t, 4, c.Runs)
	assert.Equal(t, []CoverageItem{{Name: "api (rest)", Count: 2, Passed: 1}, {Name: "web (browser)", Count: 1, Passed: 1}}, c.Targets)
	assert.Equal(t, []CoverageItem{{Name: "red-team", Count: 1}, {Name: "returns", Count: 1, Passed: 1}, {Name: "smoke", Count: 1, Passed: 1}}, c.Tags)
	assert.Equal(t, []ToolCoverage{{Name: "lookup_policy", Calls: 3, Tests: 2, Declared: true}}, c.Tools)
	assert.Equal(t, []string{"refund"}, c.Uncalled, "declared and never called")
	assert.Equal(t, []CoverageItem{{Name: "judge", Count: 2, Passed: 1}, {Name: "not_contains", Count: 1, Passed: 1}, {Name: "slo/turn_latency_ms_p95", Count: 1, Passed: 1}, {Name: "tool_called", Count: 1, Passed: 1}}, c.Assertions, "pack prefixes stripped, slo kept whole")
	assert.Equal(t, []CoverageItem{{Name: "max_turns", Count: 1}, {Name: "simulator", Count: 1, Passed: 1}}, c.StopReasons)
	assert.Equal(t, []CoverageItem{{Name: "claimed authority", Count: 1}, {Name: "role-play", Count: 1}}, c.Tactics, "a tactic counts once per test")

	md := c.Markdown()
	assert.Contains(t, md, "| `lookup_policy` | 3 | 2 | yes |")
	assert.Contains(t, md, "Declared but never called: `refund`.")
	assert.Contains(t, md, "## Attack tactics")
	con := c.Console()
	assert.Contains(t, con, "coverage: 3 tests, 4 runs")
	assert.Contains(t, con, "never called")

	dir := t.TempDir()
	require.NoError(t, WriteCoverage(dir, c))
	b, err := os.ReadFile(filepath.Join(dir, CoverageJSON))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"uncalled_tools"`)
	_, err = os.Stat(filepath.Join(dir, CoverageMD))
	assert.NoError(t, err)
}
