package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

const minimalSuite = `suite: mini
targets:
  api: { type: rest, url: http://localhost:8080/chat, response: { text: $.reply } }
tests:
  - name: t
    target: api
    scenario: { goal: say hi }
`

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return dir
}

func TestValidateSpecSuite(t *testing.T) {
	code, out, stderr := run(t, nil, cli.Deps{}, "validate", specSuite)
	assert.Equal(t, cli.ExitOK, code, stderr)
	assert.Contains(t, out, specSuite+":35: warning: targets.voice.url: env var LIVEKIT_URL is not set")
	assert.Contains(t, out, "1 suite, 4 tests, 0 errors, 1 warning\n")

	code, out, _ = run(t, nil, cli.Deps{}, "validate", "--strict", specSuite)
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, out, "1 warning")

	code, out, _ = run(t, map[string]string{"LIVEKIT_URL": "wss://lk"}, cli.Deps{}, "validate", "--strict", specSuite)
	assert.Equal(t, cli.ExitOK, code)
	assert.Equal(t, "1 suite, 4 tests, 0 errors, 0 warnings\n", out)
}

func TestValidateInvalidSuite(t *testing.T) {
	code, out, _ := run(t, nil, cli.Deps{}, "validate", "../config/testdata/invalid/missing-target.yaml")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, out, `../config/testdata/invalid/missing-target.yaml:8: tests[0] "refund".target: target "nope" is not declared in targets (have api)`)
	assert.Contains(t, out, "1 suite, 1 test, 1 error, 1 warning\n")

	code, out, _ = run(t, nil, cli.Deps{}, "validate", "../config/testdata/invalid/not-a-suite.yaml")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, out, `not-a-suite.yaml:1: not a tamesu suite (no "suite:" key)`)
}

func TestValidateUsesProjectSuites(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"tamesu.yaml":      "version: 1\nsuites: [\"suites/**/*.yaml\"]\n",
		"suites/a.yaml":    minimalSuite,
		"suites/b/c.yaml":  minimalSuite,
		"elsewhere/d.yaml": minimalSuite,
	})
	code, out, stderr := run(t, nil, cli.Deps{}, "validate", "--config", filepath.Join(dir, "tamesu.yaml"))
	assert.Equal(t, cli.ExitOK, code, stderr)
	assert.Equal(t, "2 suites, 2 tests, 0 errors, 0 warnings\n", out)

	// Paths on the command line win over suites:.
	code, out, _ = run(t, nil, cli.Deps{}, "validate", "--config", filepath.Join(dir, "tamesu.yaml"), filepath.Join(dir, "elsewhere", "d.yaml"))
	assert.Equal(t, cli.ExitOK, code)
	assert.Equal(t, "1 suite, 1 test, 0 errors, 0 warnings\n", out)
}

func TestValidateNoSuitesMatch(t *testing.T) {
	dir := writeProject(t, map[string]string{"tamesu.yaml": "version: 1\n"})
	code, _, stderr := run(t, nil, cli.Deps{}, "validate", "--config", filepath.Join(dir, "tamesu.yaml"))
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, `error: no suites match "tests/**/*.yaml"`)
}

func TestValidateMissingConfigFile(t *testing.T) {
	code, _, stderr := run(t, nil, cli.Deps{}, "validate", "--config", "nope/tamesu.yaml", specSuite)
	assert.Equal(t, cli.ExitConfig, code)
	assert.Equal(t, "error: config file nope/tamesu.yaml does not exist\n", stderr)
}

func TestValidateBadProject(t *testing.T) {
	dir := writeProject(t, map[string]string{"tamesu.yaml": "version: 3\nsuites: [\"a.yaml\"]\n", "a.yaml": minimalSuite})
	code, out, _ := run(t, nil, cli.Deps{}, "validate", "--config", filepath.Join(dir, "tamesu.yaml"))
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, out, "tamesu.yaml:1: version: version must be 1")
}
