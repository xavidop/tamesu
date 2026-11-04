package lint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/config"
)

const weakSuite = `suite: weak
vars: { used: a, unused: b }
secrets: { USED: env:USED, UNUSED: env:UNUSED }
packs:
  tone:
    - judge: "The agent stays polite and never blames the customer"
  orphan:
    - contains: "x"
targets:
  api:
    type: rest
    url: http://localhost:8080/{{vars.used}}
    headers: { Authorization: "Bearer {{secrets.USED}}" }
    response: { text: $.reply }
  spare:
    type: rest
    url: http://localhost:9090/chat
    response: { text: $.reply, listen: { path: "POST /bot*/sendMessage" } }
tests:
  - name: judge only
    target: api
    persona: { name: Marta }
    scenario: { goal: "Return shoes bought forty days ago", max_turns: 6 }
    assert:
      - judge: "Polite"
      - pack: tone
  - name: nothing checked
    target: api
    tags: [smoke]
    scenario: { goal: "Hi" }
  - name: never binds
    target: api
    tags: [smoke]
    timeout: 10s
    pass_threshold: 0.5
    scenario: { goal: "return shoes, bought forty days ago!", max_turns: 30 }
    assert:
      - turns: { lte: 30 }
      - metric: { name: turn_latency_ms_p95, lt: 20000 }
      - contains: "30 days"
`

const goodSuite = `suite: good
tags: [regression]
targets:
  api:
    type: rest
    url: http://localhost:8080/chat
    response: { text: $.reply }
tests:
  - name: refund policy
    target: api
    repeat: 3
    pass_threshold: 0.67
    persona: { name: Marta, style: polite but confused }
    scenario: { goal: "Find out whether shoes bought 40 days ago can be returned", max_turns: 6 }
    assert:
      - judge: "The agent states the 30-day policy and offers the voucher"
      - contains: "30 days"
      - turns: { lte: 4 }
      - metric: { name: turn_latency_ms_p95, lt: 3000 }
`

func load(t *testing.T, body string) (*config.Suite, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "suite.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	s, errs, err := config.LoadSuite(path, config.LoadOptions{Env: func(string) (string, bool) { return "", false }})
	require.NoError(t, err)
	require.False(t, errs.HasErrors(), errs)
	return s, []byte(body)
}

func rules(fs []Finding) map[string]int {
	out := map[string]int{}
	for _, f := range fs {
		out[f.Rule]++
	}
	return out
}

func TestSuiteFindings(t *testing.T) {
	s, raw := load(t, weakSuite)
	findings := Suite(s, config.Project{}, raw)
	got := rules(findings)
	assert.Equal(t, map[string]int{
		"judge-only": 1, "untagged": 1, "vague-rubric": 1, "persona-without-style": 1,
		"no-assertions": 1, "short-goal": 1,
		"duplicate-goal": 1, "turns-never-binds": 1, "latency-never-binds": 1, "threshold-without-repeat": 1, "long-conversation": 1,
		"unused-target": 1, "unused-pack": 1, "unused-var": 1, "unused-secret": 1, "listen-without-session": 1,
	}, got)
	byRule := map[string]Finding{}
	for _, f := range findings {
		byRule[f.Rule] = f
	}
	assert.Equal(t, 20, byRule["judge-only"].Line, "a test finding points at the test")
	assert.Equal(t, `tests "judge only".assert`, byRule["judge-only"].Path)
	assert.Contains(t, byRule["duplicate-goal"].Msg, `test "judge only"`, "punctuation and case do not make a goal different")
	assert.Contains(t, byRule["turns-never-binds"].Msg, "max_turns (30)")
	assert.Contains(t, byRule["latency-never-binds"].Msg, "10s")
	assert.Equal(t, "targets.spare", byRule["unused-target"].Path)
	assert.Equal(t, 0, byRule["unused-target"].Line, "a suite-level finding has no line")
	assert.Equal(t, "targets.spare.response.listen", byRule["listen-without-session"].Path)
	assert.Equal(t, "packs.orphan", byRule["unused-pack"].Path)
	assert.Equal(t, "vars.unused", byRule["unused-var"].Path)
	assert.Equal(t, "secrets.UNUSED", byRule["unused-secret"].Path)
	assert.Equal(t, s.Path+":20: judge-only (tests \"judge only\".assert): every assertion is a model verdict; add a deterministic check (contains, tool_called, json_path, metric) a judge cannot talk itself out of", byRule["judge-only"].String())
	for _, r := range Rules {
		assert.Contains(t, got, r, "every rule fires on the weak suite")
	}
}

func TestSuiteClean(t *testing.T) {
	s, raw := load(t, goodSuite)
	assert.Empty(t, Suite(s, config.Project{}, raw))
	assert.Nil(t, Suite(nil, config.Project{}, nil))
}

func TestDefaultsCountAsAssertions(t *testing.T) {
	s, raw := load(t, goodSuite)
	s.Tests[0].Assert = nil
	p := config.Project{Defaults: config.Defaults{Assert: []config.Assertion{{Contains: strPtr("30 days")}}}}
	findings := Suite(s, p, raw)
	assert.NotContains(t, rules(findings), "no-assertions", "defaults.assert applies to every test")
	assert.NotContains(t, rules(findings), "judge-only")
}

func strPtr(s string) *string { return &s }
