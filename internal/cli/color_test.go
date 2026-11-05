package cli_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/xavidop/tamesu/internal/cli"
)

// TestColorFlag: --color always colours the error line even on a buffer,
// never and the default (a buffer is not a terminal) leave it plain.
func TestColorFlag(t *testing.T) {
	run := func(args ...string) string {
		var out, errOut bytes.Buffer
		root := cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &out, Err: &errOut}, cli.Deps{LookupEnv: func(string) (string, bool) { return "", false }})
		code := cli.Execute(context.Background(), root, append([]string{"validate", "--config", "/nowhere/tamesu.yaml"}, args...))
		assert.Equal(t, cli.ExitConfig, code)
		return errOut.String()
	}
	assert.Contains(t, run("--color", "always"), "\x1b[1m\x1b[31merror:\x1b[0m ")
	assert.NotContains(t, run("--color", "never"), "\x1b[")
	assert.NotContains(t, run(), "\x1b[", "a buffer is not a terminal")
}
