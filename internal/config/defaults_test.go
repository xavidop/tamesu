package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultsMergeRules(t *testing.T) {
	p := Defaults{Repeat: 1, Assert: []Assertion{{Pack: ptrStr("pii_leakage")}}, SLO: map[string]float64{"turn_latency_ms_p95": 3000, "llm_cost_usd": 0.05}}
	s := &Defaults{Assert: []Assertion{{NotContains: ptrStr("as an AI")}}, SLO: map[string]float64{"turn_latency_ms_p95": 2000}}
	m := p.Merge(s)
	require.Len(t, m.Assert, 2, "the suite's assertions follow the project's")
	assert.Equal(t, "pii_leakage", *m.Assert[0].Pack)
	assert.Equal(t, map[string]float64{"turn_latency_ms_p95": 2000, "llm_cost_usd": 0.05}, m.SLO, "the suite overlays the project's SLOs")
	assert.Len(t, p.Assert, 1, "the project's defaults are not modified")
	assert.Equal(t, p, p.Merge(nil))
}

func TestExpandPacksInDefaults(t *testing.T) {
	s := &Suite{Suite: "s", Defaults: &Defaults{Assert: []Assertion{{Pack: ptrStr("pii_leakage")}, {Contains: ptrStr("x")}}},
		Targets: map[string]Target{"api": {Type: TargetREST, URL: "http://x"}}, Tests: []Test{{Name: "t", Target: "api"}}}
	errs := ExpandPacks(s, nil, "suite.yaml")
	require.Empty(t, errs)
	assert.Greater(t, len(s.Defaults.Assert), 2, "the pack expanded into its assertions")
	assert.Equal(t, "pii_leakage", s.Defaults.Assert[0].From)
	assert.Nil(t, s.Defaults.Assert[len(s.Defaults.Assert)-1].Pack)

	_, err := ExpandAssertionPacks(s, []Assertion{{Pack: ptrStr("nope")}})
	assert.ErrorContains(t, err, `unknown pack "nope"`)
	out, err := ExpandAssertionPacks(s, []Assertion{{Pack: ptrStr("prompt_injection")}})
	require.NoError(t, err)
	assert.NotEmpty(t, out)
}

func TestValidateDefaultsRules(t *testing.T) {
	s := &Suite{Suite: "s", Defaults: &Defaults{Assert: []Assertion{{}}, SLO: map[string]float64{"turn_latency_ms_p95": 0}},
		Targets: map[string]Target{"api": {Type: TargetREST, URL: "http://x"}}, Tests: []Test{{Name: "t", Target: "api", Scenario: &Scenario{Goal: "g"}}}}
	errs := Validate(s, DefaultProject(), nil)
	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	joined := ""
	for _, m := range msgs {
		joined += m + "\n"
	}
	assert.Contains(t, joined, "defaults.assert[0]")
	assert.Contains(t, joined, "defaults.slo.turn_latency_ms_p95: the maximum must be above 0")
}

func ptrStr(s string) *string { return &s }
