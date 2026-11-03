package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandLanguages(t *testing.T) {
	s := &Suite{Suite: "s", Tests: []Test{
		{Name: "returns", Target: "api", Tags: []string{"smoke"}, Languages: []string{"en", "es-ES"}, Vars: map[string]string{"days": "40"},
			Persona: &Persona{Name: "Marta", Style: "polite", Language: "fr"}},
		{Name: "no persona", Target: "api", Languages: []string{"de"}},
		{Name: "plain", Target: "api", Persona: &Persona{Language: "it"}},
	}}
	ExpandLanguages(s)
	require.Len(t, s.Tests, 4)
	assert.Equal(t, "returns [en]", s.Tests[0].Name)
	assert.Equal(t, []string{"smoke", "lang:en"}, s.Tests[0].Tags)
	assert.Equal(t, "en", s.Tests[0].Persona.Language, "the language replaces the persona's")
	assert.Equal(t, "Marta", s.Tests[0].Persona.Name, "the rest of the persona is kept")
	assert.Equal(t, map[string]string{"days": "40", "language": "en"}, s.Tests[0].Vars)
	assert.Nil(t, s.Tests[0].Languages)
	assert.Equal(t, "returns [es-ES]", s.Tests[1].Name)
	assert.Equal(t, "es-ES", s.Tests[1].Persona.Language)
	require.NotNil(t, s.Tests[2].Persona, "a test without a persona gets one")
	assert.Equal(t, "de", s.Tests[2].Persona.Language)
	assert.Equal(t, "plain", s.Tests[3].Name)
	assert.Equal(t, "it", s.Tests[3].Persona.Language)
}
