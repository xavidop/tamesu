package config

import (
	"regexp"
	"slices"

	"gopkg.in/yaml.v3"
)

// EffectiveDefaults are the project defaults with the suite's overrides.
func (s *Suite) EffectiveDefaults(p Project) Defaults { return p.Defaults.Merge(s.Defaults) }

// EffectiveModels are the project models with the suite's overrides.
func (s *Suite) EffectiveModels(p Project) Models { return p.Models.Merge(s.Models) }

// EffectiveBudgets are the project budgets with the suite's overrides.
func (s *Suite) EffectiveBudgets(p Project) Budgets { return p.Budgets.Merge(s.Budgets) }

// EffectiveSecrets are the project's secret refs with the suite's on top.
func (s *Suite) EffectiveSecrets(p Project) map[string]string {
	out := make(map[string]string, len(p.Secrets)+len(s.Secrets))
	for k, v := range p.Secrets {
		out[k] = v
	}
	for k, v := range s.Secrets {
		out[k] = v
	}
	return out
}

// TargetOf returns the target a test runs against.
func (s *Suite) TargetOf(t Test) (Target, bool) {
	target, ok := s.Targets[t.Target]
	return target, ok
}

// TagsOf returns the test's tags plus the suite's.
func (s *Suite) TagsOf(t Test) []string {
	out := append([]string{}, s.Tags...)
	for _, tag := range t.Tags {
		if !slices.Contains(out, tag) {
			out = append(out, tag)
		}
	}
	return out
}

// SelectTests returns the tests whose name matches filter (nil matches all),
// that carry any of tags (empty matches all) and run against target ("" for
// any), in suite order.
func (s *Suite) SelectTests(filter *regexp.Regexp, tags []string, target string) []Test {
	var out []Test
	for _, t := range s.Tests {
		if filter != nil && !filter.MatchString(t.Name) {
			continue
		}
		if target != "" && t.Target != target {
			continue
		}
		if len(tags) > 0 {
			have := s.TagsOf(t)
			if !slices.ContainsFunc(tags, func(tag string) bool { return slices.Contains(have, tag) }) {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// SecretsUsed lists the {{secrets.X}} names a suite's targets, tests and vars
// use, deduplicated, in order of appearance.
func SecretsUsed(s *Suite) []string {
	return secretRefsIn(struct {
		Vars    map[string]string
		Targets map[string]Target
		Tests   []Test
	}{s.Vars, s.Targets, s.Tests})
}

// SecretsUsedBy lists the {{secrets.X}} names one test and its target use.
func SecretsUsedBy(s *Suite, t Test) []string {
	target := s.Targets[t.Target]
	return secretRefsIn(struct {
		Vars   map[string]string
		Target Target
		Test   Test
	}{s.Vars, target, t})
}

func secretRefsIn(v any) []string {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil
	}
	return SecretRefs(string(b))
}
