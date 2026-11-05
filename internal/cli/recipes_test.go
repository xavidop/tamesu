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

// TestRecipesValidate loads every YAML block of every recipes page: a block
// starting with "# tamesu.yaml" is the project of the suites that follow it
// on that page, every other block is a suite that must validate against it.
func TestRecipesValidate(t *testing.T) {
	pages, err := filepath.Glob(filepath.Join("..", "..", "site", "src", "pages", "docs", "recipes", "*.md"))
	if err != nil || len(pages) == 0 {
		t.Skip("no recipes pages:", err)
	}
	suites := 0
	for _, page := range pages {
		b, err := os.ReadFile(page)
		require.NoError(t, err)
		blocks := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(string(b), -1)
		if filepath.Base(page) == "index.md" {
			require.Empty(t, blocks, "the index lists the pages; recipes live on them")
			continue
		}
		require.NotEmpty(t, blocks, page)
		dir := t.TempDir()
		project := ""
		for i, m := range blocks {
			body := m[1]
			if strings.HasPrefix(body, "# tamesu.yaml") {
				project = filepath.Join(dir, "project-"+itoa(i), "tamesu.yaml")
				require.NoError(t, os.MkdirAll(filepath.Dir(project), 0o755))
				require.NoError(t, os.WriteFile(project, []byte(body), 0o644))
				continue
			}
			require.NotEmpty(t, project, "%s: a suite block before any tamesu.yaml block", page)
			suitePath := filepath.Join(filepath.Dir(project), "suite-"+itoa(i)+".yaml")
			require.NoError(t, os.WriteFile(suitePath, []byte(body), 0o644))
			var out, errOut bytes.Buffer
			root := cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &out, Err: &errOut}, cli.Deps{LookupEnv: func(string) (string, bool) { return "", false }})
			code := cli.Execute(context.Background(), root, []string{"validate", "--config", project, suitePath})
			require.Equal(t, cli.ExitOK, code, "%s block %d does not validate:\n%s%s", page, i, out.String(), errOut.String())
			suites++
		}
	}
	require.GreaterOrEqual(t, suites, 20)
}

func itoa(i int) string { return strconv.Itoa(i) }
