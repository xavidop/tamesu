package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

const varsProject = `version: 1
suites: ["s/**/*.yaml"]
budgets: { max_cost_usd: 2 }
vars: { who: marta, where: lobby }
hooks:
  before_all:
    - command: 'printf "%s/%s" "{{vars.who}}" "{{vars.where}}" > "$TAMESU_OUT_DIR/vars.txt"'
environments:
  staging:
    vars: { who: ken }
`

// The project's vars reach the hooks and the suites; the suite's win over
// them, the environment's over both, and --var over everything.
func TestRunProjectVars(t *testing.T) {
	deps, _, _ := runDeps(t)
	suite := strings.Replace(runSuite, "suite: run-suite\n", "suite: run-suite\nvars: { where: desk }\n", 1)
	dir := writeProject(t, map[string]string{"tamesu.yaml": varsProject, "s/suite.yaml": suite})
	cfg := filepath.Join(dir, "tamesu.yaml")
	env := map[string]string{"TOKEN": "t"}

	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"project":     {nil, "marta/desk"},
		"environment": {[]string{"--env", "staging"}, "ken/desk"},
		"flag":        {[]string{"--env", "staging", "--var", "who=ana", "--var", "where=roof"}, "ana/roof"},
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(dir, name)
			args := append([]string{"run", "--config", cfg, "--filter", "good", "--out", out}, tc.args...)
			code, stdout, stderr := run(t, env, deps, args...)
			require.Equal(t, cli.ExitOK, code, stdout+stderr)
			got, err := os.ReadFile(filepath.Join(out, "vars.txt"))
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
		})
	}
}
