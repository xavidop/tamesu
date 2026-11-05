// Package lint finds the weak spots of a suite that validation accepts: tests
// that only a judge can fail, tests nobody can select, goals written twice,
// thresholds that can never bind, declarations nothing uses.
package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xavidop/tamesu/internal/config"
)

// Finding is one thing worth fixing.
type Finding struct {
	// Rule names the check (judge-only, untagged, ...).
	Rule string `json:"rule"`
	File string `json:"file"`
	// Line is the test's line, or 0 for a suite-level finding.
	Line int    `json:"line,omitempty"`
	Path string `json:"path,omitempty"`
	Msg  string `json:"message"`
}

// String formats as "<file>:<line>: <rule>: <msg>", omitting what is unknown.
func (f Finding) String() string {
	var b strings.Builder
	b.WriteString(f.File)
	if f.Line > 0 {
		fmt.Fprintf(&b, ":%d", f.Line)
	}
	b.WriteString(": ")
	b.WriteString(f.Rule)
	if f.Path != "" {
		b.WriteString(" (" + f.Path + ")")
	}
	b.WriteString(": ")
	b.WriteString(f.Msg)
	return b.String()
}

// Rules lists every rule, in documentation order.
var Rules = []string{
	"no-assertions", "judge-only", "untagged", "duplicate-goal", "short-goal", "vague-rubric",
	"turns-never-binds", "latency-never-binds", "threshold-without-repeat", "long-conversation",
	"persona-without-style", "listen-without-session", "unused-target", "unused-pack", "unused-var", "unused-secret",
}

// LongConversation is the max_turns above which a test is flagged.
const LongConversation = 20

// Suite lints a loaded suite (packs expanded, cases and languages expanded)
// against the project; raw is the suite file's text, used to see which vars
// and secrets are referenced.
func Suite(s *config.Suite, p config.Project, raw []byte) []Finding {
	if s == nil {
		return nil
	}
	l := &linter{s: s, p: p, raw: string(raw), file: s.Path}
	l.tests()
	l.declarations()
	sort.SliceStable(l.out, func(i, j int) bool {
		if l.out[i].Line != l.out[j].Line {
			return l.out[i].Line < l.out[j].Line
		}
		return l.out[i].Rule < l.out[j].Rule
	})
	return l.out
}

type linter struct {
	s    *config.Suite
	p    config.Project
	raw  string
	file string
	out  []Finding
}

func (l *linter) add(rule string, t *config.Test, path, format string, args ...any) {
	f := Finding{Rule: rule, File: l.file, Path: path, Msg: fmt.Sprintf(format, args...)}
	if t != nil {
		f.Line = t.Line
		if f.Path == "" {
			f.Path = fmt.Sprintf("tests %q", t.Name)
		} else {
			f.Path = fmt.Sprintf("tests %q.%s", t.Name, path)
		}
	}
	l.out = append(l.out, f)
}

