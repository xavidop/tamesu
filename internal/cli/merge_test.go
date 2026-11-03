package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

func TestMergeAndHistory(t *testing.T) {
	deps, _, _ := runDeps(t)
	dir := writeProject(t, map[string]string{"tamesu.yaml": "version: 1\nsuites: [\"s/**/*.yaml\"]\n", "s/suite.yaml": runSuite})
	cfg := filepath.Join(dir, "tamesu.yaml")
	runs := filepath.Join(dir, "runs")
	env := map[string]string{"TOKEN": "t"}
	for i, shard := range []string{"1/2", "2/2"} {
		out := filepath.Join(runs, "2026-11-03T09-0"+string(rune('0'+i))+"-00")
		run(t, env, deps, "run", "--config", cfg, "--filter", "good|bad", "--shard", shard, "--out", out)
	}
	merged := filepath.Join(runs, "merged")
	code, stdout, stderr := run(t, nil, deps, "merge", filepath.Join(runs, "2026-11-03T09-00-00"), filepath.Join(runs, "2026-11-03T09-01-00"), "--out", merged)
	assert.Equal(t, cli.ExitFailed, code, stdout+stderr)
	assert.Contains(t, stdout, "2 tests from 2 runs · 1 passed, 1 failed, 0 errors · shard=1/2,2/2")
	for _, f := range []string{"summary.json", "junit.xml", "report.html"} {
		_, err := os.Stat(filepath.Join(merged, f))
		assert.NoError(t, err, f)
	}
	code, _, stderr = run(t, nil, deps, "merge", filepath.Join(runs, "nope"), "--out", filepath.Join(dir, "x"))
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "nope")

	// Two more full runs so the history has something to say.
	for _, ts := range []string{"2026-11-03T10-00-00", "2026-11-03T11-00-00"} {
		run(t, env, deps, "run", "--config", cfg, "--filter", "good|bad", "--out", filepath.Join(runs, ts))
	}
	code, stdout, _ = run(t, nil, deps, "history", "--runs-dir", runs)
	assert.Equal(t, cli.ExitOK, code, stdout)
	assert.Contains(t, stdout, "5 runs,")
	assert.Contains(t, stdout, "run-suite / bad")
	assert.Contains(t, stdout, "fail 4× in a row")
	code, stdout, _ = run(t, nil, deps, "history", "--runs-dir", runs, "--json", "--filter", "good", "--last", "2")
	assert.Equal(t, cli.ExitOK, code)
	assert.Contains(t, stdout, `"pass_rate": 1`)
	assert.NotContains(t, stdout, `"name": "bad"`)
	code, stdout, _ = run(t, nil, deps, "history", "--runs-dir", runs, "--flaky")
	assert.Equal(t, cli.ExitOK, code)
	assert.NotContains(t, stdout, "run-suite /", "nothing flaky")
	code, _, stderr = run(t, nil, deps, "history", "--runs-dir", filepath.Join(dir, "missing"))
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "missing")

	// A run under the runs directory gets the history section in its report.
	last := filepath.Join(runs, "2026-11-03T11-00-00")
	b, err := os.ReadFile(filepath.Join(last, "report.html"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "History: the last")
}
