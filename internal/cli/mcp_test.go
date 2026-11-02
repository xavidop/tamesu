package cli_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

func TestMCPExecRunsCommandsInProcess(t *testing.T) {
	var got cli.MCPRequest
	deps := cli.Deps{ServeMCP: func(_ context.Context, req cli.MCPRequest) error {
		got = req
		return nil
	}}
	code, _, stderr := run(t, nil, deps, "--config", "../config/testdata/valid/tamesu.yaml", "--var", "X=1", "mcp")
	require.Equal(t, 0, code, stderr)
	require.NotNil(t, got.Exec)
	assert.Equal(t, "1.2.3", got.Version)

	stdout, stderr, code := got.Exec(context.Background(), []string{"validate", specSuite})
	assert.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "1 suite")

	stdout, _, code = got.Exec(context.Background(), []string{"list", "--json", specSuite})
	assert.Equal(t, 0, code)
	var tests []map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &tests), stdout)
	assert.NotEmpty(t, tests)

	_, stderr, code = got.Exec(context.Background(), []string{"validate", "nope.yaml"})
	assert.Equal(t, 2, code)
	assert.NotEmpty(t, stderr)

	schema, err := got.Schema("suite")
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(schema), &parsed))
	assert.Contains(t, parsed, "$defs")
	_, err = got.Schema("config")
	require.NoError(t, err)
	_, err = got.Schema("other")
	assert.Error(t, err)
}

func TestMCPUnavailable(t *testing.T) {
	code, _, stderr := run(t, nil, cli.Deps{}, "mcp")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "not available in this build")
}

func TestMCPBadCwd(t *testing.T) {
	deps := cli.Deps{ServeMCP: func(context.Context, cli.MCPRequest) error { return nil }}
	code, _, stderr := run(t, nil, deps, "mcp", "--cwd", "/nonexistent/dir/for/tamesu")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "--cwd")
}
