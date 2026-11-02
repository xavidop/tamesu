package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStrictSchema(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"say":     map[string]any{"type": "string", "description": "what to say"},
			"dtmf":    map[string]any{"type": "string", "description": "digits"},
			"hang_up": map[string]any{"type": "boolean"},
			"tests": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":       "object",
					"properties": map[string]any{"name": map[string]any{"type": "string"}, "tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
					"required":   []any{"name"},
				},
			},
		},
		"required": []any{"say", "tests"},
	}
	before, _ := json.Marshal(in)
	got := strictSchema(in)
	after, _ := json.Marshal(in)
	assert.JSONEq(t, string(before), string(after), "the input is not modified")

	assert.Equal(t, false, got["additionalProperties"])
	assert.Equal(t, []string{"dtmf", "hang_up", "say", "tests"}, got["required"], "every property is required")
	props := got["properties"].(map[string]any)
	assert.Equal(t, map[string]any{"type": "string", "description": "what to say"}, props["say"], "a required property keeps its type")
	assert.Equal(t, []any{"string", "null"}, props["dtmf"].(map[string]any)["type"], "an optional property becomes nullable")
	assert.Equal(t, "digits", props["dtmf"].(map[string]any)["description"])
	assert.Equal(t, []any{"boolean", "null"}, props["hang_up"].(map[string]any)["type"])

	items := props["tests"].(map[string]any)["items"].(map[string]any)
	assert.Equal(t, false, items["additionalProperties"], "nested objects are rewritten")
	assert.Equal(t, []string{"name", "tags"}, items["required"])
	tags := items["properties"].(map[string]any)["tags"].(map[string]any)
	assert.Equal(t, []any{"array", "null"}, tags["type"])
	assert.Equal(t, map[string]any{"type": "string"}, tags["items"], "a scalar item schema is left alone")
}

func TestStrictSchemaFoldsPicoschemaOptional(t *testing.T) {
	// What Picoschema emits for `contains?(array): string` and `goal?: string`.
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"contains": map[string]any{ // no type of its own: the array branch carries it
				"items": map[string]any{"type": "string"}, "description": "phrases",
				"anyOf": []any{map[string]any{"type": "array"}, map[string]any{"type": "null"}},
			},
			"full": map[string]any{ // a complete non-null branch stays an anyOf
				"anyOf": []any{map[string]any{"type": "string", "description": "d"}, map[string]any{"type": "null"}},
			},
			"goal": map[string]any{"type": "string", "anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
		},
	}
	got := strictSchema(in)
	props := got["properties"].(map[string]any)
	contains := props["contains"].(map[string]any)
	assert.Equal(t, []any{"array", "null"}, contains["type"])
	assert.Equal(t, map[string]any{"type": "string"}, contains["items"], "items survive")
	assert.Equal(t, "phrases", contains["description"])
	assert.NotContains(t, contains, "anyOf", "the bare anyOf branches are gone")
	goal := props["goal"].(map[string]any)
	assert.Equal(t, []any{"string", "null"}, goal["type"])
	assert.NotContains(t, goal, "anyOf")
	full := props["full"].(map[string]any)
	assert.Contains(t, full, "anyOf")
	assert.NotContains(t, full, "type")
	assert.Equal(t, []string{"contains", "full", "goal"}, got["required"])
}

func TestStrictSchemaWrapsUntypedOptional(t *testing.T) {
	in := map[string]any{
		"type":       "object",
		"properties": map[string]any{"ref": map[string]any{"$ref": "#/$defs/x", "description": "d"}},
		"$defs":      map[string]any{"x": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "integer"}}}},
	}
	got := strictSchema(in)
	ref := got["properties"].(map[string]any)["ref"].(map[string]any)
	require.Contains(t, ref, "anyOf")
	assert.Equal(t, "d", ref["description"])
	branches := ref["anyOf"].([]any)
	assert.Equal(t, map[string]any{"type": "null"}, branches[1])
	x := got["$defs"].(map[string]any)["x"].(map[string]any)
	assert.Equal(t, false, x["additionalProperties"])
	assert.Equal(t, []string{"a"}, x["required"])
}

func TestWantsStrictSchema(t *testing.T) {
	assert.True(t, wantsStrictSchema("openai/gpt-4.1-mini"))
	assert.False(t, wantsStrictSchema("googleai/gemini-2.5-flash"))
	assert.False(t, wantsStrictSchema("anthropic/claude-sonnet-4-5"))
	assert.False(t, wantsStrictSchema("fake/simulator"))
}
