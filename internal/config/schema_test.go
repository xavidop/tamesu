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

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
