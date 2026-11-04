package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltinPlaybooksAreValid(t *testing.T) {
	playbooks := BuiltinPlaybooks()
	require.NotEmpty(t, playbooks)
	var names []string
	for _, p := range playbooks {
		names = append(names, p.Name)
		assert.NotEmpty(t, p.Title, p.Name)
		assert.NotEmpty(t, p.Description, p.Name)
		_, known := OWASPTitles[p.OWASP]
		assert.True(t, known, "%s maps to an unknown OWASP category %q", p.Name, p.OWASP)
		require.NotEmpty(t, p.Attacks, p.Name)
		seen := map[string]bool{}
		for _, a := range p.Attacks {
			id := p.Name + "/" + a.Name
			assert.False(t, seen[a.Name], "%s: duplicate attack", id)
			seen[a.Name] = true
			assert.Regexp(t, `^[a-z0-9_]+$`, a.Name, id)
			assert.Contains(t, a.Goal, "{{agent}}", "%s: the goal is written against the agent", id)
			assert.NotEmpty(t, a.Persona.Style, "%s: the attacker has a style", id)
			assert.GreaterOrEqual(t, len(a.Tactics), 3, "%s: an attack has several tactics", id)
			require.NotEmpty(t, a.Assert, "%s: an attack grades the agent", id)
			for _, as := range a.Assert {
				kinds := as.Kinds()
				require.Len(t, kinds, 1, "%s: %v", id, kinds)
				if as.Pack != nil {
					_, ok := BuiltinPack(*as.Pack)
					assert.True(t, ok, "%s: pack %q is built in", id, *as.Pack)
				}
			}
		}
	}
	assert.Equal(t, []string{"harmful_content", "human_impersonation", "pii_extraction", "policy_drift", "prompt_injection", "scope_hijack", "system_prompt_leakage", "unauthorized_actions"}, names)
	assert.Equal(t, names, PlaybookNames())
	_, ok := BuiltinPlaybook("nope")
	assert.False(t, ok)
}

func TestExpandRedTeam(t *testing.T) {
	two := 2
	s := &Suite{Suite: "s", Targets: map[string]Target{"api": {Type: TargetREST}},
		RedTeam: &RedTeam{Target: "api", Agent: "  Mochi Shoes support; never more than 30-day returns  ", Playbooks: []string{"prompt_injection", "human_impersonation"},
			Language: "es", Repeat: &two, Tags: []string{"nightly"}, Vars: map[string]string{"shop": "mochi"}, Assert: []Assertion{{NotContains: strPtr("90 days")}}},
		Tests: []Test{{Name: "plain", Target: "api"}}}
	require.Empty(t, ExpandRedTeam(s, nil, "s.yaml"))
	pi, _ := BuiltinPlaybook("prompt_injection")
	hi, _ := BuiltinPlaybook("human_impersonation")
	require.Len(t, s.Tests, 1+len(pi.Attacks)+len(hi.Attacks), "one test per attack, after the suite's own")
	first := s.Tests[1]
	assert.Equal(t, "red team: prompt_injection/direct_override", first.Name)
	assert.Equal(t, "api", first.Target)
	assert.Equal(t, []string{"red-team", "playbook:prompt_injection", "owasp:LLM01", "nightly"}, first.Tags)
	assert.Equal(t, &two, first.Repeat)
	require.NotNil(t, first.Persona)
	assert.True(t, first.Persona.Adversarial)
	assert.Equal(t, "es", first.Persona.Language)
	assert.Equal(t, pi.Attacks[0].Tactics, first.Persona.Tactics)
	assert.Equal(t, "Eve", first.Persona.Name)
	require.NotNil(t, first.Scenario)
	assert.Contains(t, first.Scenario.Goal, "The agent: Mochi Shoes support; never more than 30-day returns.", "{{agent}} is replaced by the trimmed description")
	assert.NotContains(t, first.Scenario.Goal, "{{agent}}")
	assert.Equal(t, DefaultRedTeamTurns, first.Scenario.MaxTurns)
	assert.Equal(t, map[string]string{"agent": s.RedTeam.Agent, "playbook": "prompt_injection", "attack": "direct_override", "shop": "mochi"}, first.Vars)
	require.Len(t, first.Assert, 2, "the attack's assertions plus the block's")
	assert.Equal(t, "prompt_injection", *first.Assert[0].Pack)
	assert.Equal(t, "90 days", *first.Assert[1].NotContains)
	last := s.Tests[len(s.Tests)-1]
	assert.Equal(t, "red team: human_impersonation/play_human_colleague", last.Name)
	assert.Contains(t, last.Tags, "owasp:LLM09")

	// max_turns on the block wins over the default; every playbook when none is named.
	all := &Suite{Suite: "s", Targets: map[string]Target{"api": {Type: TargetREST}}, RedTeam: &RedTeam{Target: "api", Agent: "x", MaxTurns: 4}}
	require.Empty(t, ExpandRedTeam(all, nil, "s.yaml"))
	total := 0
	for _, p := range BuiltinPlaybooks() {
		total += len(p.Attacks)
	}
	assert.Len(t, all.Tests, total)
	assert.Equal(t, 4, all.Tests[0].Scenario.MaxTurns)

	// Nothing to do without the block.
	none := &Suite{Suite: "s", Tests: []Test{{Name: "plain"}}}
	require.Empty(t, ExpandRedTeam(none, nil, "s.yaml"))
	assert.Len(t, none.Tests, 1)
}

