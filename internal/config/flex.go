package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

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

// oneOfBoolOr builds a schema that accepts a boolean or the object described
// by the fields of obj.
func oneOfBoolOr(obj any, boolDesc string) *jsonschema.Schema {
	r := &jsonschema.Reflector{ExpandedStruct: true, DoNotReference: true, RequiredFromJSONSchemaTags: true}
	o := r.Reflect(obj)
	o.Version = ""
	o.ID = ""
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		{Type: "boolean", Description: boolDesc},
		o,
	}}
}

// Push is the a2a target's push notification setting: `push: true` listens
// on a free local port, the mapping form says where to listen and which URL
// the agent must call.
type Push struct {
	Enabled bool   `yaml:"enabled,omitempty" json:"enabled,omitempty" jsonschema:"description=false turns push off; default true in the mapping form."`
	Addr    string `yaml:"addr,omitempty" json:"addr,omitempty" jsonschema:"description=Address to listen on such as 0.0.0.0:8099; default a free port on 127.0.0.1."`
	URL     string `yaml:"url,omitempty" json:"url,omitempty" jsonschema:"description=The URL the agent calls (a tunnel or a container address); default http://<addr>."`
}

// UnmarshalYAML accepts a boolean or a mapping.
func (ps *Push) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		b, err := strconv.ParseBool(n.Value)
		if err != nil {
			return fmt.Errorf("line %d: push must be true, false or a mapping", n.Line)
		}
		*ps = Push{Enabled: b}
		return nil
	}
	if err := checkKeys(n, "Push", "enabled", "addr", "url"); err != nil {
		return err
	}
	type plain Push
	p := plain{Enabled: true}
	if err := n.Decode(&p); err != nil {
		return err
	}
	*ps = Push(p)
	return nil
}

// JSONSchema describes the boolean and object forms.
func (Push) JSONSchema() *jsonschema.Schema {
	type plain Push
	return oneOfBoolOr(&plain{}, "true listens on a free local port.")
}

// ScriptFile is a file a script step sends: a path relative to the suite or a
// URI, or a mapping with a name and a MIME type.
type ScriptFile struct {
	Path string `yaml:"path,omitempty" json:"path,omitempty" jsonschema:"description=A file relative to the suite; its bytes are sent."`
	URI  string `yaml:"uri,omitempty" json:"uri,omitempty" jsonschema:"description=A URI the agent fetches itself; sent instead of bytes."`
	Name string `yaml:"name,omitempty" json:"name,omitempty" jsonschema:"description=The file name the agent sees; default the path's base name."`
	MIME string `yaml:"mime,omitempty" json:"mime,omitempty" jsonschema:"description=The media type; default from the extension."`
}

// UnmarshalYAML accepts a string (a URI when it has a scheme, else a path) or
// a mapping.
func (f *ScriptFile) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if strings.Contains(n.Value, "://") {
			*f = ScriptFile{URI: n.Value}
		} else {
			*f = ScriptFile{Path: n.Value}
		}
		return nil
	}
	if err := checkKeys(n, "ScriptFile", "path", "uri", "name", "mime"); err != nil {
		return err
	}
	type plain ScriptFile
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*f = ScriptFile(p)
	return nil
}

// JSONSchema describes the string and object forms.
func (ScriptFile) JSONSchema() *jsonschema.Schema {
	type plain ScriptFile
	return oneOfStringOr(&plain{}, "A path relative to the suite, or a URI.")
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
	Name   string         `yaml:"name" json:"name" jsonschema:"required,description=Tool name as the target reports it."`
	Args   map[string]any `yaml:"args,omitempty" json:"args,omitempty" jsonschema:"description=Argument values that must match (a subset of the call's arguments)."`
	Schema map[string]any `yaml:"schema,omitempty" json:"schema,omitempty" jsonschema:"description=tool_called: a JSON Schema every call's arguments must validate against."`
}

