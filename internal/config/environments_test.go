package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyEnvironment(t *testing.T) {
	p := Project{
		Vars:    map[string]string{"agent_url": "http://localhost:8080/chat"},
		Secrets: map[string]string{"TOKEN": "env:TOKEN", "OTHER": "env:OTHER"},
		Labels:  map[string]string{"team": "support"},
		Environments: map[string]Environment{
			"staging":    {Vars: map[string]string{"agent_url": "https://staging/chat"}, Secrets: map[string]string{"TOKEN": "env:STAGING_TOKEN"}},
			"production": {Vars: map[string]string{"agent_url": "https://prod/chat"}, Labels: map[string]string{"tier": "prod", "env": "prod-eu"}},
		},
	}
	assert.Equal(t, []string{"production", "staging"}, p.EnvironmentNames())

	s := p
	require.NoError(t, s.ApplyEnvironment("staging"))
	assert.Equal(t, "staging", s.ActiveEnvironment)
	assert.Equal(t, map[string]string{"agent_url": "https://staging/chat"}, s.EnvironmentVars())
	assert.Equal(t, map[string]string{"TOKEN": "env:STAGING_TOKEN", "OTHER": "env:OTHER"}, s.Secrets, "the environment's secrets override the project's")
	assert.Equal(t, map[string]string{"team": "support", "env": "staging"}, s.Labels, "env is labelled")
	assert.Equal(t, map[string]string{"TOKEN": "env:TOKEN", "OTHER": "env:OTHER"}, p.Secrets, "the original project is untouched")

	pr := p
	require.NoError(t, pr.ApplyEnvironment("production"))
	assert.Equal(t, map[string]string{"team": "support", "env": "prod-eu", "tier": "prod"}, pr.Labels, "the environment's own labels win, env included")
	assert.Equal(t, p.Secrets, pr.Secrets, "no secrets of its own: the project's stay")

	none := p
	require.NoError(t, none.ApplyEnvironment(""))
	assert.Empty(t, none.ActiveEnvironment)
	assert.Nil(t, none.EnvironmentVars())

	err := p.ApplyEnvironment("qa")
	assert.EqualError(t, err, `unknown environment "qa" (have production, staging)`)
	empty := Project{}
	assert.ErrorContains(t, empty.ApplyEnvironment("qa"), "declares no environments")
}
