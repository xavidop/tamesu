package config

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/invopop/jsonschema"
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
	assert.Contains(t, md, "| `type` | one of `browser`, `rest`, `livekit`, `sip`, `realtime`, `grpc`, `pipecat`, `model`, `twilio`, `mcp`, `a2a`, `voice` | yes |")
	assert.Contains(t, md, "Either a string (The rubric; the judge model and a binary scale are the defaults), or an object with:")
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSuitesMarkdownFieldExamples: every key of every definition has an
// example line in the YAML reference.
func TestSuitesMarkdownFieldExamples(t *testing.T) {
	s := SuiteSchema()
	check := func(def string, sch *jsonschema.Schema) {
		if sch == nil || sch.Properties == nil {
			return
		}
		for pair := sch.Properties.Oldest(); pair != nil; pair = pair.Next() {
			ex := fieldExamples[def][pair.Key]
			assert.NotEmpty(t, ex, "fieldExamples[%q][%q]: no example for the YAML reference", def, pair.Key)
			assert.True(t, strings.HasPrefix(ex, pair.Key+":") || strings.HasPrefix(ex, "- "+pair.Key+":"), "fieldExamples[%q][%q] should start with the key: %s", def, pair.Key, ex)
		}
	}
	check("Suite", s)
	for name, def := range s.Definitions {
		check(name, def)
		if len(def.OneOf) == 2 {
			check(name, def.OneOf[1])
		}
	}
	for def := range fieldExamples {
		if def == "Suite" {
			continue
		}
		_, ok := s.Definitions[def]
		assert.True(t, ok, "fieldExamples has examples for %q, which is not a definition", def)
	}
	md := string(SuitesMarkdown())
	assert.Contains(t, md, "| Key | Type | Required | Description | Example |")
	assert.Contains(t, md, "| `max_turns` | integer |  | The turn budget; default 10 | `max_turns: 6` |")
}
