package config

import "fmt"

// ExpandLanguages replaces every test that lists languages with one test per
// language, named "<name> [<lang>]", tagged lang:<lang>, whose persona speaks
// that language and whose vars carry it as language. It runs after the cases
// expanded, so a parameterised test runs once per case and language.
func ExpandLanguages(s *Suite) {
	var out []Test
	changed := false
	for _, t := range s.Tests {
		if len(t.Languages) == 0 {
			out = append(out, t)
			continue
		}
		changed = true
		for _, lang := range t.Languages {
			nt := t
			nt.Name = fmt.Sprintf("%s [%s]", t.Name, lang)
			nt.Languages = nil
			nt.Tags = append(append([]string(nil), t.Tags...), "lang:"+lang)
			p := Persona{}
			if t.Persona != nil {
				p = *t.Persona
			}
			p.Language = lang
			nt.Persona = &p
			nt.Vars = map[string]string{}
			for k, v := range t.Vars {
				nt.Vars[k] = v
			}
			nt.Vars["language"] = lang
			out = append(out, nt)
		}
	}
	if changed {
		s.Tests = out
	}
}
