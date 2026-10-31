package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

// This file holds the YAML types that accept a short form and a long form:
// `judge: "rubric"` or `judge: { rubric: ..., scale: 1-5 }`, `tool_called: name`
// or `tool_called: { name, args }`, `call_ended_by: agent` or a list, and
// `on_turn: 3` or `on_turn: last`. Each decodes both forms and describes both
// in the JSON Schema with oneOf.

// checkKeys fails on a mapping key that is not in allowed, in the same words
// yaml.v3 uses for unknown struct fields, so the loader reports it with the
// line number like any other unknown field. Custom UnmarshalYAML methods
// bypass the decoder's KnownFields setting, hence this check.
func checkKeys(n *yaml.Node, typ string, allowed ...string) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: cannot unmarshal %s into %s", n.Line, nodeKind(n), typ)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if !slices.Contains(allowed, k.Value) {
			return fmt.Errorf("line %d: field %s not found in type %s", k.Line, k.Value, typ)
		}
	}
	return nil
}

func nodeKind(n *yaml.Node) string {
	switch n.Kind {
	case yaml.ScalarNode:
		return "a scalar"
	case yaml.SequenceNode:
		return "a list"
	case yaml.MappingNode:
		return "a mapping"
	default:
		return "a document"
	}
}

// oneOfStringOr builds a schema that accepts a plain string or the object
// described by the fields of obj.
func oneOfStringOr(obj any, stringDesc string) *jsonschema.Schema {
	r := &jsonschema.Reflector{ExpandedStruct: true, DoNotReference: true, RequiredFromJSONSchemaTags: true}
	o := r.Reflect(obj)
	o.Version = ""
	o.ID = ""
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		{Type: "string", Description: stringDesc},
		o,
	}}
}

// Judge is an LLM-judged assertion: a rubric and how to score it.
type Judge struct {
	Rubric string  `yaml:"rubric" json:"rubric" jsonschema:"required,description=What the judge checks in plain language."`
	Model  string  `yaml:"model,omitempty" json:"model,omitempty" jsonschema:"description=Genkit model reference; defaults to models.judge."`
	Scale  string  `yaml:"scale,omitempty" json:"scale,omitempty" jsonschema:"enum=binary,enum=1-5,description=binary (pass or fail) or 1-5 with min."`
	Min    float64 `yaml:"min,omitempty" json:"min,omitempty" jsonschema:"description=Minimum score to pass on the 1-5 scale."`
}

// UnmarshalYAML accepts a rubric string or a mapping.
func (j *Judge) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		j.Rubric = n.Value
		return nil
	}
	if err := checkKeys(n, "Judge", "rubric", "model", "scale", "min"); err != nil {
		return err
	}
	type plain Judge
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*j = Judge(p)
	return nil
}

// JSONSchema describes the string and object forms.
func (Judge) JSONSchema() *jsonschema.Schema {
	type plain Judge
	return oneOfStringOr(&plain{}, "The rubric; the judge model and a binary scale are the defaults.")
}

// ToolCall names a tool the agent must (or must not) have called, with
// optional argument values that must match.
type ToolCall struct {
	Name string         `yaml:"name" json:"name" jsonschema:"required,description=Tool name as the target reports it."`
	Args map[string]any `yaml:"args,omitempty" json:"args,omitempty" jsonschema:"description=Argument values that must match (a subset of the call's arguments)."`
}

// UnmarshalYAML accepts a tool name or a mapping.
func (c *ToolCall) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		c.Name = n.Value
		return nil
	}
	if err := checkKeys(n, "ToolCall", "name", "args"); err != nil {
		return err
	}
	type plain ToolCall
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*c = ToolCall(p)
	return nil
}

// JSONSchema describes the string and object forms.
func (ToolCall) JSONSchema() *jsonschema.Schema {
	type plain ToolCall
	return oneOfStringOr(&plain{}, "The tool name.")
}

