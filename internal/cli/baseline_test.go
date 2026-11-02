package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

func writeSummary(t *testing.T, dir string, passed bool, latency float64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	s := map[string]any{
		"started_at": "2026-11-01T00:00:00Z", "out_dir": dir,
		"tests": []map[string]any{{"suite": "s", "name": "refund", "passed": passed, "pass_rate": 1,
			"runs": []map[string]any{{"passed": passed, "metrics": map[string]any{"turn_latency_ms_p95": latency, "llm_cost_usd": 0.01}}}}},
	}
	b, _ := json.Marshal(s)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), b, 0o644))
}

func TestBaselineSaveAndCompare(t *testing.T) {
	root := t.TempDir()
	base, good, slow := filepath.Join(root, "base"), filepath.Join(root, "good"), filepath.Join(root, "slow")
	writeSummary(t, base, true, 1000)
	writeSummary(t, good, true, 1100)
	writeSummary(t, slow, true, 1500)
	baselineDir := filepath.Join(root, "baseline")
	run := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		r := cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &out, Err: &errOut}, cli.Deps{LookupEnv: func(string) (string, bool) { return "", false }})
		code := cli.Execute(context.Background(), r, append([]string{"--config", filepath.Join(root, "none.yaml")}, args...))
		return code, out.String() + errOut.String()
	}
	code, text := run("baseline", "save", base, "--dir", baselineDir)
	assert.Equal(t, cli.ExitOK, code, text)
	assert.Contains(t, text, "baseline saved to")
	_, err := os.Stat(filepath.Join(baselineDir, "summary.json"))
	require.NoError(t, err)

	code, text = run("compare", baselineDir, good)
	assert.Equal(t, cli.ExitOK, code, text)
	assert.Contains(t, text, "no regression")
	_, err = os.Stat(filepath.Join(good, "comparison.json"))
	require.NoError(t, err)

	code, text = run("compare", baselineDir, slow)
	assert.Equal(t, cli.ExitFailed, code, text)
	assert.Contains(t, text, "REGRESSED")
	assert.Contains(t, text, "turn_latency_ms_p95 1000 ms → 1500 ms (+50%, limit 20%)")

	code, text = run("compare", baselineDir, slow, "--max-latency-increase-pct", "60")
	assert.Equal(t, cli.ExitOK, code, text)

	code, text = run("compare", filepath.Join(root, "missing"), slow)
	assert.Equal(t, cli.ExitConfig, code, text)
	assert.Contains(t, text, "save one with tamesu baseline save")
}
