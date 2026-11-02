package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSchemaFilesAreUpToDate fails when the committed schemas differ from
// what the Go structs generate: run `make schema`.
func TestSchemaFilesAreUpToDate(t *testing.T) {
	for file, gen := range map[string]func() []byte{
		"../../schema/tamesu.schema.json":        func() []byte { b, _ := SchemaJSON(SuiteSchema()); return b },
		"../../schema/tamesu-config.schema.json": func() []byte { b, _ := SchemaJSON(ProjectSchema()); return b },
		"../../site/src/pages/docs/suites.md":    SuitesMarkdown,
	} {
		committed, err := os.ReadFile(file)
		require.NoError(t, err, "run make schema")
		assert.Equal(t, string(gen()), string(committed), "%s is stale: run make schema", file)
	}
}

func TestSuiteSchemaKnowsEveryAssertionKind(t *testing.T) {
	s := SuiteSchema()
	def, ok := s.Definitions["Assertion"]
	require.True(t, ok, "schema has no Assertion definition; have %v", keys(s.Definitions))
	for _, kind := range AssertionKinds {
		_, has := def.Properties.Get(kind)
		assert.True(t, has, "Assertion schema lacks %q", kind)
	}
	_, has := def.Properties.Get("on_turn")
	assert.True(t, has)
	target := s.Definitions["Target"]
	require.NotNil(t, target)
	typ, _ := target.Properties.Get("type")
	require.NotNil(t, typ)
	var enum []string
	for _, v := range typ.Enum {
		enum = append(enum, v.(string))
	}
	assert.Equal(t, TargetTypes(), enum)
}

func TestSuitesMarkdownCoversEveryDefinition(t *testing.T) {
	md := string(SuitesMarkdown())
	for name := range SuiteSchema().Definitions {
		assert.Contains(t, md, "\n## "+name+"\n", "suites.md has no section for %s", name)
	}
	assert.Contains(t, md, "| `judge` | [Judge](#judge) |")
	assert.Contains(t, md, "| `targets` | map of string to [Target](#target) | yes |")
	assert.Contains(t, md, "| `type` | one of `browser`, `rest`, `livekit`, `sip`, `realtime` | yes |")
	assert.Contains(t, md, "Either a string (The rubric; the judge model and a binary scale are the defaults), or an object with:")
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
