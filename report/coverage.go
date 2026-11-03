package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Coverage is what a run exercised: which targets, tags, tools, assertion
// kinds, stop reasons and attack tactics the tests reached, and which tools
// the suites declared that no test ever made the agent call.
type Coverage struct {
	Tests       int            `json:"tests"`
	Runs        int            `json:"runs"`
	Targets     []CoverageItem `json:"targets"`
	Tags        []CoverageItem `json:"tags,omitempty"`
	Tools       []ToolCoverage `json:"tools,omitempty"`
	Uncalled    []string       `json:"uncalled_tools,omitempty"`
	Assertions  []CoverageItem `json:"assertions,omitempty"`
	StopReasons []CoverageItem `json:"stop_reasons,omitempty"`
	Tactics     []CoverageItem `json:"tactics,omitempty"`
}

// CoverageItem counts the tests of one target, tag, assertion kind, stop
// reason or tactic, and how many of them passed.
type CoverageItem struct {
	Name   string `json:"name"`
	Count  int    `json:"count"`
	Passed int    `json:"passed"`
}

// ToolCoverage is one tool the agent called: how many times, in how many
// tests, and whether a suite declared it (a model target's tools).
type ToolCoverage struct {
	Name     string `json:"name"`
	Calls    int    `json:"calls"`
	Tests    int    `json:"tests"`
	Declared bool   `json:"declared,omitempty"`
}

// CoverageFiles are the files WriteCoverage writes into a run directory.
const (
	CoverageJSON = "coverage.json"
	CoverageMD   = "coverage.md"
)

// BuildCoverage computes the coverage of a run. declared maps a target name
// to the tools its suite declares (the model target's tools, the tools
// named by tool_called assertions), so tools never called can be listed.
func BuildCoverage(s *Summary, declared map[string][]string) *Coverage {
	c := &Coverage{Tests: len(s.Tests)}
	targets := map[string]*CoverageItem{}
	tags := map[string]*CoverageItem{}
	kinds := map[string]*CoverageItem{}
	stops := map[string]*CoverageItem{}
	tactics := map[string]*CoverageItem{}
	tools := map[string]*ToolCoverage{}
	bump := func(m map[string]*CoverageItem, name string, passed bool) {
		it, ok := m[name]
		if !ok {
			it = &CoverageItem{Name: name}
			m[name] = it
		}
		it.Count++
		if passed {
			it.Passed++
		}
	}
	for _, t := range s.Tests {
		name := t.Target
		if t.Type != "" {
			name = t.Target + " (" + t.Type + ")"
		}
		bump(targets, name, t.Passed)
		for _, tag := range t.Tags {
			bump(tags, tag, t.Passed)
		}
		seenKinds := map[string]bool{}
		seenStops := map[string]bool{}
		seenTactics := map[string]bool{}
		seenTools := map[string]bool{}
		for _, r := range t.Runs {
			c.Runs++
			for _, a := range r.Assertions {
				kind := assertionKind(a.Name)
				if kind != "" && !seenKinds[kind] {
					seenKinds[kind] = true
					bump(kinds, kind, t.Passed)
				}
			}
			if reason := stopFamily(r.StopReason); reason != "" && !seenStops[reason] {
				seenStops[reason] = true
				bump(stops, reason, t.Passed)
			}
			for _, e := range r.Events {
				if e.Kind == "tactic" {
					tactic := tacticOf(e.Detail)
					if !seenTactics[tactic] {
						seenTactics[tactic] = true
						bump(tactics, tactic, t.Passed)
					}
				}
			}
			for _, call := range r.ToolCalls {
				tc, ok := tools[call.Name]
				if !ok {
					tc = &ToolCoverage{Name: call.Name}
					tools[call.Name] = tc
				}
				tc.Calls++
				if !seenTools[call.Name] {
					seenTools[call.Name] = true
					tc.Tests++
				}
			}
		}
	}
	declaredSet := map[string]bool{}
	for _, list := range declared {
		for _, name := range list {
			declaredSet[name] = true
		}
	}
	for name := range declaredSet {
		if tc, ok := tools[name]; ok {
			tc.Declared = true
		} else {
			c.Uncalled = append(c.Uncalled, name)
		}
	}
	sort.Strings(c.Uncalled)
	c.Targets = sortedItems(targets)
	c.Tags = sortedItems(tags)
	c.Assertions = sortedItems(kinds)
	c.StopReasons = sortedItems(stops)
	c.Tactics = sortedItems(tactics)
	for _, tc := range tools {
		c.Tools = append(c.Tools, *tc)
	}
	sort.Slice(c.Tools, func(i, j int) bool {
		if c.Tools[i].Calls != c.Tools[j].Calls {
			return c.Tools[i].Calls > c.Tools[j].Calls
		}
		return c.Tools[i].Name < c.Tools[j].Name
	})
	return c
}

