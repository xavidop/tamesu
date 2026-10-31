package config

import (
	"encoding/json"

	"github.com/invopop/jsonschema"
)

// SchemaBaseURL is where the generated schemas are published.
const SchemaBaseURL = "https://xavidop.github.io/tamesu/schema/"

func newReflector() *jsonschema.Reflector {
	return &jsonschema.Reflector{
		ExpandedStruct:             true,
		RequiredFromJSONSchemaTags: true,
	}
}

// SuiteSchema is the JSON Schema of a suite file (schema/tamesu.schema.json).
func SuiteSchema() *jsonschema.Schema {
	s := newReflector().Reflect(&Suite{})
	s.ID = jsonschema.ID(SchemaBaseURL + "tamesu.schema.json")
	s.Title = "tamesu suite"
	s.Description = "A tamesu suite: targets, defaults and tests. See https://xavidop.github.io/tamesu/docs/suites/."
	return s
}

// ProjectSchema is the JSON Schema of tamesu.yaml (schema/tamesu-config.schema.json).
func ProjectSchema() *jsonschema.Schema {
	s := newReflector().Reflect(&Project{})
	s.ID = jsonschema.ID(SchemaBaseURL + "tamesu-config.schema.json")
	s.Title = "tamesu.yaml"
	s.Description = "tamesu project configuration. See https://xavidop.github.io/tamesu/docs/configuration/."
	return s
}

// SchemaJSON renders a schema as indented JSON with a trailing newline, the
// form committed under schema/.
func SchemaJSON(s *jsonschema.Schema) ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
