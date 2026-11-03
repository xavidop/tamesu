package runner

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestBuildSemanticAndSchema(t *testing.T) {
	a, err := Build(config.Assertion{SimilarTo: &config.Similar{Text: "Returns within 30 days", Min: 0.7}})
	require.NoError(t, err)
	assert.Equal(t, `similar_to "Returns within 30 days"`, a.Name())

	a, err = Build(config.Assertion{ToolCalled: &config.ToolCall{Name: "book", Schema: map[string]any{"type": "object"}}})
	require.NoError(t, err)
	assert.Equal(t, "tool_called book (schema)", a.Name())

	a, err = Build(config.Assertion{ToolCalled: &config.ToolCall{Name: "book"}})
	require.NoError(t, err)
	assert.Equal(t, "tool_called book", a.Name(), "no schema is the plain assertion")

	_, err = Build(config.Assertion{ToolCalled: &config.ToolCall{Name: "book", Schema: map[string]any{"type": "nope"}}})
	assert.ErrorContains(t, err, "schema")
}
