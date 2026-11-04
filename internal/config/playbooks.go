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

//go:embed playbooks/*.yaml
var playbookFS embed.FS

// Playbook is a packaged attack family: several attacks, each an adversarial
// persona with its own tactics, a goal and the assertions that grade the
// agent's side, mapped to an OWASP Top 10 for LLM Applications category.
type Playbook struct {
	Name        string   `yaml:"name" json:"name"`
	Title       string   `yaml:"title" json:"title"`
	OWASP       string   `yaml:"owasp" json:"owasp"`
	Description string   `yaml:"description" json:"description"`
	Attacks     []Attack `yaml:"attacks" json:"attacks"`
}

// Attack is one way of pursuing a playbook's goal.
type Attack struct {
	Name string `yaml:"name" json:"name"`
	// Goal is what the attacker is after; {{agent}} is replaced by the
	// red_team block's description of the agent.
	Goal string `yaml:"goal" json:"goal"`
	// Persona is the attacker (name and style); the language and the
	// adversarial flag come from the red_team block.
	Persona  Persona     `yaml:"persona" json:"persona"`
	Tactics  []string    `yaml:"tactics,omitempty" json:"tactics,omitempty"`
	MaxTurns int         `yaml:"max_turns,omitempty" json:"max_turns,omitempty"`
	Assert   []Assertion `yaml:"assert" json:"assert"`
}

// OWASPTitles names the OWASP Top 10 for LLM Applications (2025) categories
// the playbooks map to.
var OWASPTitles = map[string]string{
	"LLM01": "Prompt Injection",
	"LLM02": "Sensitive Information Disclosure",
	"LLM03": "Supply Chain",
	"LLM04": "Data and Model Poisoning",
	"LLM05": "Improper Output Handling",
	"LLM06": "Excessive Agency",
	"LLM07": "System Prompt Leakage",
	"LLM08": "Vector and Embedding Weaknesses",
	"LLM09": "Misinformation",
	"LLM10": "Unbounded Consumption",
}

var (
	playbookOnce     sync.Once
	builtinPlaybooks []Playbook
)

// BuiltinPlaybooks returns the playbooks that ship with tamesu, by name.
func BuiltinPlaybooks() []Playbook {
	playbookOnce.Do(func() {
		entries, err := fs.ReadDir(playbookFS, "playbooks")
		if err != nil {
			panic(fmt.Sprintf("config: read playbooks: %v", err))
		}
		for _, e := range entries {
			data, err := playbookFS.ReadFile("playbooks/" + e.Name())
			if err != nil {
				panic(fmt.Sprintf("config: read playbook %s: %v", e.Name(), err))
			}
			var p Playbook
			if err := yaml.Unmarshal(data, &p); err != nil {
				panic(fmt.Sprintf("config: playbook %s: %v", e.Name(), err))
			}
			builtinPlaybooks = append(builtinPlaybooks, p)
		}
		sort.Slice(builtinPlaybooks, func(i, j int) bool { return builtinPlaybooks[i].Name < builtinPlaybooks[j].Name })
	})
	return builtinPlaybooks
}

// BuiltinPlaybook returns a playbook by name.
func BuiltinPlaybook(name string) (Playbook, bool) {
	for _, p := range BuiltinPlaybooks() {
		if p.Name == name {
			return p, true
		}
	}
	return Playbook{}, false
}

// PlaybookNames lists the built-in playbooks.
func PlaybookNames() []string {
	var names []string
	for _, p := range BuiltinPlaybooks() {
		names = append(names, p.Name)
	}
	return names
}

// RedTeam is a suite's red_team block: it expands into one test per attack
// of the chosen playbooks when the suite loads.
type RedTeam struct {
	Target    string            `yaml:"target" json:"target" jsonschema:"required,description=The target the attacks talk to (a key of targets)."`
	Agent     string            `yaml:"agent" json:"agent" jsonschema:"required,description=What the agent is and what it must never do; the attacks pursue goals written against it."`
	Playbooks []string          `yaml:"playbooks,omitempty" json:"playbooks,omitempty" jsonschema:"description=The built-in playbooks to run (tamesu playbooks lists them); every playbook when empty."`
	MaxTurns  int               `yaml:"max_turns,omitempty" json:"max_turns,omitempty" jsonschema:"description=Turns each attack gets (default 8)."`
	Repeat    *int              `yaml:"repeat,omitempty" json:"repeat,omitempty" jsonschema:"description=Times each attack runs."`
	Language  string            `yaml:"language,omitempty" json:"language,omitempty" jsonschema:"description=The attacker's language (BCP 47)."`
	Tags      []string          `yaml:"tags,omitempty" json:"tags,omitempty" jsonschema:"description=Extra tags on every generated test (red-team and playbook:<name> and owasp:<id> are always set)."`
	Vars      map[string]string `yaml:"vars,omitempty" json:"vars,omitempty" jsonschema:"description=Values for {{vars.<key>}} in the generated tests."`
	Assert    []Assertion       `yaml:"assert,omitempty" json:"assert,omitempty" jsonschema:"description=Assertions added to every generated test (a pack or a not_contains with the forbidden outcome)."`
}

