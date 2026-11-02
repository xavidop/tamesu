package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadOptions control how files are loaded.
type LoadOptions struct {
	// Env resolves ${VAR}; nil means no variable is set.
	Env EnvLookup
	// StrictEnv makes an unset ${VAR} an error instead of a warning. validate
	// uses false so a suite can be checked on a machine without the
	// variables; run and doctor use true.
	StrictEnv bool
}

// topLevelKeysNotExpanded are the top-level mappings whose scalars keep their
// ${VAR}: secret refs expand ${VAR} from mamori ref vars (--var and vars:),
// never from the environment.
var topLevelKeysNotExpanded = map[string]bool{"secrets": true}

// LoadProject reads tamesu.yaml. A missing file yields DefaultProject() and no
// errors. Problems in the file come back as Errors with line numbers; err is
// only an I/O failure.
func LoadProject(path string, o LoadOptions) (Project, Errors, error) {
	p := DefaultProject()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil, nil
	}
	if err != nil {
		return p, nil, err
	}
	root, errs := parseStrict(path, data, &Project{})
	if errs.HasErrors() {
		return p, errs, nil
	}
	if root == nil {
		return p, Errors{{File: path, Line: 1, Msg: "empty file"}}, nil
	}
	errs = append(errs, expandNodeEnv(path, root, o)...)
	if errs.HasErrors() {
		return p, errs.Sorted(), nil
	}
	if err := root.Decode(&p); err != nil {
		return p, append(errs, decodeErrors(path, err)...).Sorted(), nil
	}
	errs = append(errs, ValidateProject(p, path, root)...)
	return p, errs.Sorted(), nil
}

// LoadSuite reads and validates one suite file. Problems come back as Errors
// with line numbers (the suite is still returned when only warnings were
// found); err is only an I/O failure.
func LoadSuite(path string, o LoadOptions) (*Suite, Errors, error) {
	return LoadSuiteWithProject(path, DefaultProject(), o)
}

// LoadSuiteWithProject is LoadSuite with the project configuration the suite
// belongs to, so project-level secrets count as declared.
func LoadSuiteWithProject(path string, p Project, o LoadOptions) (*Suite, Errors, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	s := &Suite{Path: path}
	root, errs := parseStrict(path, data, &Suite{})
	if root == nil && errs.HasErrors() {
		return nil, errs.Sorted(), nil
	}
	if root == nil || !hasKey(root, "suite") {
		line := 1
		if root != nil {
			line = max(1, root.Line)
		}
		return nil, Errors{{File: path, Line: line, Msg: `not a tamesu suite (no "suite:" key)`}}, nil
	}
	if errs.HasErrors() {
		return nil, errs.Sorted(), nil
	}
	errs = append(errs, expandNodeEnv(path, root, o)...)
	if errs.HasErrors() {
		return nil, errs.Sorted(), nil
	}
	if err := root.Decode(s); err != nil {
		return nil, append(errs, decodeErrors(path, err)...).Sorted(), nil
	}
	if perrs := ExpandPacks(s, root, path); len(perrs) > 0 {
		return nil, append(errs, perrs...).Sorted(), nil
	}
	errs = append(errs, Validate(s, p, root)...)
	errs = append(errs, ExpandCases(s, path)...)
	return s, errs.Sorted(), nil
}

// parseStrict parses data into a yaml.Node and, separately, decodes it into
// into (a throwaway value of the target type) with KnownFields on, so unknown
// fields and type mismatches come back with their line numbers. The node is
// nil for an empty document.
func parseStrict(path string, data []byte, into any) (*yaml.Node, Errors) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, decodeErrors(path, err)
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return nil, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(into); err != nil {
		return root.Content[0], decodeErrors(path, err)
	}
	return root.Content[0], nil
}

func hasKey(n *yaml.Node, key string) bool {
	if n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return true
		}
	}
	return false
}

var lineRe = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)

// decodeErrors turns a yaml.v3 error into Errors, one per "line N: ..." entry,
// without the Go type prefixes a user has no use for.
func decodeErrors(path string, err error) Errors {
	var lines []string
	var te *yaml.TypeError
	if errors.As(err, &te) {
		lines = te.Errors
	} else {
		lines = strings.Split(err.Error(), "\n")
	}
	var out Errors
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || l == "yaml: unmarshal errors:" {
			continue
		}
		e := &Error{File: path, Msg: strings.TrimPrefix(l, "yaml: ")}
		if m := lineRe.FindStringSubmatch(l); m != nil {
			e.Line, _ = strconv.Atoi(m[1])
			e.Msg = m[2]
		}
		e.Msg = strings.ReplaceAll(e.Msg, "in type config.", "in type ")
		out = append(out, e)
	}
	if len(out) == 0 {
		out = Errors{{File: path, Msg: err.Error()}}
	}
	return out
}

