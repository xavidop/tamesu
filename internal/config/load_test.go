package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noEnv(string) (string, bool) { return "", false }

func TestLoadSuiteSpecExample(t *testing.T) {
	path := filepath.Join("testdata", "valid", "support-agent.yaml")
	s, errs, err := LoadSuite(path, LoadOptions{Env: noEnv})
	require.NoError(t, err)
	require.NotNil(t, s)
	require.Empty(t, errs.Only(), "unexpected errors:\n%s", errs)
	require.Len(t, errs.Warnings(), 1, "warnings:\n%s", errs)
	assert.Contains(t, errs[0].Error(), "LIVEKIT_URL")
	assert.Equal(t, path+":35: warning: targets.voice.url: env var LIVEKIT_URL is not set; ${LIVEKIT_URL} is left as is", errs[0].Error())

	assert.Equal(t, "support-agent", s.Suite)
	assert.Equal(t, path, s.Path)
	assert.Len(t, s.Targets, 4)
	assert.Equal(t, TargetREST, s.Targets["api"].Type)
	assert.Equal(t, "$.reply", s.Targets["api"].Response.Text)
	assert.Equal(t, "Bearer {{secrets.API_TOKEN}}", s.Targets["api"].Headers["Authorization"])
	assert.Equal(t, 30*time.Second, s.Targets["phone"].RingTimeout.Std())
	require.Len(t, s.Tests, 4)
	phone := s.Tests[3]
	assert.Equal(t, "phone IVR routes to billing", phone.Name)
	require.NotNil(t, phone.Scenario)
	assert.Len(t, phone.Scenario.Script, 3)
	assert.Equal(t, "dtmf", phone.Scenario.Script[1].Kind())
	var kinds []string
	for _, a := range phone.Assert {
		kinds = append(kinds, a.Kind())
	}
	assert.Equal(t, []string{"judge", "metric", "metric", "call_ended_by", "transferred_to"}, kinds)
	voice := s.Tests[2]
	require.NotNil(t, voice.Repeat)
	assert.Equal(t, 5, *voice.Repeat)
	assert.Equal(t, "audio", voice.Mode)
	assert.Equal(t, []int{300, 1500}, voice.Persona.Behaviors.SilenceAfterTurnMs)

	// Strict env: the unset variable is an error.
	_, errs, err = LoadSuite(path, LoadOptions{Env: noEnv, StrictEnv: true})
	require.NoError(t, err)
	require.True(t, errs.HasErrors())
	assert.Contains(t, errs.Only()[0].Msg, "env var LIVEKIT_URL is not set")

	// With the variable set there is nothing to report.
	env := lookupFrom(map[string]string{"LIVEKIT_URL": "wss://lk.example.com"})
	s, errs, err = LoadSuite(path, LoadOptions{Env: env, StrictEnv: true})
	require.NoError(t, err)
	assert.Empty(t, errs)
	assert.Equal(t, "wss://lk.example.com", s.Targets["voice"].URL)
}

func TestLoadSuiteRealtime(t *testing.T) {
	path := filepath.Join("testdata", "valid", "realtime-agent.yaml")
	s, errs, err := LoadSuite(path, LoadOptions{Env: noEnv})
	require.NoError(t, err)
	require.NotNil(t, s)
	require.Empty(t, errs, "a realtime suite with its headers, model, session_update and an audio test is clean:\n%s", errs)
	rt := s.Targets["rt"]
	assert.Equal(t, TargetRealtime, rt.Type)
	assert.Equal(t, "gpt-realtime", rt.Model)
	assert.Equal(t, "You are the support agent of Mochi Shoes.", rt.SessionUpdate["instructions"])
	assert.Equal(t, 45*time.Second, rt.TurnTimeout.Std())
}

func TestLoadSuiteEnvUnsetIsWarningUnlessStrict(t *testing.T) {
	path := filepath.Join("testdata", "valid", "env-unset.yaml")
	s, errs, err := LoadSuite(path, LoadOptions{Env: noEnv})
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.False(t, errs.HasErrors())
	require.Len(t, errs, 1)
	assert.Equal(t, 5, errs[0].Line)
	assert.True(t, errs[0].Warning)
	assert.Equal(t, "${AGENT_URL}", s.Targets["api"].URL)

	_, errs, err = LoadSuite(path, LoadOptions{Env: noEnv, StrictEnv: true})
	require.NoError(t, err)
	assert.True(t, errs.HasErrors())
}

func TestLoadSuiteMissingFile(t *testing.T) {
	_, _, err := LoadSuite("testdata/nope.yaml", LoadOptions{})
	require.Error(t, err)
}

func TestLoadProjectSpecExample(t *testing.T) {
	env := lookupFrom(map[string]string{"LIVEKIT_URL": "wss://lk"})
	p, errs, err := LoadProject(filepath.Join("testdata", "valid", "tamesu.yaml"), LoadOptions{Env: env, StrictEnv: true})
	require.NoError(t, err)
	assert.Empty(t, errs, "%s", errs)
	assert.Equal(t, 1, p.Version)
	assert.Equal(t, []string{"googlegenai", "anthropic", "openai", "ollama", "deepgram", "elevenlabs"}, p.Genkit.Plugins)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", p.Models.Judge)
	assert.Equal(t, "deepgram/nova-3", p.Speech.STT.Model)
	assert.Equal(t, "silero", p.Speech.VAD.Provider)
	assert.Equal(t, "wss://lk", p.SIP.LiveKitURL)
	assert.Equal(t, 2, p.SIP.MaxConcurrentCalls)
	assert.Equal(t, 3*time.Minute, p.Defaults.Timeout.Std())
	assert.Equal(t, 1.0, p.Defaults.PassThreshold)
	assert.Equal(t, 2_000_000, p.Budgets.MaxTokens)
	assert.Equal(t, 5.0, p.Budgets.MaxCostUSD)
	assert.Equal(t, []string{"console", "junit", "html"}, p.Reporters)
	assert.Equal(t, "env:ANTHROPIC_API_KEY,aws-sm://ci/tamesu#anthropic", p.Secrets["ANTHROPIC_API_KEY"])
	// Keys the file does not set keep their defaults.
	assert.Equal(t, []string{"tests/**/*.yaml"}, p.Suites)
}

func TestLoadProjectMissingFileIsDefaults(t *testing.T) {
	p, errs, err := LoadProject("testdata/nope/tamesu.yaml", LoadOptions{})
	require.NoError(t, err)
	assert.Empty(t, errs)
	assert.Equal(t, DefaultProject(), p)
}

func TestLoadProjectBadVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tamesu.yaml")
	require.NoError(t, writeFile(path, "version: 2\nreporters: [console, fax]\n"))
	_, errs, err := LoadProject(path, LoadOptions{})
	require.NoError(t, err)
	require.Len(t, errs.Only(), 2, "%s", errs)
	assert.Equal(t, path+":1: version: version must be 1", errs[0].Error())
	assert.Equal(t, path+`:2: reporters[1]: unknown reporter "fax" (one of console, junit, json, html, markdown, github, slack, sarif)`, errs[1].Error())
}

func TestLoadProjectSecretRefsAreNotEnvExpanded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tamesu.yaml")
	require.NoError(t, writeFile(path, "version: 1\nsecrets:\n  TOKEN: aws-sm://${ENV}/svc#token\n"))
	p, errs, err := LoadProject(path, LoadOptions{Env: noEnv, StrictEnv: true})
	require.NoError(t, err)
	assert.Empty(t, errs, "%s", errs)
	assert.Equal(t, "aws-sm://${ENV}/svc#token", p.Secrets["TOKEN"])
}
