package llm

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
)

// executePrompt renders the named prompt with input and generates with
// modelName, the way prompt.Execute does, with one difference: the output
// schema a provider needs shaped its own way is reshaped first. The OpenAI
// plugin sends a prompt's JSON schema as a strict response_format, and
// OpenAI's strict mode rejects a schema unless every object forbids
// additional properties and lists every property as required, which the
// Picoschema of a .prompt file never does. strictSchema makes that
// transformation for openai/ models; every other provider gets the schema
// as written.
func (c *Client) executePrompt(ctx context.Context, name string, input map[string]any, modelName string) (*ai.ModelResponse, error) {
	prompt := genkit.LookupPrompt(c.g, name)
	if prompt == nil {
		return nil, fmt.Errorf("llm: prompt %s is not loaded", name)
	}
	opts, err := prompt.Render(ctx, input)
	if err != nil {
		return nil, err
	}
	opts.Model = modelName
	if out := opts.Output; out != nil && out.JsonSchema != nil && wantsStrictSchema(modelName) {
		out.JsonSchema = strictSchema(out.JsonSchema)
	}
	return genkit.GenerateWithRequest(ctx, c.g, opts, nil, nil)
}

// wantsStrictSchema reports whether modelName's provider validates output
// schemas with OpenAI's strict rules.
func wantsStrictSchema(modelName string) bool {
	return strings.HasPrefix(modelName, "openai/")
}

// strictSchema returns a copy of schema that satisfies OpenAI's strict
// structured-output rules: every object has additionalProperties false and
// every property required, with a property that was optional made nullable
// so the model can still leave it out in substance. Arrays, anyOf/oneOf
// branches and $defs are rewritten recursively; the input is not modified.
func strictSchema(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema)+2)
	for k, v := range schema {
		out[k] = v
	}
	// Picoschema writes an optional typed property as its full schema plus
	// anyOf: [{type: T}, {type: null}], and the bare branch fails strict
	// validation (an array branch without items). Fold it into a nullable
	// type on the schema itself.
	if branches, ok := out["anyOf"].([]any); ok {
		t, _ := out["type"].(string)
		if t == "" {
			t = bareBranchType(branches)
		}
		if t != "" {
			if hasNullBranch(branches) {
				out["type"] = []any{t, "null"}
			} else {
				out["type"] = t
			}
			delete(out, "anyOf")
		}
	}
	if props, ok := out["properties"].(map[string]any); ok {
		required := map[string]bool{}
		if req, ok := out["required"].([]any); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
		}
		if req, ok := out["required"].([]string); ok {
			for _, r := range req {
				required[r] = true
			}
		}
		newProps := make(map[string]any, len(props))
		names := make([]string, 0, len(props))
		for name, p := range props {
			names = append(names, name)
			sub, _ := p.(map[string]any)
			if sub == nil {
				newProps[name] = p
				continue
			}
			sub = strictSchema(sub)
			if !required[name] {
				sub = nullable(sub)
			}
			newProps[name] = sub
		}
		sort.Strings(names)
		out["properties"] = newProps
		out["required"] = names
		out["additionalProperties"] = false
	} else if t, _ := out["type"].(string); t == "object" {
		out["additionalProperties"] = false
	}
	if items, ok := out["items"].(map[string]any); ok {
		out["items"] = strictSchema(items)
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if branches, ok := out[key].([]any); ok {
			nb := make([]any, len(branches))
			for i, b := range branches {
				if m, ok := b.(map[string]any); ok {
					nb[i] = strictSchema(m)
				} else {
					nb[i] = b
				}
			}
			out[key] = nb
		}
	}
	if defs, ok := out["$defs"].(map[string]any); ok {
		nd := make(map[string]any, len(defs))
		for k, d := range defs {
			if m, ok := d.(map[string]any); ok {
				nd[k] = strictSchema(m)
			} else {
				nd[k] = d
			}
		}
		out["$defs"] = nd
	}
	return out
}

// bareBranchType returns the type of the one anyOf branch that is nothing
// but a non-null type, such as the {type: array} Picoschema writes for an
// optional array whose items sit on the schema itself; "" when the branches
// are anything else.
func bareBranchType(branches []any) string {
	typ := ""
	for _, b := range branches {
		m, ok := b.(map[string]any)
		if !ok {
			return ""
		}
		t, _ := m["type"].(string)
		if t == "null" {
			continue
		}
		if len(m) != 1 || t == "" || typ != "" {
			return ""
		}
		typ = t
	}
	return typ
}

// hasNullBranch reports whether one of the anyOf branches is {type: null}.
func hasNullBranch(branches []any) bool {
	for _, b := range branches {
		if m, ok := b.(map[string]any); ok && m["type"] == "null" {
			return true
		}
	}
	return false
}

// nullable lets schema also accept null: a string type becomes [type, null],
// a type list gains null, and a schema without a type (anyOf, $ref) is
// wrapped in anyOf with a null branch.
func nullable(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema)+1)
	for k, v := range schema {
		out[k] = v
	}
	switch t := out["type"].(type) {
	case string:
		if t != "null" {
			out["type"] = []any{t, "null"}
		}
	case []any:
		if !slices.Contains(t, any("null")) {
			out["type"] = append(slices.Clone(t), "null")
		}
	case []string:
		if !slices.Contains(t, "null") {
			out["type"] = append(slices.Clone(t), "null")
		}
	default:
		if _, ok := out["anyOf"]; ok {
			break
		}
		inner := out
		out = map[string]any{"anyOf": []any{inner, map[string]any{"type": "null"}}}
		if d, ok := inner["description"]; ok {
			out["description"] = d
		}
	}
	return out
}