func TestExpandRedTeamErrors(t *testing.T) {
	s := &Suite{Suite: "s", Targets: map[string]Target{"api": {Type: TargetREST}},
		RedTeam: &RedTeam{Target: "nope", Playbooks: []string{"prompt_injection", "ghost", "prompt_injection"}, MaxTurns: -1, Repeat: intPtr(0)}}
	errs := ExpandRedTeam(s, nil, "s.yaml")
	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, e.Path+": "+e.Msg)
	}
	joined := strings.Join(msgs, "\n")
	assert.Contains(t, joined, `red_team.target: target "nope" is not declared in targets (have api)`)
	assert.Contains(t, joined, "red_team: red_team needs agent")
	assert.Contains(t, joined, "red_team.max_turns: max_turns cannot be negative")
	assert.Contains(t, joined, "red_team.repeat: repeat must be 1 or more")
	assert.Contains(t, joined, `red_team.playbooks[1]: unknown playbook "ghost" (have harmful_content, human_impersonation, pii_extraction, policy_drift, prompt_injection, scope_hijack, system_prompt_leakage, unauthorized_actions)`)
	assert.Contains(t, joined, `red_team.playbooks[2]: playbook "prompt_injection" is listed twice`)
	assert.Empty(t, s.Tests, "errors generate no tests")
}

func TestRedTeamLoadsAndExpandsPacks(t *testing.T) {
	path := writeSuite(t, `suite: rt
targets:
  api:
    type: rest
    url: http://localhost:8080/chat
    request: { body: { message: "{{message}}" } }
    response: { text: $.reply }
red_team:
  target: api
  agent: a shoe shop's support agent that must never promise more than 30-day returns
  playbooks: [pii_extraction]
  max_turns: 5
tests:
  - name: smoke
    target: api
    scenario: { goal: ask about returns, max_turns: 2 }
    assert:
      - contains: "30"
`)
	s, errs, err := LoadSuite(path, LoadOptions{Env: noEnv})
	require.NoError(t, err)
	require.False(t, errs.HasErrors(), errs)
	require.NotNil(t, s)
	pb, _ := BuiltinPlaybook("pii_extraction")
	require.Len(t, s.Tests, 1+len(pb.Attacks))
	rt := s.Tests[1]
	assert.Equal(t, "red team: pii_extraction/other_customer_record", rt.Name)
	assert.Equal(t, 8, rt.Line, "generated tests point at the red_team block")
	assert.Equal(t, 5, rt.Scenario.MaxTurns)
	require.NotEmpty(t, rt.Assert)
	assert.Nil(t, rt.Assert[0].Pack, "the pack expanded into its assertions")
	assert.Equal(t, "pii_leakage", rt.Assert[0].From)
	assert.Contains(t, s.TagsOf(rt), "owasp:LLM02")
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
