package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestModelsMergeFallbackAndRetry(t *testing.T) {
	p := Models{
		Default:  "googleai/gemini-2.5-flash",
		Judge:    "anthropic/claude-sonnet-4-6",
		Fallback: ModelFallback{Default: []string{"openai/gpt-4.1-mini"}, Judge: []string{"googleai/gemini-2.5-pro"}},
		Retry:    ModelRetry{Attempts: 3, Backoff: Duration(500 * time.Millisecond), MaxBackoff: Duration(10 * time.Second)},
	}
	s := &Models{
		Fallback: ModelFallback{Judge: []string{"openai/gpt-4.1"}},
		Retry:    ModelRetry{Attempts: 5},
	}
	m := p.Merge(s)
	assert.Equal(t, []string{"openai/gpt-4.1"}, m.Fallback.Judge, "the suite's list replaces the project's")
	assert.Equal(t, []string{"openai/gpt-4.1-mini"}, m.Fallback.Default, "a list the suite does not set is kept")
	assert.Equal(t, ModelRetry{Attempts: 5, Backoff: Duration(500 * time.Millisecond), MaxBackoff: Duration(10 * time.Second)}, m.Retry, "retry fields merge one by one")
	assert.Equal(t, p, p.Merge(nil))
	assert.Equal(t, p, p.Merge(&Models{}), "an empty override changes nothing")
	assert.Equal(t, []string{"googleai/gemini-2.5-pro"}, p.Fallback.Lists()["judge"])
}
