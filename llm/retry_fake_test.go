//go:build llmfake

package llm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tassert "github.com/xavidop/tamesu/assert"
	"github.com/xavidop/tamesu/internal/config"
	"github.com/xavidop/tamesu/internal/model"
	"github.com/xavidop/tamesu/llm/llmfake"
	"github.com/xavidop/tamesu/target"
)

// withConfig swaps the shared client's models, speech and retry settings for
// one test. The fake plugin never sleeps, so the backoff is a millisecond.
func withConfig(t *testing.T, c *Client, models Models, speech SpeechConfig) {
	t.Helper()
	savedModels, savedSpeech, savedRetry := c.cfg.Models, c.cfg.Speech, c.cfg.Retry
	c.cfg.Models, c.cfg.Speech = models, speech
	c.cfg.Retry = Retry{Attempts: 3, Backoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
	t.Cleanup(func() { c.cfg.Models, c.cfg.Speech, c.cfg.Retry = savedModels, savedSpeech, savedRetry })
}

func TestJudgeFallsBackWhenTheModelIsDown(t *testing.T) {
	c := client(t)
	withConfig(t, c, Models{Default: "fake/text", Judge: "fake/down", Fallback: Fallbacks{Judge: []string{"fake/judge"}}}, c.cfg.Speech)
	before := c.Usage()
	v, err := c.Judge(context.Background(), tassert.JudgeRequest{Rubric: "polite", Transcript: []tassert.Turn{{Role: "agent", Text: "Hello"}}})
	require.NoError(t, err)
	assert.True(t, v.Pass)
	u := c.Usage()
	assert.Equal(t, before.Retries+2, u.Retries, "fake/down was tried three times")
	assert.Equal(t, before.Fallbacks+1, u.Fallbacks)
	assert.Equal(t, before.Calls+1, u.Calls, "only the answer is a counted call")
}

func TestJudgeWithoutFallbackReportsWhatWasTried(t *testing.T) {
	c := client(t)
	withConfig(t, c, Models{Default: "fake/text", Judge: "fake/down"}, c.cfg.Speech)
	_, err := c.Judge(context.Background(), tassert.JudgeRequest{Rubric: "polite"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fake/down is down")
	assert.Contains(t, err.Error(), "(tried fake/down 3 times)")
}

func TestRefusedKeySkipsTheRetries(t *testing.T) {
	c := client(t)
	withConfig(t, c, Models{Default: "fake/denied", Simulator: "fake/denied", Fallback: Fallbacks{Default: []string{"fake/simulator"}}}, c.cfg.Speech)
	before := c.Usage()
	out, err := c.Simulate(context.Background(), model.SimInput{Persona: config.Persona{Name: "Marta"}, Scenario: config.Scenario{Goal: "returns"}, Turn: 1, MaxTurns: 4})
	require.NoError(t, err)
	assert.Contains(t, out.Say, "40 days ago", "the default fallback list served the simulator")
	u := c.Usage()
	assert.Equal(t, before.Retries, u.Retries, "UNAUTHENTICATED is not retried")
	assert.Equal(t, before.Fallbacks+1, u.Fallbacks)
}

func TestFlakyModelIsRetried(t *testing.T) {
	c := client(t)
	withConfig(t, c, c.cfg.Models, c.cfg.Speech)
	fakeInstance.SetScript(llmfake.Script{Fail: map[string]int{"simulator": 2}})
	t.Cleanup(func() { fakeInstance.SetScript(llmfake.Script{}) })
	before := c.Usage()
	out, err := c.Simulate(context.Background(), model.SimInput{Persona: config.Persona{Name: "Marta"}, Scenario: config.Scenario{Goal: "returns"}, Turn: 1, MaxTurns: 4})
	require.NoError(t, err)
	assert.Contains(t, out.Say, "40 days ago")
	u := c.Usage()
	assert.Equal(t, before.Retries+2, u.Retries, "two scripted failures, then the answer")
	assert.Equal(t, before.Fallbacks, u.Fallbacks)
}

func TestSpeechFallback(t *testing.T) {
	c := client(t)
	withConfig(t, c, c.cfg.Models, SpeechConfig{
		TTS: SpeechModel{Model: "fake/down", Fallback: []string{"fake/tts"}},
		STT: SpeechModel{Model: "fake/denied", Fallback: []string{"fake/stt"}},
	})
	before := c.Usage()
	a, err := c.Synthesize(context.Background(), model.SynthesizeRequest{Text: "hello there"})
	require.NoError(t, err)
	assert.Equal(t, llmfake.TTSRate, a.Rate)
	tr, err := c.Transcribe(context.Background(), model.TranscribeRequest{Audio: model.Audio{Rate: 16000, Samples: make([]int16, 16000)}})
	require.NoError(t, err)
	assert.NotEmpty(t, tr.Text)
	u := c.Usage()
	assert.Equal(t, before.Fallbacks+2, u.Fallbacks)
	assert.Equal(t, before.Retries+2, u.Retries, "the outage was retried, the refused key was not")
}

func TestChatFallbackNamesTheModel(t *testing.T) {
	c := client(t)
	withConfig(t, c, Models{Default: "fake/text", Fallback: Fallbacks{Default: []string{"fake/text"}}}, c.cfg.Speech)
	req := target.ChatRequest{Model: "fake/down", Fallback: []string{"fake/text"}, Messages: []target.ChatMessage{{Role: "user", Text: "hi"}}}
	resp, err := c.Chat(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "fake/text", resp.Model, "the answer says which model gave it")
	assert.NotEmpty(t, resp.Text)

	// Without its own list the model under test gets no fallback, whatever
	// models.fallback.default says.
	_, err = c.Chat(context.Background(), target.ChatRequest{Model: "fake/down", Messages: []target.ChatMessage{{Role: "user", Text: "hi"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "(tried fake/down 3 times)")
}
