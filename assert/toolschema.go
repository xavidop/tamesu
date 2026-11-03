package assert

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// schemaMessages words the validator's error kinds in English.
var schemaMessages = message.NewPrinter(language.English)

// CompileSchema compiles a JSON Schema given as a document (the YAML mapping
// of a suite), so a mistake in the schema is a config error and not a test
// failure.
func CompileSchema(schema map[string]any) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", schema); err != nil {
		return nil, err
	}
	return c.Compile("schema.json")
}

// ToolCalledWithSchema passes when the agent called the tool (with args
// matching, like ToolCalled) and the arguments of every call to it validate
// against the JSON Schema: a call with arguments the schema rejects fails the
// assertion, whatever the other calls looked like. A nil schema is ToolCalled.
func ToolCalledWithSchema(name string, args map[string]any, schema map[string]any) (Assertion, error) {
	if schema == nil {
		return ToolCalled(name, args), nil
	}
	compiled, err := CompileSchema(schema)
	if err != nil {
		return nil, fmt.Errorf("tool_called %s: schema: %w", name, err)
	}
	return Func("tool_called "+name+" (schema)", func(_ context.Context, r *RunResult) Result {
		var seen []string
		matched := false
		for _, c := range r.ToolCalls {
			seen = append(seen, c.Name)
			if c.Name != name {
				continue
			}
			if err := compiled.Validate(normalizeArgs(c.Args)); err != nil {
				return Result{Pass: false, Reason: fmt.Sprintf("the agent called %s with arguments the schema rejects: %s", name, schemaReason(err)), Evidence: []string{jsonString(c.Args)}}
			}
			if argsMatch(c.Args, args) {
				matched = true
			}
		}
		if !matched {
			if len(args) > 0 {
				return Result{Pass: false, Reason: fmt.Sprintf("the agent did not call %s (tool calls: %s)", describeCall(name, args), listOr(seen))}
			}
			return Result{Pass: false, Reason: fmt.Sprintf("the agent did not call %s (tool calls: %s)", name, listOr(seen))}
		}
		return Result{Pass: true, Reason: fmt.Sprintf("every call to %s had arguments the schema accepts", name)}
	}), nil
}

// normalizeArgs gives the validator a document in the shapes it knows: an
// empty map for a call without arguments, and JSON numbers as float64.
func normalizeArgs(args map[string]any) any {
	if args == nil {
		return map[string]any{}
	}
	return normalizeJSON(args)
}

// normalizeJSON converts the integer and typed-map values a target may
// report into the float64 / map[string]any / []any shapes of decoded JSON.
func normalizeJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = normalizeJSON(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[fmt.Sprint(k)] = normalizeJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalizeJSON(val)
		}
		return out
	case int:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case float32:
		return float64(x)
	}
	return v
}

// schemaReason words a validation error in one line, the innermost causes
// first, since the validator's own message is a multi-line tree.
func schemaReason(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err.Error()
	}
	var leaves []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := "/" + strings.Join(e.InstanceLocation, "/")
			if loc == "/" {
				loc = "the arguments"
			}
			leaves = append(leaves, fmt.Sprintf("%s: %s", loc, e.ErrorKind.LocalizedString(schemaMessages)))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	if len(leaves) == 0 {
		return ve.Error()
	}
	return strings.Join(leaves, "; ")
}
