package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xavidop/tamesu/internal/config"
)

func TestParseMatrixFlags(t *testing.T) {
	variants, err := parseMatrixFlags([]string{"models.judge=a/1, b/2", "vars.prompt=v1"})
	require.NoError(t, err)
	require.Len(t, variants, 2, "the cartesian product, in flag order")
	assert.Equal(t, "models.judge=a/1, vars.prompt=v1", variants[0].String())
	assert.Equal(t, "models.judge=b/2, vars.prompt=v1", variants[1].String())
	assert.Equal(t, "judge-a-1+prompt-v1", variants[0].Slug())
	assert.Equal(t, map[string]string{"models.judge": "b/2", "vars.prompt": "v1"}, variants[1].Labels())

	none, err := parseMatrixFlags(nil)
	require.NoError(t, err)
	assert.Nil(t, none)

	_, err = parseMatrixFlags([]string{"models.judge"})
	assert.ErrorContains(t, err, "want KEY=value1,value2")
	_, err = parseMatrixFlags([]string{"budgets.max=1,2"})
	assert.ErrorContains(t, err, "the key is one of")
	_, err = parseMatrixFlags([]string{"targets.api.headers=a,b"})
	assert.ErrorContains(t, err, "the key is one of")
}

func TestApplyMatrix(t *testing.T) {
	p := config.Project{Models: config.Models{Default: "x/1", Judge: "x/2"}}
	s1 := &config.Suite{Suite: "a", Models: &config.Models{Judge: "suite/judge"}, Targets: map[string]config.Target{"prompt": {Type: "model", Model: "x/1", SystemFile: "v1.md"}}}
	s2 := &config.Suite{Suite: "b", Targets: map[string]config.Target{"api": {Type: "rest", URL: "http://a"}}}
	v := matrixVariant{{Key: "models.judge", Value: "y/9"}, {Key: "vars.prompt", Value: "v2"}, {Key: "targets.prompt.system_file", Value: "v2.md"}, {Key: "speech.tts.model", Value: "tts/1"}}
	require.NoError(t, applyMatrix(&p, []*config.Suite{s1, s2}, v))
	assert.Equal(t, "y/9", p.Models.Judge)
	assert.Equal(t, "y/9", s1.Models.Judge, "a suite that overrides models gets the value too, so it cannot undo it")
	assert.Equal(t, "v2", s1.Vars["prompt"])
	assert.Equal(t, "v2", s2.Vars["prompt"])
	assert.Equal(t, "v2.md", s1.Targets["prompt"].SystemFile)
	assert.Equal(t, "tts/1", p.Speech.TTS.Model)
	assert.Equal(t, "http://a", s2.Targets["api"].URL, "other targets are untouched")

	err := applyMatrix(&p, []*config.Suite{s1, s2}, matrixVariant{{Key: "targets.nope.url", Value: "x"}})
	assert.ErrorContains(t, err, "no selected suite declares a target named \"nope\"")
}
