package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/cli"
)

// TestRecipesValidate loads every YAML block of the recipes page: a block
// starting with "# tamesu.yaml" is the project of the suites that follow it,
// every other block is a suite that must validate against that project.
func TestRecipesValidate(t *testing.T) {
	page := filepath.Join("..", "..", "site", "src", "pages", "docs", "recipes.md")
	b, err := os.ReadFile(page)
	if err != nil {
		t.Skip("no recipes page:", err)
	}
	blocks := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(string(b), -1)
	require.NotEmpty(t, blocks)
	dir := t.TempDir()
	project := ""
	suites := 0
	for i, m := range blocks {
		body := m[1]
		if strings.HasPrefix(body, "# tamesu.yaml") {
			project = filepath.Join(dir, "project-"+itoa(i), "tamesu.yaml")
			require.NoError(t, os.MkdirAll(filepath.Dir(project), 0o755))
			require.NoError(t, os.WriteFile(project, []byte(body), 0o644))
			continue
		}
		require.NotEmpty(t, project, "a suite block before any tamesu.yaml block")
		suitePath := filepath.Join(filepath.Dir(project), "suite-"+itoa(i)+".yaml")
		require.NoError(t, os.WriteFile(suitePath, []byte(body), 0o644))
		var out, errOut bytes.Buffer
		root := cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &out, Err: &errOut}, cli.Deps{LookupEnv: func(string) (string, bool) { return "", false }})
		code := cli.Execute(context.Background(), root, []string{"validate", "--config", project, suitePath})
		require.Equal(t, cli.ExitOK, code, "recipe block %d does not validate:\n%s%s", i, out.String(), errOut.String())
		suites++
	}
	require.GreaterOrEqual(t, suites, 5)
}

func itoa(i int) string { return strconv.Itoa(i) }
