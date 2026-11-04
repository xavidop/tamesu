package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

const envProject = `version: 1
suites: ["s/**/*.yaml"]
budgets: { max_cost_usd: 2 }
labels: { team: support }
environments:
  staging:
    vars: { who: marta }
    labels: { tier: pre }
  production:
    vars: { who: ken }
    secrets: { TOKEN: fake://prod-token }
`

func labelsOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	require.NoError(t, err)
	var s struct {
		Labels map[string]string `json:"labels"`
	}
	require.NoError(t, json.Unmarshal(b, &s))
	return s.Labels
}

func TestRunEnvironments(t *testing.T) {
	deps, _, _ := runDeps(t)
	dir := writeProject(t, map[string]string{"tamesu.yaml": envProject, "s/suite.yaml": runSuite})
	cfg := filepath.Join(dir, "tamesu.yaml")
	env := map[string]string{"TOKEN": "t"}

	out := filepath.Join(dir, "staging")
	code, stdout, stderr := run(t, env, deps, "run", "--config", cfg, "--filter", "good", "--env", "staging", "--out", out)
	assert.Equal(t, cli.ExitOK, code, stdout+stderr)
	assert.Equal(t, map[string]string{"team": "support", "env": "staging", "tier": "pre"}, labelsOf(t, out), "the environment labels the run")

	code, _, stderr = run(t, env, deps, "run", "--config", cfg, "--filter", "good", "--env", "qa", "--out", filepath.Join(dir, "qa"))
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, `unknown environment "qa" (have production, staging)`)

	matrix := filepath.Join(dir, "matrix")
	code, stdout, stderr = run(t, env, deps, "run", "--config", cfg, "--filter", "good", "--matrix", "env=staging,production", "--out", matrix)
	assert.Equal(t, cli.ExitOK, code, stdout+stderr)
	assert.Contains(t, stdout, "==> matrix 1/2: env=staging")
	assert.Contains(t, stdout, "==> matrix 2/2: env=production")
	assert.Equal(t, "staging", labelsOf(t, filepath.Join(matrix, "env-staging"))["env"])
	assert.Equal(t, "production", labelsOf(t, filepath.Join(matrix, "env-production"))["env"])
	md, err := os.ReadFile(filepath.Join(matrix, "matrix.md"))
	require.NoError(t, err)
	assert.Contains(t, string(md), "`env=staging`")
	assert.Contains(t, string(md), "`env=production`")

	code, _, stderr = run(t, env, deps, "run", "--config", cfg, "--filter", "good", "--matrix", "env=qa", "--out", filepath.Join(dir, "m2"))
	assert.NotEqual(t, cli.ExitOK, code)
	assert.Contains(t, stderr, `unknown environment "qa"`)
}
