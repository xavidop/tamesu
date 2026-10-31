package cli_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/xavidop/tamesu/internal/cli"
)

func TestVersion(t *testing.T) {
	code, out, _ := run(t, nil, cli.Deps{}, "version")
	assert.Equal(t, 0, code)
	assert.Equal(t, "tamesu 1.2.3 (abc1234, 2026-10-31)\n", out)

	var buf bytes.Buffer
	root := cli.NewRoot(cli.BuildInfo{}, cli.Streams{Out: &buf}, cli.Deps{})
	assert.Equal(t, 0, cli.Execute(context.Background(), root, []string{"version"}))
	assert.Equal(t, "tamesu dev (none, unknown)\n", buf.String())
}
