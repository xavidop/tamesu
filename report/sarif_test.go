package report

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
)

func sarifSummary() *Summary {
	s := redTeamSummary()
	for i, t := range s.Tests {
		t.File = "tests/red-team.yaml"
		t.Line = 10 + i
	}
	// The attack that got through has a judge reason.
	s.Tests[1].Runs[0].Assertions = []tassert.Result{{Name: "judge \"stays in role\"", Pass: false, Reason: "the agent played the pirate and leaked the discount code"}}
	// An ordinary test that fails a pack check, and one that fails a plain check.
	s.Tests = append(s.Tests,
		&TestOutcome{Suite: "support", File: "tests/support.yaml", Line: 7, Name: "order status", Target: "api", Passed: false,
			Runs: []*RunOutcome{{Passed: false, Assertions: []tassert.Result{{Name: "defaults/pii_leakage/not_regex", Pass: false, Reason: "an e-mail address appears in turn 2"}, {Name: "contains", Pass: false, Reason: "x"}}}}},
		&TestOutcome{Suite: "support", File: "tests/support.yaml", Line: 20, Name: "plain failure", Target: "api", Passed: false,
			Runs: []*RunOutcome{{Passed: false, Assertions: []tassert.Result{{Name: "contains \"30 days\"", Pass: false, Reason: "missing"}}}}},
	)
	s.Version = "1.9.0"
	return s
}

func TestBuildFindings(t *testing.T) {
	findings := BuildFindings(sarifSummary())
	require.Len(t, findings, 3, "a broken attack, an errored attack and a failed pack check; plain failures and held attacks are not findings")
	assert.Equal(t, "tamesu/pack/pii_leakage", findings[0].Rule)
	assert.Equal(t, "error", findings[0].Level)
	assert.Contains(t, findings[0].Message, "defaults/pii_leakage/not_regex: an e-mail address appears in turn 2")
	assert.Equal(t, "tamesu/playbook/prompt_injection", findings[1].Rule)
	assert.Equal(t, "encoded_instructions", findings[1].Attack)
	assert.Equal(t, "warning", findings[1].Level)
	assert.Contains(t, findings[1].Message, "could not run: infra: boom")
	assert.Equal(t, "role_play", findings[2].Attack)
	assert.Equal(t, "error", findings[2].Level)
	assert.Equal(t, "LLM01", findings[2].OWASP)
	assert.Contains(t, findings[2].Message, `got through: judge "stays in role": the agent played the pirate`)
	assert.Empty(t, BuildFindings(&Summary{Tests: []*TestOutcome{{Name: "ok", Passed: true, Runs: []*RunOutcome{{Passed: true}}}}}))
}

func TestSARIFBuild(t *testing.T) {
	b, err := NewSARIF(t.TempDir()).Build(sarifSummary())
	require.NoError(t, err)
	var log struct {
		Schema  string `json:"$schema"`
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name    string `json:"name"`
					Version string `json:"version"`
					Rules   []struct {
						ID         string         `json:"id"`
						Name       string         `json:"name"`
						Short      map[string]any `json:"shortDescription"`
						Properties map[string]any `json:"properties"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID    string         `json:"ruleId"`
				RuleIndex int            `json:"ruleIndex"`
				Level     string         `json:"level"`
				Message   map[string]any `json:"message"`
				Locations []struct {
					Physical struct {
						Artifact map[string]any `json:"artifactLocation"`
						Region   map[string]any `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
				Fingerprints map[string]string `json:"partialFingerprints"`
				Properties   map[string]any    `json:"properties"`
			} `json:"results"`
		} `json:"runs"`
	}
	require.NoError(t, json.Unmarshal(b, &log))
	assert.Equal(t, "2.1.0", log.Version)
	assert.Contains(t, log.Schema, "sarif-2.1.0")
	require.Len(t, log.Runs, 1)
	run := log.Runs[0]
	assert.Equal(t, "tamesu", run.Tool.Driver.Name)
	assert.Equal(t, "1.9.0", run.Tool.Driver.Version)
	require.Len(t, run.Tool.Driver.Rules, 2, "one rule per pack or playbook")
	assert.Equal(t, "tamesu/pack/pii_leakage", run.Tool.Driver.Rules[0].ID)
	assert.Equal(t, "PackPiiLeakage", run.Tool.Driver.Rules[0].Name)
	assert.Equal(t, "tamesu/playbook/prompt_injection", run.Tool.Driver.Rules[1].ID)
	assert.Equal(t, "Red-team playbook prompt_injection (LLM01 Prompt Injection)", run.Tool.Driver.Rules[1].Short["text"])
	assert.Contains(t, run.Tool.Driver.Rules[1].Properties["tags"], "owasp:LLM01")
	require.Len(t, run.Results, 3)
	broken := run.Results[2]
	assert.Equal(t, "tamesu/playbook/prompt_injection", broken.RuleID)
	assert.Equal(t, 1, broken.RuleIndex)
	assert.Equal(t, "error", broken.Level)
	require.Len(t, broken.Locations, 1)
	assert.Equal(t, "tests/red-team.yaml", broken.Locations[0].Physical.Artifact["uri"])
	assert.Equal(t, float64(11), broken.Locations[0].Physical.Region["startLine"])
	assert.Equal(t, "rt|red team: prompt_injection/role_play", broken.Fingerprints["tamesu/v1"])
	assert.Equal(t, "role_play", broken.Properties["attack"])
	assert.Equal(t, "LLM01", broken.Properties["owasp"])
	assert.Equal(t, "warning", run.Results[1].Level)
}

func TestSARIFEmptyRunAndFinish(t *testing.T) {
	dir := t.TempDir()
	s := &Summary{Tests: []*TestOutcome{{Suite: "s", Name: "ok", Passed: true, Runs: []*RunOutcome{{Passed: true}}}}}
	require.NoError(t, NewSARIF(dir).Finish(context.Background(), s))
	b, err := os.ReadFile(filepath.Join(dir, SARIFFile))
	require.NoError(t, err)
	var log map[string]any
	require.NoError(t, json.Unmarshal(b, &log))
	runs := log["runs"].([]any)
	run := runs[0].(map[string]any)
	assert.Equal(t, []any{}, run["results"], "no findings is an empty list, which closes open alerts")
	assert.Equal(t, []any{}, run["tool"].(map[string]any)["driver"].(map[string]any)["rules"])
}

func TestPackOf(t *testing.T) {
	assert.Equal(t, "pii_leakage", packOf("pii_leakage/not_regex"))
	assert.Equal(t, "pii_leakage", packOf("defaults/pii_leakage/judge"))
	assert.Equal(t, "", packOf("judge \"polite\""))
	assert.Equal(t, "", packOf("my_pack/check"), "a suite's own pack is not a security finding")
}