// UnmarshalYAML accepts a tool name or a mapping.
func (c *ToolCall) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		c.Name = n.Value
		return nil
	}
	if err := checkKeys(n, "ToolCall", "name", "args", "schema"); err != nil {
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

// Delegation is the delegated_to assertion: a remote agent's name, or a
// mapping with the agent and what the message must contain or match.
type Delegation struct {
	Agent    string `yaml:"agent" json:"agent" jsonschema:"required,description=The remote agent's name (a key of remote_agents)."`
	Contains string `yaml:"contains,omitempty" json:"contains,omitempty" jsonschema:"description=Text some message to the agent must contain."`
	Regex    string `yaml:"regex,omitempty" json:"regex,omitempty" jsonschema:"description=A regular expression some message to the agent must match."`
}

// UnmarshalYAML accepts an agent name or a mapping.
func (d *Delegation) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		d.Agent = n.Value
		return nil
	}
	if err := checkKeys(n, "Delegation", "agent", "contains", "regex"); err != nil {
		return err
	}
	type plain Delegation
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*d = Delegation(p)
	return nil
}

// JSONSchema describes the string and object forms.
func (Delegation) JSONSchema() *jsonschema.Schema {
	type plain Delegation
	return oneOfStringOr(&plain{}, "The remote agent's name.")
}

// UnmarshalYAML accepts a skill id or a mapping.
func (s *RemoteSkill) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		s.ID = n.Value
		return nil
	}
	if err := checkKeys(n, "RemoteSkill", "id", "name", "description", "tags"); err != nil {
		return err
	}
	type plain RemoteSkill
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*s = RemoteSkill(p)
	return nil
}

// JSONSchema describes the string and object forms.
func (RemoteSkill) JSONSchema() *jsonschema.Schema {
	type plain RemoteSkill
	return oneOfStringOr(&plain{}, "The skill id.")
}

// Similar is a semantic assertion: some agent turn means the same as Text,
// by embedding similarity.
type Similar struct {
	Text  string  `yaml:"text" json:"text" jsonschema:"required,description=What an agent turn should mean."`
	Min   float64 `yaml:"min,omitempty" json:"min,omitempty" jsonschema:"description=Minimum cosine similarity between the embeddings (0 to 1); default 0.8."`
	Model string  `yaml:"model,omitempty" json:"model,omitempty" jsonschema:"description=Embedding model (a Genkit reference); defaults to models.embedder."`
}

// UnmarshalYAML accepts a text string or a mapping.
func (sm *Similar) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		sm.Text = n.Value
		return nil
	}
	if err := checkKeys(n, "Similar", "text", "min", "model"); err != nil {
		return err
	}
	type plain Similar
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*sm = Similar(p)
	return nil
}

// JSONSchema describes the string and object forms.
func (Similar) JSONSchema() *jsonschema.Schema {
	type plain Similar
	return oneOfStringOr(&plain{}, "The text some agent turn should mean; the configured embedding model and a 0.8 minimum are the defaults.")
}

// Fuzz is one tool's fuzzed result: `lookup_order: error` or
// `lookup_order: { mode: delay, delay: 8s }` or `{ mode: custom, result: {...} }`.
type Fuzz struct {
	Mode   string   `yaml:"mode" json:"mode" jsonschema:"required,enum=error,enum=empty,enum=null,enum=malformed,enum=hostile,enum=huge,enum=wrong_type,enum=delay,enum=custom,description=What the tool answers instead of its result."`
	Delay  Duration `yaml:"delay,omitempty" json:"delay,omitempty" jsonschema:"description=How long the answer is held back (any mode; the whole point of delay)."`
	Result any      `yaml:"result,omitempty" json:"result,omitempty" jsonschema:"description=The answer in custom mode (any JSON)."`
}

// FuzzModes lists the fuzz modes a test may name.
var FuzzModes = []string{"error", "empty", "null", "malformed", "hostile", "huge", "wrong_type", "delay", "custom"}

// FuzzTargets are the target types that answer tool calls, so fuzzing applies.
var FuzzTargets = []string{TargetRealtime, TargetPipecat, TargetModel, TargetMCP}

