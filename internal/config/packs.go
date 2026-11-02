package config

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed packs/*.yaml
var packFS embed.FS

// Pack is a reusable set of assertions: a built-in red-team pack or one the
// suite defines under packs:.
type Pack struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Assertions  []Assertion `yaml:"assertions"`
}

var (
	builtinOnce  sync.Once
	builtinPacks []Pack
)

// BuiltinPacks returns the packs that ship with tamesu, by name.
func BuiltinPacks() []Pack {
	builtinOnce.Do(func() {
		entries, err := fs.ReadDir(packFS, "packs")
		if err != nil {
			panic(fmt.Sprintf("config: read packs: %v", err))
		}
		for _, e := range entries {
			data, err := packFS.ReadFile("packs/" + e.Name())
			if err != nil {
				panic(fmt.Sprintf("config: read pack %s: %v", e.Name(), err))
			}
			var p Pack
			if err := yaml.Unmarshal(data, &p); err != nil {
				panic(fmt.Sprintf("config: pack %s: %v", e.Name(), err))
			}
			builtinPacks = append(builtinPacks, p)
		}
		sort.Slice(builtinPacks, func(i, j int) bool { return builtinPacks[i].Name < builtinPacks[j].Name })
	})
	return builtinPacks
}

// BuiltinPack returns a built-in pack by name.
func BuiltinPack(name string) (Pack, bool) {
	for _, p := range BuiltinPacks() {
		if p.Name == name {
			return p, true
		}
	}
	return Pack{}, false
}

// PackNames lists the packs a suite may use: its own, then the built-in ones
// it does not override.
func PackNames(s *Suite) []string {
	seen := map[string]bool{}
	var names []string
	if s != nil {
		for name := range s.Packs {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, p := range BuiltinPacks() {
		if !seen[p.Name] {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names
}

// LookupPack resolves a pack name for a suite: the suite's own definition
// first, then the built-in one.
func LookupPack(s *Suite, name string) ([]Assertion, bool) {
	if s != nil {
		if as, ok := s.Packs[name]; ok {
			return as, true
		}
	}
	if p, ok := BuiltinPack(name); ok {
		return p.Assertions, true
	}
	return nil, false
}

// ExpandPacks replaces every `pack:` assertion of every test with the pack's
// assertions, each marked with From so results read <pack>/<assertion>; a
// pack entry's on_turn applies to the assertions that have none. An unknown
// pack, or a pack inside a pack, is an error at the assertion's line (root
// may be nil). LoadSuite calls it before validation.
func ExpandPacks(s *Suite, root *yaml.Node, file string) Errors {
	var errs Errors
	for i := range s.Tests {
		t := &s.Tests[i]
		out, perrs := expandAssertionPacks(s, t.Assert, func(j int) []string { return []string{"tests", idx(i), "assert", idx(j), "pack"} }, root, file)
		errs = append(errs, perrs...)
		t.Assert = out
	}
	if s.Defaults != nil && len(s.Defaults.Assert) > 0 {
		out, perrs := expandAssertionPacks(s, s.Defaults.Assert, func(j int) []string { return []string{"defaults", "assert", idx(j), "pack"} }, root, file)
		errs = append(errs, perrs...)
		s.Defaults.Assert = out
	}
	return errs
}

// ExpandAssertionPacks expands the pack entries of a list of assertions
// against the suite's packs (and the built-in ones); an unknown or nested
// pack is an error naming the entry.
func ExpandAssertionPacks(s *Suite, list []Assertion) ([]Assertion, error) {
	out, errs := expandAssertionPacks(s, list, func(j int) []string { return []string{"assert", idx(j), "pack"} }, nil, "")
	if len(errs) > 0 {
		return nil, errs[0]
	}
	return out, nil
}

func expandAssertionPacks(s *Suite, list []Assertion, pathOf func(j int) []string, root *yaml.Node, file string) ([]Assertion, Errors) {
	var errs Errors
	var out []Assertion
	for j, a := range list {
		if a.Pack == nil {
			out = append(out, a)
			continue
		}
		path := pathOf(j)
		name := *a.Pack
		as, ok := LookupPack(s, name)
		if !ok {
			errs = append(errs, &Error{File: file, Line: lineOf(root, path...), Path: describePath(s, path), Msg: fmt.Sprintf("unknown pack %q (have %s)", name, strings.Join(PackNames(s), ", "))})
			continue
		}
		for _, pa := range as {
			if pa.Pack != nil {
				errs = append(errs, &Error{File: file, Line: lineOf(root, path...), Path: describePath(s, path), Msg: fmt.Sprintf("pack %q uses pack %q: packs cannot nest", name, *pa.Pack)})
				continue
			}
			pa.From = name
			if !pa.OnTurn.IsSet() {
				pa.OnTurn = a.OnTurn
			}
			out = append(out, pa)
		}
	}
	return out, errs
}
