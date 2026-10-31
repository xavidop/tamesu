package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that reads and writes as a Go duration string
// such as "30s", "3m" or "1h30m" in YAML and JSON.
type Duration time.Duration

// Std returns the duration as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String formats the duration as time.Duration does.
func (d Duration) String() string { return time.Duration(d).String() }

func (d *Duration) parse(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: use a Go duration such as 30s, 3m or 1h30m", s)
	}
	*d = Duration(v)
	return nil
}

// UnmarshalYAML decodes a scalar duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: a duration must be a string such as 3m", n.Line)
	}
	if err := d.parse(n.Value); err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	return nil
}

// MarshalYAML encodes the duration as a string.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// MarshalJSON encodes the duration as a JSON string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON decodes a JSON duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	return d.parse(s)
}

// JSONSchema describes the duration as a string with Go duration syntax.
func (Duration) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Pattern:     `^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$`,
		Description: "A duration such as 30s, 3m or 1h30m.",
	}
}