// Evaluator names a registered Genkit evaluator and the minimum score.
type Evaluator struct {
	Name string  `yaml:"name" json:"name" jsonschema:"required,description=Genkit evaluator name such as genkitEval/faithfulness."`
	Min  float64 `yaml:"min,omitempty" json:"min,omitempty" jsonschema:"description=Minimum score to pass (0 to 1)."`
}

// UnmarshalYAML accepts an evaluator name or a mapping.
func (e *Evaluator) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		e.Name = n.Value
		return nil
	}
	if err := checkKeys(n, "Evaluator", "name", "min"); err != nil {
		return err
	}
	type plain Evaluator
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*e = Evaluator(p)
	return nil
}

// JSONSchema describes the string and object forms.
func (Evaluator) JSONSchema() *jsonschema.Schema {
	type plain Evaluator
	return oneOfStringOr(&plain{}, "The evaluator name.")
}

// StringList is a list of strings that also accepts a single string.
type StringList []string

// UnmarshalYAML accepts a string or a list of strings.
func (l *StringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*l = StringList{n.Value}
		return nil
	case yaml.SequenceNode:
		var s []string
		if err := n.Decode(&s); err != nil {
			return err
		}
		*l = StringList(s)
		return nil
	default:
		return fmt.Errorf("line %d: expected a string or a list of strings", n.Line)
	}
}

// JSONSchema describes the string and list forms.
func (StringList) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		{Type: "string"},
		{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
	}}
}

// OnTurn attaches an assertion to one agent turn: a 1-based number or the
// last turn. The zero value means the whole test.
type OnTurn struct {
	Last bool
	N    int
}

// IsSet reports whether on_turn was given.
func (o OnTurn) IsSet() bool { return o.Last || o.N > 0 }

// String renders "last", the turn number, or "" when unset.
func (o OnTurn) String() string {
	switch {
	case o.Last:
		return "last"
	case o.N > 0:
		return strconv.Itoa(o.N)
	default:
		return ""
	}
}

// UnmarshalYAML accepts a positive integer or "last".
func (o *OnTurn) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: on_turn must be a turn number (1 or more) or \"last\"", n.Line)
	}
	if n.Value == "last" {
		*o = OnTurn{Last: true}
		return nil
	}
	v, err := strconv.Atoi(n.Value)
	if err != nil || v < 1 {
		return fmt.Errorf("line %d: on_turn must be a turn number (1 or more) or \"last\", not %q", n.Line, n.Value)
	}
	*o = OnTurn{N: v}
	return nil
}

// MarshalYAML encodes "last", the number, or nothing.
func (o OnTurn) MarshalYAML() (any, error) {
	if o.Last {
		return "last", nil
	}
	if o.N > 0 {
		return o.N, nil
	}
	return nil, nil
}

// MarshalJSON encodes "last", the number, or null.
func (o OnTurn) MarshalJSON() ([]byte, error) {
	v, _ := o.MarshalYAML()
	return json.Marshal(v)
}

// IsZero lets yaml omitempty drop an unset on_turn.
func (o OnTurn) IsZero() bool { return !o.IsSet() }

// JSONSchema describes the integer and "last" forms.
func (OnTurn) JSONSchema() *jsonschema.Schema {
	one := json.Number("1")
	return &jsonschema.Schema{
		Description: "Evaluate the assertion on one agent turn: a 1-based number or last.",
		OneOf: []*jsonschema.Schema{
			{Type: "integer", Minimum: one},
			{Type: "string", Enum: []any{"last"}},
		},
	}
}

// JSONPathAssertion checks a JSONPath of every REST response.
type JSONPathAssertion struct {
	Path   string `yaml:"path" json:"path" jsonschema:"required,description=JSONPath into the response body such as $.status."`
	Equals any    `yaml:"equals,omitempty" json:"equals,omitempty" jsonschema:"description=The value the path must equal."`
}

// DTMFAssertion checks the digits the agent sent.
type DTMFAssertion struct {
	Equals string `yaml:"equals" json:"equals" jsonschema:"required,description=The exact digits such as 1234#."`
}

// Matches is a regular expression an observed value must match.
type Matches struct {
	Matches string `yaml:"matches" json:"matches" jsonschema:"required,description=A regular expression such as ^sip:billing@."`
}

