package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/xavidop/tamesu/internal/cli"
)

func TestRootHelp(t *testing.T) {
	code, out, _ := run(t, nil, cli.Deps{})
	assert.Equal(t, cli.ExitOK, code)
	for _, cmd := range []string{"init", "validate", "list", "doctor", "version"} {
		assert.Contains(t, out, "\n  "+cmd)
	}
	assert.Contains(t, out, "Test your agents the way your users will.")
}

func TestUnknownCommand(t *testing.T) {
	code, _, stderr := run(t, nil, cli.Deps{}, "bogus")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Equal(t, "error: unknown command \"bogus\" for \"tamesu\"\n", stderr)
}

func TestUnknownFlag(t *testing.T) {
	code, _, stderr := run(t, nil, cli.Deps{}, "validate", "--nope")
	assert.Equal(t, cli.ExitConfig, code)
	assert.Contains(t, stderr, "error: unknown flag: --nope")
}

func TestExitCode(t *testing.T) {
	assert.Equal(t, 0, cli.ExitCode(nil))
	assert.Equal(t, 3, cli.ExitCode(assert.AnError))
}