// scalarRef is one scalar of a document with the keys and indexes leading to it.
type scalarRef struct {
	node *yaml.Node
	path []string
}

// walkScalars visits every scalar under n (aliases are not followed), skipping
// the top-level mappings in skipTop.
func walkScalars(n *yaml.Node, path []string, skipTop map[string]bool, fn func(scalarRef)) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walkScalars(c, path, skipTop, fn)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if len(path) == 0 && skipTop[k.Value] {
				continue
			}
			walkScalars(v, append(path[:len(path):len(path)], k.Value), skipTop, fn)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			walkScalars(c, append(path[:len(path):len(path)], strconv.Itoa(i)), skipTop, fn)
		}
	case yaml.ScalarNode:
		fn(scalarRef{node: n, path: path})
	}
}

// expandNodeEnv expands ${VAR} in every scalar of the document in place. It
// returns a warning per unset variable (or an error when StrictEnv), and a
// warning for a ${VAR} inside a field documented as secret.
func expandNodeEnv(path string, root *yaml.Node, o LoadOptions) Errors {
	var errs Errors
	walkScalars(root, nil, topLevelKeysNotExpanded, func(sc scalarRef) {
		n := sc.node
		if !strings.Contains(n.Value, "${") {
			return
		}
		if IsSecretField(sc.path) {
			errs = append(errs, &Error{File: path, Line: n.Line, Path: strings.Join(sc.path, "."), Warning: true,
				Msg: fmt.Sprintf("use {{secrets.NAME}} instead of %s for %s; ${VAR} is for non-secret values", firstEnvRef(n.Value), sc.path[len(sc.path)-1])})
		}
		out, unresolved, err := ExpandEnv(n.Value, o.Env, o.StrictEnv)
		if err != nil {
			errs = append(errs, &Error{File: path, Line: n.Line, Path: strings.Join(sc.path, "."), Msg: err.Error()})
			return
		}
		for _, name := range unresolved {
			errs = append(errs, &Error{File: path, Line: n.Line, Path: strings.Join(sc.path, "."), Warning: true,
				Msg: fmt.Sprintf("env var %s is not set; ${%s} is left as is", name, name)})
		}
		if out != n.Value {
			n.Value = out
			if n.Style == 0 {
				// A plain scalar re-resolves its type (an expanded "8" is an int).
				n.Tag = ""
			}
		}
	})
	return errs
}

func firstEnvRef(s string) string {
	if m := envRe.FindString(s); m != "" {
		return m
	}
	return "${VAR}"
}

// nodeAt follows mapping keys and sequence indexes from n. It returns nil when
// the path does not exist.
func nodeAt(n *yaml.Node, path ...string) *yaml.Node {
	value, _ := nodeAndKeyAt(n, path...)
	return value
}

// nodeAndKeyAt is nodeAt plus the key node of the last mapping step (nil when
// the last step is a sequence index or the path is empty).
func nodeAndKeyAt(n *yaml.Node, path ...string) (value, key *yaml.Node) {
	if n == nil {
		return nil, nil
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	for _, p := range path {
		key = nil
		switch n.Kind {
		case yaml.MappingNode:
			var next *yaml.Node
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == p {
					key, next = n.Content[i], n.Content[i+1]
					break
				}
			}
			if next == nil {
				return nil, nil
			}
			n = next
		case yaml.SequenceNode:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(n.Content) {
				return nil, nil
			}
			n = n.Content[i]
		default:
			return nil, nil
		}
	}
	return n, key
}

// lineOf is the line of the node at path, or of the nearest existing parent
// (a missing field is reported at its parent). Zero when root is nil.
func lineOf(root *yaml.Node, path ...string) int {
	if root == nil {
		return 0
	}
	for {
		if n, key := nodeAndKeyAt(root, path...); n != nil {
			// A block mapping or list starts on the line after its key; the
			// key's line is where a reader looks for the field.
			if key != nil && key.Line < n.Line {
				return key.Line
			}
			return n.Line
		}
		if len(path) == 0 {
			return 1
		}
		path = path[:len(path)-1]
	}
}
