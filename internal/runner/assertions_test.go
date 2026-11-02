package runner

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/xavidop/tamesu/internal/config"
)

func TestRenderAssertion(t *testing.T) {
	render := func(s string) (string, error) { return strings.ReplaceAll(s, "{{vars.expect}}", "30 days"), nil }
	in := config.Assertion{Contains: ptr("says {{vars.expect}}"), Judge: &config.Judge{Rubric: "mentions {{vars.expect}}"}}
	out := renderAssertion(in, render)
	assert.Equal(t, "says 30 days", *out.Contains)
	assert.Equal(t, "mentions 30 days", out.Judge.Rubric)
	assert.Equal(t, "says {{vars.expect}}", *in.Contains, "the config is not modified")
	assert.Equal(t, "mentions {{vars.expect}}", in.Judge.Rubric)
}