// UnmarshalYAML accepts a mode string or a mapping.
func (f *Fuzz) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		f.Mode = n.Value
		return nil
	}
	if err := checkKeys(n, "Fuzz", "mode", "delay", "result"); err != nil {
		return err
	}
	type plain Fuzz
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*f = Fuzz(p)
	return nil
}

// JSONSchema describes the string and object forms.
func (Fuzz) JSONSchema() *jsonschema.Schema {
	type plain Fuzz
	return oneOfStringOr(&plain{}, "The fuzz mode: error, empty, null, malformed, hostile, huge, wrong_type or delay.")
}

// HandoffAssertion is `handoff: true`, `handoff: false` or a mapping that
// says when and to whom the agent must have handed the conversation to a
// person, and what its summary must say.
type HandoffAssertion struct {
	Expected        *bool    `yaml:"expected,omitempty" json:"expected,omitempty" jsonschema:"description=Whether a handoff must have happened; true when omitted."`
	WithinTurns     int      `yaml:"within_turns,omitempty" json:"within_turns,omitempty" jsonschema:"description=The latest agent turn the handoff may happen on."`
	To              *string  `yaml:"to,omitempty" json:"to,omitempty" jsonschema:"description=A regular expression the destination must match."`
	SummaryContains []string `yaml:"summary_contains,omitempty" json:"summary_contains,omitempty" jsonschema:"description=Texts the handoff summary must contain."`
	Summary         string   `yaml:"summary,omitempty" json:"summary,omitempty" jsonschema:"description=A judge rubric for the handoff summary."`
}

// UnmarshalYAML accepts a boolean or a mapping.
func (h *HandoffAssertion) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var b bool
		if err := n.Decode(&b); err != nil {
			return fmt.Errorf("line %d: handoff is true, false or a mapping", n.Line)
		}
		h.Expected = &b
		return nil
	}
	if err := checkKeys(n, "HandoffAssertion", "expected", "within_turns", "to", "summary_contains", "summary"); err != nil {
		return err
	}
	type plain HandoffAssertion
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*h = HandoffAssertion(p)
	return nil
}

// JSONSchema describes the boolean and object forms.
func (HandoffAssertion) JSONSchema() *jsonschema.Schema {
	type plain HandoffAssertion
	r := &jsonschema.Reflector{ExpandedStruct: true, DoNotReference: true, RequiredFromJSONSchemaTags: true}
	o := r.Reflect(&plain{})
	o.Version = ""
	o.ID = ""
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{
		{Type: "boolean", Description: "Whether the agent must have handed the conversation to a person."},
		o,
	}}
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
	Name string   `yaml:"name" json:"name" jsonschema:"required,description=Metric name such as turn_latency_ms_p95."`
	Lt   *float64 `yaml:"lt,omitempty" json:"lt,omitempty" jsonschema:"description=Passes when the metric is below this value"`
	Lte  *float64 `yaml:"lte,omitempty" json:"lte,omitempty" jsonschema:"description=Passes when the metric is at most this value"`
	Gt   *float64 `yaml:"gt,omitempty" json:"gt,omitempty" jsonschema:"description=Passes when the metric is above this value"`
	Gte  *float64 `yaml:"gte,omitempty" json:"gte,omitempty" jsonschema:"description=Passes when the metric is at least this value"`
	Eq   *float64 `yaml:"eq,omitempty" json:"eq,omitempty" jsonschema:"description=Passes when the metric equals this value"`
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
	Lt  *int `yaml:"lt,omitempty" json:"lt,omitempty" jsonschema:"description=Passes when the count is below this value"`
	Lte *int `yaml:"lte,omitempty" json:"lte,omitempty" jsonschema:"description=Passes when the count is at most this value"`
	Gt  *int `yaml:"gt,omitempty" json:"gt,omitempty" jsonschema:"description=Passes when the count is above this value"`
	Gte *int `yaml:"gte,omitempty" json:"gte,omitempty" jsonschema:"description=Passes when the count is at least this value"`
	Eq  *int `yaml:"eq,omitempty" json:"eq,omitempty" jsonschema:"description=Passes when the count equals this value"`
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
