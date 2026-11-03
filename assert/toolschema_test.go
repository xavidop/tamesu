package assert

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/target"
)

func TestToolCalledWithSchema(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"required":             []any{"day", "guests"},
		"additionalProperties": false,
		"properties": map[string]any{
			"day":    map[string]any{"type": "string", "enum": []any{"saturday", "sunday"}},
			"guests": map[string]any{"type": "integer", "minimum": 1, "maximum": 8},
		},
	}
	good := target.ToolCall{Name: "book", Args: map[string]any{"day": "saturday", "guests": 4}}
	bad := target.ToolCall{Name: "book", Args: map[string]any{"day": "monday", "guests": 12}}
	other := target.ToolCall{Name: "lookup", Args: map[string]any{"q": 1}}

	a, err := ToolCalledWithSchema("book", nil, schema)
	require.NoError(t, err)
	assert.Equal(t, "tool_called book (schema)", a.Name())

	res := a.Evaluate(context.Background(), &RunResult{ToolCalls: []target.ToolCall{other, good}})
	assert.True(t, res.Pass, res.Reason)
	assert.Equal(t, "every call to book had arguments the schema accepts", res.Reason)

	res = a.Evaluate(context.Background(), &RunResult{ToolCalls: []target.ToolCall{good, bad}})
	assert.False(t, res.Pass, "one rejected call fails the assertion whatever the others looked like")
	assert.Contains(t, res.Reason, "arguments the schema rejects")
	assert.Contains(t, res.Reason, "/day:")
	assert.Contains(t, res.Reason, "/guests:")
	assert.Equal(t, []string{`{"day":"monday","guests":12}`}, res.Evidence)

	res = a.Evaluate(context.Background(), &RunResult{ToolCalls: []target.ToolCall{other}})
	assert.False(t, res.Pass)
	assert.Equal(t, "the agent did not call book (tool calls: lookup)", res.Reason)

	res = a.Evaluate(context.Background(), &RunResult{ToolCalls: []target.ToolCall{{Name: "book"}}})
	assert.False(t, res.Pass, "a call without arguments misses the required keys")
	assert.Contains(t, res.Reason, "the arguments:")

	withArgs, err := ToolCalledWithSchema("book", map[string]any{"day": "sunday"}, schema)
	require.NoError(t, err)
	res = withArgs.Evaluate(context.Background(), &RunResult{ToolCalls: []target.ToolCall{good}})
	assert.False(t, res.Pass, "valid arguments that do not match args are not the call asked for")
	assert.Contains(t, res.Reason, "did not call book{\"day\":\"sunday\"}")

	plain, err := ToolCalledWithSchema("book", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "tool_called book", plain.Name(), "no schema is the plain assertion")

	_, err = ToolCalledWithSchema("book", nil, map[string]any{"type": "nope"})
	assert.ErrorContains(t, err, "schema")
}

func TestCompileSchemaNestedAndNumbers(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"items": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"qty": map[string]any{"type": "number"}}}},
	}}
	a, err := ToolCalledWithSchema("cart", nil, schema)
	require.NoError(t, err)
	call := target.ToolCall{Name: "cart", Args: map[string]any{"items": []any{map[string]any{"qty": int64(2)}, map[string]any{"qty": 1.5}}}}
	res := a.Evaluate(context.Background(), &RunResult{ToolCalls: []target.ToolCall{call}})
	assert.True(t, res.Pass, "integers a target reports count as JSON numbers: %s", res.Reason)
}