func (l *linter) tests() {
	defaults := l.s.EffectiveDefaults(l.p)
	var everyAssert []config.Assertion
	everyAssert = append(everyAssert, l.p.Defaults.Assert...)
	if l.s.Defaults != nil {
		everyAssert = append(everyAssert, l.s.Defaults.Assert...)
	}
	goals := map[string]string{}
	for i := range l.s.Tests {
		t := &l.s.Tests[i]
		ttype := l.s.Targets[t.Target].Type
		browser := ttype == config.TargetBrowser
		all := append(append([]config.Assertion(nil), everyAssert...), t.Assert...)

		if len(all) == 0 && !browser {
			l.add("no-assertions", t, "", "the test has no assertions: it passes whenever the conversation finishes")
		} else if len(all) > 0 && !browser {
			judges := 0
			for _, a := range all {
				if a.Kind() == "judge" || a.Kind() == "evaluator" {
					judges++
				}
			}
			if judges == len(all) {
				l.add("judge-only", t, "assert", "every assertion is a model verdict; add a deterministic check (contains, tool_called, json_path, metric) a judge cannot talk itself out of")
			}
		}
		if len(l.s.TagsOf(*t)) == 0 {
			l.add("untagged", t, "", "the test has no tags and the suite none either, so --tags cannot select it")
		}
		if t.Scenario != nil {
			goal := strings.TrimSpace(t.Scenario.Goal)
			if goal != "" {
				if n := len(strings.Fields(goal)); n < 4 {
					l.add("short-goal", t, "scenario.goal", "the goal is %d word(s); the simulated user needs a situation and an aim to play it", n)
				}
				key := normalize(goal) + "\x00" + t.Target
				if other, dup := goals[key]; dup {
					l.add("duplicate-goal", t, "scenario.goal", "the same goal as test %q against the same target; vary the persona or merge them", other)
				} else {
					goals[key] = t.Name
				}
			}
			maxTurns := t.Scenario.MaxTurns
			if maxTurns == 0 {
				maxTurns = 10
			}
			if maxTurns > LongConversation {
				l.add("long-conversation", t, "scenario.max_turns", "max_turns %d: long conversations cost model calls and rarely need more than %d turns", maxTurns, LongConversation)
			}
			for j, a := range t.Assert {
				if a.Turns != nil {
					if a.Turns.Lte != nil && int(*a.Turns.Lte) >= maxTurns || a.Turns.Lt != nil && int(*a.Turns.Lt) > maxTurns {
						l.add("turns-never-binds", t, fmt.Sprintf("assert[%d].turns", j), "the limit is at or above max_turns (%d): the conversation can never have more turns, so the assertion always passes", maxTurns)
					}
				}
			}
		}
		timeout := defaults.Timeout
		if t.Timeout != nil {
			timeout = *t.Timeout
		}
		if timeout > 0 {
			ms := float64(time.Duration(timeout) / time.Millisecond)
			for j, a := range t.Assert {
				if a.Metric != nil && strings.HasPrefix(a.Metric.Name, "turn_latency_ms") {
					if (a.Metric.Lt != nil && *a.Metric.Lt >= ms) || (a.Metric.Lte != nil && *a.Metric.Lte >= ms) {
						l.add("latency-never-binds", t, fmt.Sprintf("assert[%d].metric", j), "the %s limit is at or above the test timeout (%s): a turn that slow fails the run before the metric is compared", a.Metric.Name, time.Duration(timeout))
					}
				}
			}
		}
		repeat := defaults.Repeat
		if t.Repeat != nil {
			repeat = *t.Repeat
		}
		if t.PassThreshold != nil && *t.PassThreshold < 1 && repeat <= 1 {
			l.add("threshold-without-repeat", t, "pass_threshold", "pass_threshold %.2f with a single run: the test still needs that one run to pass; set repeat", *t.PassThreshold)
		}
		for j, a := range t.Assert {
			if a.Judge != nil && len(strings.Fields(a.Judge.Rubric)) < 5 {
				l.add("vague-rubric", t, fmt.Sprintf("assert[%d].judge", j), "the rubric %q is %d word(s); say what the agent must do or avoid", a.Judge.Rubric, len(strings.Fields(a.Judge.Rubric)))
			}
		}
		if t.Persona != nil && t.Persona.Name != "" && strings.TrimSpace(t.Persona.Style) == "" && !browser {
			l.add("persona-without-style", t, "persona", "%s has no style: the simulated user plays a generic customer", t.Persona.Name)
		}
	}
}

func (l *linter) declarations() {
	used := map[string]bool{}
	for _, t := range l.s.Tests {
		used[t.Target] = true
		if t.Counterpart != nil {
			used[t.Counterpart.Target] = true
		}
	}
	if l.s.RedTeam != nil {
		used[l.s.RedTeam.Target] = true
	}
	for _, name := range sortedKeys(l.s.Targets) {
		if !used[name] {
			l.add("unused-target", nil, "targets."+name, "no test uses target %q", name)
		}
		if t := l.s.Targets[name]; t.Response != nil && t.Response.Listen != nil && t.Response.Listen.Session == "" {
			l.add("listen-without-session", nil, "targets."+name+".response.listen", "response.listen has no session: with --parallel the agent's requests all go to the oldest open session; set session to the JSONPath of the session id in the agent's requests ($.chat_id)")
		}
	}
	referenced := map[string]bool{}
	for _, t := range l.s.Tests {
		for _, a := range t.Assert {
			if from, _, _ := strings.Cut(a.From, "/"); from != "" {
				referenced[from] = true
			}
		}
	}
	if l.s.Defaults != nil {
		for _, a := range l.s.Defaults.Assert {
			if from, _, _ := strings.Cut(a.From, "/"); from != "" {
				referenced[from] = true
			}
		}
	}
	for _, name := range sortedKeys(l.s.Packs) {
		if !referenced[name] && !strings.Contains(l.raw, "pack: "+name) {
			l.add("unused-pack", nil, "packs."+name, "pack %q is defined but no assertion uses it", name)
		}
	}
	for _, name := range sortedKeys(l.s.Vars) {
		if !regexp.MustCompile(`vars\.` + regexp.QuoteMeta(name) + `\b`).MatchString(l.raw) {
			l.add("unused-var", nil, "vars."+name, "var %q is declared but {{vars.%s}} appears nowhere in the suite", name, name)
		}
	}
	for _, name := range sortedKeys(l.s.Secrets) {
		if !strings.Contains(l.raw, "secrets."+name) {
			l.add("unused-secret", nil, "secrets."+name, "secret %q is declared but {{secrets.%s}} appears nowhere in the suite", name, name)
		}
	}
}

var spaces = regexp.MustCompile(`\s+`)

// normalize lowercases a goal and collapses punctuation and whitespace, so
// two goals that differ by a comma count as the same.
func normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(".,;:!?\"'()[]", r) {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(spaces.ReplaceAllString(s, " "))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
