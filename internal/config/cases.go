package config

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Case is one row of a parameterised test: a name and the values it gives
// {{vars.<key>}}.
type Case struct {
	Name string            `yaml:"name,omitempty" json:"name,omitempty" jsonschema:"description=The case's name; default case 1 / case 2..."`
	Vars map[string]string `yaml:"vars" json:"vars" jsonschema:"required,description=Values for {{vars.<key>}} in this case."`
}

// ExpandCases replaces every test that has cases (inline, or from
// cases_file) with one test per case, named "<name> [<case>]", tagged
// case:<name>, with the case's vars on top of the test's. It runs after
// validation, so YAML line numbers still point at the original tests.
func ExpandCases(s *Suite, file string) Errors {
	var errs Errors
	var out []Test
	changed := false
	for i, t := range s.Tests {
		cases, err := casesOf(t, filepath.Dir(file))
		if err != nil {
			errs = append(errs, &Error{File: file, Path: describePath(s, []string{"tests", idx(i)}), Msg: err.Error()})
		}
		if len(cases) == 0 {
			out = append(out, t)
			continue
		}
		changed = true
		for _, c := range cases {
			nt := t
			nt.Name = fmt.Sprintf("%s [%s]", t.Name, c.Name)
			nt.Cases, nt.CasesFile = nil, ""
			nt.Tags = append(append([]string(nil), t.Tags...), "case:"+c.Name)
			nt.Vars = map[string]string{}
			for k, v := range t.Vars {
				nt.Vars[k] = v
			}
			for k, v := range c.Vars {
				nt.Vars[k] = v
			}
			out = append(out, nt)
		}
	}
	if changed {
		s.Tests = out
	}
	return errs
}

// casesOf returns a test's cases, inline or from its file, with names
// filled in; nil when the test is not parameterised.
func casesOf(t Test, dir string) ([]Case, error) {
	cases := append([]Case(nil), t.Cases...)
	if t.CasesFile != "" {
		fromFile, err := LoadCases(filepath.Join(dir, t.CasesFile))
		if err != nil {
			return nil, fmt.Errorf("cases_file %s: %w", t.CasesFile, err)
		}
		cases = append(cases, fromFile...)
	}
	if len(cases) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	for i := range cases {
		if cases[i].Name == "" {
			cases[i].Name = fmt.Sprintf("case %d", i+1)
		}
		if seen[cases[i].Name] {
			return nil, fmt.Errorf("case %q appears twice", cases[i].Name)
		}
		seen[cases[i].Name] = true
		if len(cases[i].Vars) == 0 {
			return nil, fmt.Errorf("case %q has no vars", cases[i].Name)
		}
	}
	return cases, nil
}

// LoadCases reads cases from a CSV file (a header row; a name column names
// the case) or a JSON file (an array of objects; a name key names the case,
// every other key is a var).
func LoadCases(path string) ([]Case, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		var rows []map[string]any
		if err := json.Unmarshal(b, &rows); err != nil {
			return nil, fmt.Errorf("not a JSON array of objects: %w", err)
		}
		var out []Case
		for _, row := range rows {
			c := Case{Vars: map[string]string{}}
			keys := make([]string, 0, len(row))
			for k := range row {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v := scalarString(row[k])
				if k == "name" {
					c.Name = v
					continue
				}
				c.Vars[k] = v
			}
			out = append(out, c)
		}
		return out, nil
	case ".csv":
		recs, err := csv.NewReader(strings.NewReader(string(b))).ReadAll()
		if err != nil {
			return nil, err
		}
		if len(recs) < 2 {
			return nil, fmt.Errorf("needs a header row and at least one case")
		}
		header := recs[0]
		var out []Case
		for _, rec := range recs[1:] {
			c := Case{Vars: map[string]string{}}
			for i, h := range header {
				v := ""
				if i < len(rec) {
					v = rec[i]
				}
				if strings.TrimSpace(h) == "name" {
					c.Name = v
					continue
				}
				c.Vars[strings.TrimSpace(h)] = v
			}
			out = append(out, c)
		}
		return out, nil
	}
	return nil, fmt.Errorf("must be a .csv or .json file")
}

func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprint(x)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// validateCases checks a test's cases before expansion.
func (v *validator) validateCases(s *Suite, i int, t Test, file string) {
	tp := []string{"tests", idx(i)}
	for j, c := range t.Cases {
		if len(c.Vars) == 0 {
			v.errorf(with(tp, "cases", idx(j)), "a case needs vars")
		}
	}
	if t.CasesFile != "" {
		if strings.Contains(t.CasesFile, "{{") || strings.Contains(t.CasesFile, "${") {
			v.errorf(with(tp, "cases_file"), "cases_file is a plain path, not a template")
		} else if _, err := os.Stat(filepath.Join(filepath.Dir(file), t.CasesFile)); err != nil && file != "" {
			v.errorf(with(tp, "cases_file"), "cases_file %s: %v", t.CasesFile, err)
		}
	}
	if _, err := casesOf(t, filepath.Dir(file)); err != nil && len(t.Cases) > 0 && t.CasesFile == "" {
		v.errorf(with(tp, "cases"), "%v", err)
	}
}

var _ = yaml.Node{}