func sortedItems(m map[string]*CoverageItem) []CoverageItem {
	out := make([]CoverageItem, 0, len(m))
	for _, it := range m {
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// assertionKind is the first word of an assertion's name ("judge", "contains",
// "slo/turn_latency_ms_p95"), without a pack prefix.
func assertionKind(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 && !strings.HasPrefix(name, "slo/") {
		name = name[i+1:]
	}
	kind, _, _ := strings.Cut(name, " ")
	return kind
}

// stopFamily is a stop reason without its explanation ("simulator: the goal
// is met" is "simulator").
func stopFamily(reason string) string {
	family, _, _ := strings.Cut(reason, ":")
	return strings.TrimSpace(family)
}

// tacticOf strips the turn prefix of a tactic event ("turn 3: role-play").
func tacticOf(detail string) string {
	if _, rest, ok := strings.Cut(detail, ": "); ok && strings.HasPrefix(detail, "turn ") {
		return rest
	}
	return detail
}

// Markdown renders the coverage as a report section.
func (c *Coverage) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Coverage\n\n%d tests, %d runs.\n\n", c.Tests, c.Runs)
	table := func(title, col string, items []CoverageItem) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n\n| %s | Tests | Passed |\n|---|---|---|\n", title, col)
		for _, it := range items {
			fmt.Fprintf(&b, "| `%s` | %d | %d |\n", it.Name, it.Count, it.Passed)
		}
		b.WriteString("\n")
	}
	table("Targets", "Target", c.Targets)
	table("Tags", "Tag", c.Tags)
	if len(c.Tools) > 0 || len(c.Uncalled) > 0 {
		b.WriteString("## Tools\n\n")
		if len(c.Tools) > 0 {
			b.WriteString("| Tool | Calls | Tests | Declared |\n|---|---|---|---|\n")
			for _, t := range c.Tools {
				declared := ""
				if t.Declared {
					declared = "yes"
				}
				fmt.Fprintf(&b, "| `%s` | %d | %d | %s |\n", t.Name, t.Calls, t.Tests, declared)
			}
			b.WriteString("\n")
		}
		if len(c.Uncalled) > 0 {
			fmt.Fprintf(&b, "Declared but never called: `%s`.\n\n", strings.Join(c.Uncalled, "`, `"))
		}
	}
	table("Assertions", "Kind", c.Assertions)
	table("Stop reasons", "Reason", c.StopReasons)
	table("Attack tactics", "Tactic", c.Tactics)
	return b.String()
}

// Console renders the coverage for a terminal.
func (c *Coverage) Console() string {
	var b strings.Builder
	fmt.Fprintf(&b, "coverage: %d tests, %d runs\n", c.Tests, c.Runs)
	section := func(title string, items []CoverageItem) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s\n", title)
		for _, it := range items {
			fmt.Fprintf(&b, "  %-40s %3d tests, %3d passed\n", it.Name, it.Count, it.Passed)
		}
	}
	section("targets", c.Targets)
	section("tags", c.Tags)
	if len(c.Tools) > 0 || len(c.Uncalled) > 0 {
		b.WriteString("\ntools\n")
		for _, t := range c.Tools {
			declared := ""
			if t.Declared {
				declared = ", declared"
			}
			fmt.Fprintf(&b, "  %-40s %3d calls in %d tests%s\n", t.Name, t.Calls, t.Tests, declared)
		}
		for _, name := range c.Uncalled {
			fmt.Fprintf(&b, "  %-40s never called\n", name)
		}
	}
	section("assertions", c.Assertions)
	section("stop reasons", c.StopReasons)
	section("attack tactics", c.Tactics)
	return b.String()
}

// WriteCoverage writes coverage.json and coverage.md into dir.
func WriteCoverage(dir string, c *Coverage) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, CoverageJSON), append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, CoverageMD), []byte(c.Markdown()), 0o644)
}
