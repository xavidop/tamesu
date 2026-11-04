package cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

func TestPlaybooks(t *testing.T) {
	deps, _, _ := runDeps(t)
	code, stdout, stderr := run(t, nil, deps, "playbooks")
	assert.Equal(t, cli.ExitOK, code, stderr)
	assert.Contains(t, stdout, "PLAYBOOK")
	assert.Contains(t, stdout, "prompt_injection")
	assert.Contains(t, stdout, "LLM01 Prompt Injection")
	assert.Contains(t, stdout, "8 playbooks")
	assert.Contains(t, stdout, "red_team:")

	code, stdout, stderr = run(t, nil, deps, "playbooks", "pii_extraction")
	assert.Equal(t, cli.ExitOK, code, stderr)
	assert.Contains(t, stdout, "pii_extraction: Personal data extraction (LLM02 Sensitive Information Disclosure)")
	assert.Contains(t, stdout, "pii_extraction/colleague_pretext")
	assert.Contains(t, stdout, "tactics:")
	assert.Contains(t, stdout, "asserts:  pack")

	code, stdout, _ = run(t, nil, deps, "playbooks", "--json")
	assert.Equal(t, cli.ExitOK, code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &list))
	assert.Len(t, list, 8)

	code, _, stderr = run(t, nil, deps, "playbooks", "nope")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, `unknown playbook "nope"`)
}
