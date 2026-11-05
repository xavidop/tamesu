package cli_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/xavidop/tamesu/internal/cli"
	"github.com/xavidop/tamesu/report"
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

// TestColorHelpAndVersion: the help screen and the version line are coloured
// with --color always and plain otherwise, with the same text.
func TestColorHelpAndVersion(t *testing.T) {
	run := func(args ...string) string {
		var out bytes.Buffer
		root := cli.NewRoot(cli.BuildInfo{Version: "1.2.3", Commit: "abc", Date: "today"}, cli.Streams{Out: &out, Err: &out}, cli.Deps{LookupEnv: func(string) (string, bool) { return "", false }})
		assert.Equal(t, cli.ExitOK, cli.Execute(context.Background(), root, args))
		return out.String()
	}
	plain, coloured := run("--help"), run("--help", "--color", "always")
	assert.NotContains(t, plain, "\x1b[")
	assert.Contains(t, coloured, "\x1b[1mAvailable Commands:\x1b[0m")
	assert.Contains(t, coloured, "\x1b[1mtamesu: Your agents, tested like users would.\x1b[0m")
	assert.Contains(t, coloured, "\x1b[36m--color")
	assert.Equal(t, plain, report.Strip(coloured), "colours change nothing but the codes")

	plain, coloured = run("version"), run("version", "--color", "always")
	assert.Equal(t, "tamesu 1.2.3 (abc, today)\n", plain)
	assert.Contains(t, coloured, "\x1b[1m\x1b[32m1.2.3\x1b[0m")
	assert.Equal(t, plain, report.Strip(coloured))
}
