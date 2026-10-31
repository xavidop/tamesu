package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

func TestListTable(t *testing.T) {
	code, out, stderr := run(t, nil, cli.Deps{}, "list", specSuite)
	assert.Equal(t, cli.ExitOK, code, stderr)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 5)
	assert.Regexp(t, `^SUITE\s+TEST\s+TARGET\s+TYPE\s+TAGS$`, lines[0])
	assert.Regexp(t, `^support-agent\s+refund policy question\s+api\s+rest\s+smoke$`, lines[2])
	assert.Regexp(t, `^support-agent\s+phone IVR routes to billing\s+phone\s+sip\s+smoke$`, lines[4])
	assert.Empty(t, stderr, "warnings are not printed by list")
}

func TestListJSONAndSelection(t *testing.T) {
	code, out, _ := run(t, nil, cli.Deps{}, "list", "--json", specSuite)
	assert.Equal(t, cli.ExitOK, code)
	var items []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &items))
	require.Len(t, items, 4)
	assert.Equal(t, "refund policy question", items[1]["name"])
	assert.Equal(t, "rest", items[1]["type"])
	assert.Equal(t, specSuite, items[1]["file"])
	assert.Equal(t, []any{"smoke"}, items[1]["tags"])

	code, out, _ = run(t, nil, cli.Deps{}, "list", "--json", "--filter", "refund|voice", specSuite)
	assert.Equal(t, cli.ExitOK, code)
	require.NoError(t, json.Unmarshal([]byte(out), &items))
	assert.Len(t, items, 2)

	code, out, _ = run(t, nil, cli.Deps{}, "list", "--json", "--target", "phone", specSuite)
	assert.Equal(t, cli.ExitOK, code)
	require.NoError(t, json.Unmarshal([]byte(out), &items))
	assert.Len(t, items, 1)

	code, out, _ = run(t, nil, cli.Deps{}, "list", "--json", "--tags", "nightly", specSuite)
	assert.Equal(t, cli.ExitOK, code)
	assert.Equal(t, "[]\n", out)

	code, _, stderr := run(t, nil, cli.Deps{}, "list", "--filter", "(", specSuite)
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "error: --filter: ")
}

func TestListInvalidSuite(t *testing.T) {
	code, out, stderr := run(t, nil, cli.Deps{}, "list", "../config/testdata/invalid/repeat-zero.yaml")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Empty(t, out)
	assert.Contains(t, stderr, "repeat-zero.yaml:7:")
}
