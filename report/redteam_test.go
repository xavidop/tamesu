package report

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func redTeamSummary() *Summary {
	mk := func(name string, tags []string, passed bool, runs ...*RunOutcome) *TestOutcome {
		if runs == nil {
			runs = []*RunOutcome{{Passed: passed}}
		}
		return &TestOutcome{Suite: "rt", Name: name, Target: "api", Tags: tags, Passed: passed, Runs: runs}
	}
	pi := []string{"red-team", "playbook:prompt_injection", "owasp:LLM01"}
	pii := []string{"red-team", "playbook:pii_extraction", "owasp:LLM02"}
	return &Summary{Tests: []*TestOutcome{
		mk("red team: prompt_injection/direct_override", pi, true),
		mk("red team: prompt_injection/role_play", pi, false),
		mk("red team: prompt_injection/encoded_instructions", pi, false, &RunOutcome{Passed: false, Error: "boom", ErrorKind: KindInfra}),
		mk("red team: pii_extraction/other_customer_record", pii, true),
		mk("plain test", []string{"smoke"}, false),
	}}
}

func TestBuildRedTeam(t *testing.T) {
	results := BuildRedTeam(redTeamSummary())
	require.Len(t, results, 2, "one row per playbook; the plain test is not red team")
	assert.Equal(t, PlaybookResult{Name: "prompt_injection", OWASP: "LLM01", Attacks: 3, Held: 1, Broken: []string{"role_play"}, Errored: []string{"encoded_instructions"}}, results[0])
	assert.Equal(t, PlaybookResult{Name: "pii_extraction", OWASP: "LLM02", Attacks: 1, Held: 1}, results[1])
	assert.Nil(t, BuildRedTeam(&Summary{Tests: []*TestOutcome{{Name: "x", Tags: []string{"smoke"}}}}))
}

func TestRedTeamRendering(t *testing.T) {
	results := BuildRedTeam(redTeamSummary())
	md := RedTeamMarkdown(results)
	assert.Contains(t, md, "## Red team")
	assert.Contains(t, md, "| `prompt_injection` | LLM01 Prompt Injection | 3 | 1 | role_play, errors: encoded_instructions |")
	assert.Contains(t, md, "| `pii_extraction` | LLM02 Sensitive Information Disclosure | 1 | 1 | none |")
	console := RedTeamConsole(results)
	assert.Contains(t, console, "prompt_injection")
	assert.Contains(t, console, "1/3 attacks held · got through: role_play · errors: encoded_instructions")
	assert.Contains(t, console, "1/1 attacks held · held")
	assert.Equal(t, "red team: 2 of 4 attacks held · got through: prompt_injection/role_play", RedTeamLine(results))
	assert.Empty(t, RedTeamMarkdown(nil))
	assert.Empty(t, RedTeamConsole(nil))
	assert.Empty(t, RedTeamLine(nil))

	full := RenderMarkdown(redTeamSummary())
	assert.Contains(t, full, "## Red team", "the run report carries the table")
	cov := BuildCoverage(redTeamSummary(), nil)
	require.Len(t, cov.Playbooks, 2)
	assert.Contains(t, cov.Markdown(), "## Red team")
	assert.Contains(t, cov.Console(), "red team")
}
