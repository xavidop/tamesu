package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
)

func validate(t *testing.T, data []byte) *config.Suite {
	t.Helper()
	path := filepath.Join(t.TempDir(), "suite.yaml")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	s, errs, err := config.LoadSuiteWithProject(path, config.DefaultProject(), config.LoadOptions{})
	require.NoError(t, err, string(data))
	require.False(t, errs.HasErrors(), "%s\n%s", errs, data)
	return s
}

func TestConversationalSuite(t *testing.T) {
	d := model.Draft{Tests: []model.DraftTest{
		{Name: "refund inside the window", Tags: []string{"Returns", "happy path"}, Persona: "Marta", Style: "polite", Language: "en", Goal: "Return shoes bought 10 days ago", MaxTurns: 6, Judges: []string{"The agent accepts the return"}, Contains: []string{"30 days"}},
		{Name: "refund inside the window", Persona: "", Goal: "", Judges: nil, NotContains: []string{"I'm just an AI"}},
	}}
	data, err := Suite(d, Options{SourceName: "docs/PRD.md", Target: "api", Type: "rest"})
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, "suite: prd\n")
	assert.Contains(t, text, "  api:\n    type: rest\n    url: \"${AGENT_URL}\"")
	assert.Contains(t, text, "tags: [returns, happy-path]")
	assert.Contains(t, text, "persona: {name: Marta, style: polite, language: en}")
	assert.Contains(t, text, "- judge: The agent accepts the return")
	assert.Contains(t, text, `- contains: 30 days`)
	assert.Contains(t, text, "name: refund inside the window (2)", "duplicate names are numbered")
	assert.Contains(t, text, "TODO: describe what the user wants")
	assert.Contains(t, text, `- not_contains: "I'm just an AI"`)
	assert.NotContains(t, text, "TODO: what must the agent do", "a test with any assertion gets no placeholder judge")
	s := validate(t, data)
	assert.Len(t, s.Tests, 2)
	assert.Equal(t, 8, s.Tests[1].Scenario.MaxTurns, "a missing max_turns gets 8")

	for _, typ := range []string{"livekit", "sip"} {
		data, err := Suite(d, Options{SourceName: "x.md", Target: "voice", Type: typ})
		require.NoError(t, err)
		validate(t, data)
	}
}

func TestBrowserSuite(t *testing.T) {
	d := model.Draft{Tests: []model.DraftTest{
		{Name: "log in", Steps: []string{"Log in as {{vars.user}} with the password {{secrets.TEST_PASSWORD}}", "Open the cart"}, Expects: []string{"The cart is empty"}, Judges: []string{"The flow completed"}},
		{Name: "no steps"},
	}}
	data, err := Suite(d, Options{SourceName: "shop.md", Target: "web", Type: "browser"})
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, "vars:\n  user: tester\n")
	assert.Contains(t, text, `TEST_PASSWORD: "env:TEST_PASSWORD"`)
	assert.Contains(t, text, `- goal: "Log in as {{vars.user}} with the password {{secrets.TEST_PASSWORD}}"`)
	assert.Contains(t, text, "- expect: The cart is empty")
	assert.Contains(t, text, `- goal: "TODO: describe the goal"`)
	s := validate(t, data)
	assert.Len(t, s.Tests[0].Steps, 3)
	assert.Nil(t, s.Tests[0].Persona)
}

func TestSuiteErrors(t *testing.T) {
	_, err := Suite(model.Draft{}, Options{Target: "api"})
	assert.ErrorContains(t, err, "drafted no tests")
	_, err = Suite(model.Draft{Tests: []model.DraftTest{{Name: "x"}}}, Options{Target: ""})
	assert.ErrorContains(t, err, "target name is required")
	_, err = Suite(model.Draft{Tests: []model.DraftTest{{Name: "x"}}}, Options{Target: "a", Type: "fax"})
	assert.ErrorContains(t, err, `unknown target type "fax"`)
}
