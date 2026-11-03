package cartesia

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One Genkit instance per process.
var (
	g       *genkit.Genkit
	handler atomic.Pointer[http.HandlerFunc]
)

func setup(t *testing.T) *genkit.Genkit {
	t.Helper()
	if g != nil {
		return g
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*handler.Load())(w, r)
	}))
	g = genkit.Init(context.Background(), genkit.WithPlugins(&Cartesia{APIKey: "ca-test", BaseURL: srv.URL, HTTPClient: srv.Client()}))
	return g
}

func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	handler.Store(&h)
}

func TestSynthesize(t *testing.T) {
	g := setup(t)
	pcm := make([]byte, 48000) // one second at 24 kHz
	var got *http.Request
	var body map[string]any
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "audio/pcm")
		_, _ = w.Write(pcm)
	})
	speed := 1.1
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("cartesia/sonic-3.6"),
		ai.WithPrompt("Hello there"), ai.WithConfig(Config{Voice: "voice-1", Language: "en-GB", Speed: &speed}))
	require.NoError(t, err)
	part := resp.Message.Content[0]
	require.True(t, part.IsMedia())
	assert.Equal(t, "audio/L16;rate=24000", part.ContentType)
	data, err := base64.StdEncoding.DecodeString(part.Text[len("data:audio/L16;rate=24000;base64,"):])
	require.NoError(t, err)
	assert.Equal(t, pcm, data)
	assert.Equal(t, 11, resp.Usage.InputCharacters)
	assert.Equal(t, 1, resp.Usage.OutputAudioFiles)
	assert.InDelta(t, 1.0, resp.Usage.Custom["audio_seconds"], 0.001)

	require.NotNil(t, got)
	assert.Equal(t, "/tts/bytes", got.URL.Path)
	assert.Equal(t, "Bearer ca-test", got.Header.Get("Authorization"))
	assert.Equal(t, DefaultVersion, got.Header.Get("Cartesia-Version"))
	assert.Equal(t, "sonic-3.6", body["model_id"])
	assert.Equal(t, "Hello there", body["transcript"])
	assert.Equal(t, map[string]any{"mode": "id", "id": "voice-1"}, body["voice"])
	assert.Equal(t, map[string]any{"container": "raw", "encoding": "pcm_s16le", "sample_rate": float64(24000)}, body["output_format"])
	assert.Equal(t, "en-GB", body["language"])
	assert.Equal(t, map[string]any{"speed": 1.1}, body["generation_config"])
}

func TestSynthesizeDefaults(t *testing.T) {
	g := setup(t)
	var body map[string]any
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write(make([]byte, 4))
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("cartesia/sonic-3"), ai.WithPrompt("Hi"))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "id", "id": DefaultVoice}, body["voice"], "the library voice when none is named")
	_, hasGen := body["generation_config"]
	assert.False(t, hasGen)
	_, hasLang := body["language"]
	assert.False(t, hasLang)
}

func TestTranscribe(t *testing.T) {
	g := setup(t)
	audio := []byte("RIFF....WAVEfmt fake")
	var got *http.Request
	var fields map[string]string
	var file []byte
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		require.NoError(t, r.ParseMultipartForm(1<<20))
		fields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
		f, _, err := r.FormFile("file")
		require.NoError(t, err)
		file, _ = io.ReadAll(f)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"transcript","text":"Hello, I need a refund.","language":"en","duration":2.5}`))
	})
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("cartesia/ink-whisper"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))),
		ai.WithConfig(Config{Language: "en-US"}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", resp.Text())
	assert.Equal(t, 2.5, resp.Usage.Custom["audio_seconds"])
	assert.Equal(t, 1, resp.Usage.InputAudioFiles)

	require.NotNil(t, got)
	assert.Equal(t, "/stt", got.URL.Path)
	assert.Equal(t, "Bearer ca-test", got.Header.Get("Authorization"))
	assert.Equal(t, map[string]string{"model": "ink-whisper", "language": "en"}, fields, "a locale is cut to its language")
	assert.Equal(t, audio, file)
}

func TestErrors(t *testing.T) {
	g := setup(t)
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("cartesia/sonic-3.6"), ai.WithPrompt("Hi"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "invalid api key")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("cartesia/ink-whisper"), ai.WithPrompt("no audio here"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no audio media part")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("cartesia/sonic-3.6"), ai.WithMessages(ai.NewUserMessage(ai.NewTextPart("   "))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no text to speak")
}

func TestNoKey(t *testing.T) {
	c := &Cartesia{HTTPClient: http.DefaultClient}
	t.Setenv("CARTESIA_API_KEY", "")
	actions := c.Init(context.Background())
	assert.Len(t, actions, len(DefaultTTSModels)+len(DefaultSTTModels))
	_, err := c.synthesize(context.Background(), "sonic-3.6", &ai.ModelRequest{Messages: []*ai.Message{ai.NewUserTextMessage("hi")}}, Config{})
	assert.ErrorContains(t, err, "no API key")
	assert.True(t, IsSTT("ink-whisper"))
	assert.False(t, IsSTT("sonic-3.6"))
}