// MetricAssertion compares a built-in metric with exactly one comparator.
type MetricAssertion struct {
	Name string   `yaml:"name" json:"name" jsonschema:"required,description=Metric name such as turn_latency_p95_ms."`
	Lt   *float64 `yaml:"lt,omitempty" json:"lt,omitempty"`
	Lte  *float64 `yaml:"lte,omitempty" json:"lte,omitempty"`
	Gt   *float64 `yaml:"gt,omitempty" json:"gt,omitempty"`
	Gte  *float64 `yaml:"gte,omitempty" json:"gte,omitempty"`
	Eq   *float64 `yaml:"eq,omitempty" json:"eq,omitempty"`
}

// Comparators lists the comparators set, in lt lte gt gte eq order.
func (m MetricAssertion) Comparators() []string {
	var out []string
	for _, c := range []struct {
		name string
		v    *float64
	}{{"lt", m.Lt}, {"lte", m.Lte}, {"gt", m.Gt}, {"gte", m.Gte}, {"eq", m.Eq}} {
		if c.v != nil {
			out = append(out, c.name)
		}
	}
	return out
}

// Compare evaluates v against the single comparator and explains the result.
func (m MetricAssertion) Compare(v float64) (bool, string) {
	switch {
	case m.Lt != nil:
		return v < *m.Lt, fmt.Sprintf("%s = %g, want < %g", m.Name, v, *m.Lt)
	case m.Lte != nil:
		return v <= *m.Lte, fmt.Sprintf("%s = %g, want <= %g", m.Name, v, *m.Lte)
	case m.Gt != nil:
		return v > *m.Gt, fmt.Sprintf("%s = %g, want > %g", m.Name, v, *m.Gt)
	case m.Gte != nil:
		return v >= *m.Gte, fmt.Sprintf("%s = %g, want >= %g", m.Name, v, *m.Gte)
	case m.Eq != nil:
		return v == *m.Eq, fmt.Sprintf("%s = %g, want == %g", m.Name, v, *m.Eq)
	}
	return false, fmt.Sprintf("%s = %g, no comparator", m.Name, v)
}

// IntCompare compares an integer (the number of turns) with exactly one
// comparator.
type IntCompare struct {
	Lt  *int `yaml:"lt,omitempty" json:"lt,omitempty"`
	Lte *int `yaml:"lte,omitempty" json:"lte,omitempty"`
	Gt  *int `yaml:"gt,omitempty" json:"gt,omitempty"`
	Gte *int `yaml:"gte,omitempty" json:"gte,omitempty"`
	Eq  *int `yaml:"eq,omitempty" json:"eq,omitempty"`
}

// Comparators lists the comparators set, in lt lte gt gte eq order.
func (c IntCompare) Comparators() []string {
	var out []string
	for _, x := range []struct {
		name string
		v    *int
	}{{"lt", c.Lt}, {"lte", c.Lte}, {"gt", c.Gt}, {"gte", c.Gte}, {"eq", c.Eq}} {
		if x.v != nil {
			out = append(out, x.name)
		}
	}
	return out
}

// Compare evaluates v against the single comparator and explains the result.
func (c IntCompare) Compare(name string, v int) (bool, string) {
	switch {
	case c.Lt != nil:
		return v < *c.Lt, fmt.Sprintf("%s = %d, want < %d", name, v, *c.Lt)
	case c.Lte != nil:
		return v <= *c.Lte, fmt.Sprintf("%s = %d, want <= %d", name, v, *c.Lte)
	case c.Gt != nil:
		return v > *c.Gt, fmt.Sprintf("%s = %d, want > %d", name, v, *c.Gt)
	case c.Gte != nil:
		return v >= *c.Gte, fmt.Sprintf("%s = %d, want >= %d", name, v, *c.Gte)
	case c.Eq != nil:
		return v == *c.Eq, fmt.Sprintf("%s = %d, want == %d", name, v, *c.Eq)
	}
	return false, fmt.Sprintf("%s = %d, no comparator", name, v)
}
