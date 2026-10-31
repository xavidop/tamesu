package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

func TestInitThenValidate(t *testing.T) {
	dir := t.TempDir()
	code, out, stderr := run(t, nil, cli.Deps{}, "init", dir)
	require.Equal(t, cli.ExitOK, code, stderr)
	for _, f := range []string{"tamesu.yaml", "examples/support-agent.yaml", "examples/README.md"} {
		_, err := os.Stat(filepath.Join(dir, f))
		assert.NoError(t, err, f)
		assert.Contains(t, out, "created "+filepath.Join(dir, f))
	}

	code, out, stderr = run(t, nil, cli.Deps{}, "validate", "--config", filepath.Join(dir, "tamesu.yaml"))
	assert.Equal(t, cli.ExitOK, code, stderr)
	assert.Equal(t, "1 suite, 2 tests, 0 errors, 0 warnings\n", out)

	code, out, _ = run(t, nil, cli.Deps{}, "list", "--config", filepath.Join(dir, "tamesu.yaml"))
	assert.Equal(t, cli.ExitOK, code)
	assert.Contains(t, out, "reschedule booking by voice")
}

func TestInitRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tamesu.yaml"), []byte("version: 1\n"), 0o644))
	code, _, stderr := run(t, nil, cli.Deps{}, "init", dir)
	assert.Equal(t, cli.ExitConfig, code)
	assert.Equal(t, "error: "+filepath.Join(dir, "tamesu.yaml")+" already exists; use --force to overwrite\n", stderr)
	_, err := os.Stat(filepath.Join(dir, "examples", "support-agent.yaml"))
	assert.True(t, os.IsNotExist(err), "nothing is written when init refuses")

	code, _, stderr = run(t, nil, cli.Deps{}, "init", "--force", dir)
	assert.Equal(t, cli.ExitOK, code, stderr)
	b, err := os.ReadFile(filepath.Join(dir, "tamesu.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "suites:")
}