// DefaultRedTeamTurns is how many turns an attack gets when neither the
// attack nor the red_team block says.
const DefaultRedTeamTurns = 8

// DefaultAttacker is the attacker's name when an attack names none.
const DefaultAttacker = "Eve"

// ExpandRedTeam appends one test per attack of the suite's red_team
// playbooks: an adversarial persona with the attack's tactics, the attack's
// goal written against the agent, and the attack's assertions (pack entries
// expand with the other packs afterwards). Unknown targets or playbooks are
// errors at the block's line and generate nothing. LoadSuite calls it before
// ExpandPacks and Validate.
func ExpandRedTeam(s *Suite, root *yaml.Node, file string) Errors {
	rt := s.RedTeam
	if rt == nil {
		return nil
	}
	var errs Errors
	fail := func(path []string, format string, args ...any) {
		errs = append(errs, &Error{File: file, Line: lineOf(root, path...), Path: describePath(s, path), Msg: fmt.Sprintf(format, args...)})
	}
	if rt.Target == "" {
		fail(at("red_team"), "red_team needs a target")
	} else if _, ok := s.Targets[rt.Target]; !ok {
		fail(at("red_team", "target"), "target %q is not declared in targets (have %s)", rt.Target, listOr(sortedKeys(s.Targets), "none"))
	}
	if strings.TrimSpace(rt.Agent) == "" {
		fail(at("red_team"), "red_team needs agent: what the agent is and what it must never do")
	}
	if rt.MaxTurns < 0 {
		fail(at("red_team", "max_turns"), "max_turns cannot be negative")
	}
	if rt.Repeat != nil && *rt.Repeat < 1 {
		fail(at("red_team", "repeat"), "repeat must be 1 or more")
	}
	names := rt.Playbooks
	if len(names) == 0 {
		names = PlaybookNames()
	}
	var playbooks []Playbook
	seen := map[string]bool{}
	for i, name := range names {
		p, ok := BuiltinPlaybook(name)
		switch {
		case !ok:
			fail(at("red_team", "playbooks", idx(i)), "unknown playbook %q (have %s)", name, strings.Join(PlaybookNames(), ", "))
		case seen[name]:
			fail(at("red_team", "playbooks", idx(i)), "playbook %q is listed twice", name)
		default:
			seen[name] = true
			playbooks = append(playbooks, p)
		}
	}
	if len(errs) > 0 {
		return errs
	}
	line := lineOf(root, "red_team")
	for _, p := range playbooks {
		for _, a := range p.Attacks {
			s.Tests = append(s.Tests, redTeamTest(rt, p, a, line))
		}
	}
	return nil
}

// redTeamTest builds the test for one attack.
func redTeamTest(rt *RedTeam, p Playbook, a Attack, line int) Test {
	persona := a.Persona
	if persona.Name == "" {
		persona.Name = DefaultAttacker
	}
	persona.Language = rt.Language
	persona.Adversarial = true
	persona.Tactics = append([]string(nil), a.Tactics...)
	turns := a.MaxTurns
	if rt.MaxTurns > 0 {
		turns = rt.MaxTurns
	}
	if turns <= 0 {
		turns = DefaultRedTeamTurns
	}
	vars := map[string]string{"agent": rt.Agent, "playbook": p.Name, "attack": a.Name}
	for k, v := range rt.Vars {
		vars[k] = v
	}
	return Test{
		Name:     fmt.Sprintf("red team: %s/%s", p.Name, a.Name),
		Target:   rt.Target,
		Tags:     append([]string{"red-team", "playbook:" + p.Name, "owasp:" + p.OWASP}, rt.Tags...),
		Repeat:   rt.Repeat,
		Vars:     vars,
		Persona:  &persona,
		Scenario: &Scenario{Goal: strings.ReplaceAll(a.Goal, "{{agent}}", strings.TrimSpace(rt.Agent)), MaxTurns: turns},
		Assert:   append(append([]Assertion(nil), a.Assert...), rt.Assert...),
		Line:     line,
	}
}
