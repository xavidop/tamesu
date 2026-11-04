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

func TestEmbed(t *testing.T) {
	c := client(t)
	withConfig(t, c, Models{Default: "fake/text"}, c.cfg.Speech)
	before := c.Usage()
	resp, err := c.Embed(context.Background(), tassert.EmbedRequest{Texts: []string{
		"Our return window is 30 days from delivery, so a return after 40 days is not possible.",
		"The return window is 30 days from delivery; returning after 40 days is not possible.",
		"The weather in Madrid is sunny this week.",
	}})
	require.NoError(t, err)
	assert.Equal(t, "fake/embedding", resp.Model, "the default embedder of the default model's provider")
	require.Len(t, resp.Vectors, 3)
	assert.Greater(t, tassert.Cosine(resp.Vectors[0], resp.Vectors[1]), 0.8, "a paraphrase shares most words")
	assert.Less(t, tassert.Cosine(resp.Vectors[0], resp.Vectors[2]), 0.2, "unrelated text shares none")
	u := c.Usage()
	assert.Equal(t, before.Calls+1, u.Calls, "one embedding call")
	assert.Greater(t, u.InputTokens, before.InputTokens)

	empty, err := c.Embed(context.Background(), tassert.EmbedRequest{})
	require.NoError(t, err)
	assert.Empty(t, empty.Vectors)

	withConfig(t, c, Models{Default: "anthropic/claude-sonnet-4-6"}, c.cfg.Speech)
	_, err = c.Embed(context.Background(), tassert.EmbedRequest{Texts: []string{"x"}})
	assert.ErrorContains(t, err, "models.embedder", "a provider without embeddings needs an explicit one")

	withConfig(t, c, Models{Default: "fake/text", Embedder: "fake/down", Fallback: Fallbacks{Embedder: []string{"fake/embedding"}}}, c.cfg.Speech)
	resp, err = c.Embed(context.Background(), tassert.EmbedRequest{Texts: []string{"x"}})
	require.NoError(t, err)
	assert.Equal(t, "fake/embedding", resp.Model, "the embedder fallback list applies")
}

func TestDefaultEmbedder(t *testing.T) {
	assert.Equal(t, "googleai/text-embedding-004", DefaultEmbedder("googleai"))
	assert.Equal(t, "openai/text-embedding-3-small", DefaultEmbedder("openai"))
	assert.Equal(t, "", DefaultEmbedder("anthropic"))
	assert.Equal(t, "", DefaultEmbedder("ollama"))
}

func TestGroundFake(t *testing.T) {
	c := client(t)
	withConfig(t, c, Models{Default: "fake/text", Judge: "fake/judge"}, c.cfg.Speech)
	v, err := c.Ground(context.Background(), tassert.GroundRequest{
		Context:    []string{"Returns are accepted within 30 days of delivery."},
		Transcript: []tassert.Turn{{Role: "user", Text: "Can I return after 40 days?"}, {Role: "agent", Text: "No, the window is 30 days.", Index: 1}},
	})
	require.NoError(t, err)
	require.Len(t, v.Claims, 1)
	assert.True(t, v.Claims[0].Supported)
	assert.Equal(t, "fake grounding check", v.Reason)
	assert.Equal(t, "1", v.PromptVersion)

	v, err = c.Ground(context.Background(), tassert.GroundRequest{Context: []string{"x"}, Transcript: []tassert.Turn{{Role: "agent", Text: "UNSUPPORTED claim here", Index: 1}}})
	require.NoError(t, err)
	supported, total := v.Supported()
	assert.Equal(t, 1, supported)
	assert.Equal(t, 2, total, "the fake marks one claim unsupported when the conversation says UNSUPPORTED")
}

func TestExtractFake(t *testing.T) {
	c := client(t)
	withConfig(t, c, Models{Default: "fake/text", Judge: "fake/judge"}, c.cfg.Speech)
	res, err := c.Extract(context.Background(), model.ExtractRequest{Description: "the order number the agent mentioned", Text: "Your order ORD-42 is on its way."})
	require.NoError(t, err)
	assert.True(t, res.Found)
	assert.Equal(t, "ORD-42", res.Value, "the fake takes the first word with a digit, without punctuation")
	assert.Equal(t, "1", res.PromptVersion)
	res, err = c.Extract(context.Background(), model.ExtractRequest{Description: "a date", Text: "Hello there"})
	require.NoError(t, err)
	assert.False(t, res.Found)
}
