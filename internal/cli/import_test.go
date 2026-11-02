package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

const chatsJSON = `[
  {"id": "refund", "turns": [
    {"role": "assistant", "text": "Welcome!"},
    {"role": "user", "text": "hi"},
    {"role": "assistant", "text": "echo: hi"},
    {"role": "user", "text": "where is my refund"},
    {"role": "assistant", "text": "echo: where is my refund"}
  ]},
  {"id": "silent", "turns": [{"role": "assistant", "text": "Hello?"}]}
]`

func TestImportConversations(t *testing.T) {
	deps, _, _ := runDeps(t)
	dir := writeProject(t, map[string]string{
		"chats.json":  chatsJSON,
		"tamesu.yaml": "version: 1\nsuites: [\"imported/*.yaml\"]\n",
	})
	cfg := filepath.Join(dir, "tamesu.yaml")
	out := filepath.Join(dir, "imported", "chats.yaml")

	code, stdout, stderr := run(t, nil, deps, "import", filepath.Join(dir, "chats.json"), "--config", cfg, "--target", "api", "--out", out, "--golden-dir", "golden", "--golden-mode", "exact")
	assert.Equal(t, cli.ExitOK, code, stdout+stderr)
	assert.Contains(t, stdout, "1 test(s) from 2 conversation(s) for target api (rest), 1 reference file(s) under")
	assert.Contains(t, stderr, `skipped "silent"`)
	suite, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(suite), "- say: hi")
	assert.Contains(t, string(suite), "file: golden/refund.json")
	golden, err := os.ReadFile(filepath.Join(dir, "imported", "golden", "refund.json"))
	require.NoError(t, err)
	assert.JSONEq(t, `["echo: hi", "echo: where is my refund"]`, string(golden))

	// The imported suite replays against the agent, with no model at all.
	noAI := deps
	noAI.NewProvider = nil
	code, stdout, _ = run(t, map[string]string{"AGENT_URL": "http://agent"}, noAI, "run", "--config", cfg, "--out", filepath.Join(dir, "run"))
	assert.Equal(t, cli.ExitOK, code, stdout)
	assert.Contains(t, stdout, "PASS   chats / refund")

	// Printed when no --out; inline turns.
	code, stdout, _ = run(t, nil, deps, "import", filepath.Join(dir, "chats.json"), "--config", cfg, "--target", "api")
	assert.Equal(t, cli.ExitOK, code, stdout)
	assert.Contains(t, stdout, "turns:")
	assert.Contains(t, stdout, "mode: similar")

	code, _, stderr = run(t, nil, deps, "import", filepath.Join(dir, "chats.json"), "--config", cfg)
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "--target <name> is required")
	code, _, stderr = run(t, nil, deps, "import", filepath.Join(dir, "chats.json"), "--config", cfg, "--target", "web", "--type", "browser")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "conversational target type")
	code, _, stderr = run(t, nil, deps, "import", filepath.Join(dir, "chats.json"), "--config", cfg, "--target", "api", "--golden-dir", "g")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "--golden-dir needs --out")
	code, _, stderr = run(t, nil, deps, "import", filepath.Join(dir, "nope.csv"), "--config", cfg, "--target", "api")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "nope.csv")
}
