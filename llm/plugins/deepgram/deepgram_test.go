package deepgram

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// The server outlives the first test: genkit.Init runs once per process.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*handler.Load())(w, r)
	}))
	g = genkit.Init(context.Background(), genkit.WithPlugins(&Deepgram{APIKey: "dg-test", BaseURL: srv.URL, HTTPClient: srv.Client()}))
	return g
}

func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	handler.Store(&h)
}

func TestTranscribe(t *testing.T) {
	g := setup(t)
	audio := []byte("RIFF....WAVEfmt fake")
	var got *http.Request
	var body []byte
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"metadata":{"duration":2.5},"results":{"channels":[{"alternatives":[{"transcript":"Hello, I need a refund.","confidence":0.97}]}]}}`))
	})
	resp, err := genkit.Generate(context.Background(), g,
		ai.WithModelName("deepgram/nova-3"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))),
		ai.WithConfig(Config{Language: "en", Keyterms: []string{"tamesu"}}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", resp.Text())
	assert.Equal(t, 2.5, resp.Usage.Custom["audio_seconds"])
	assert.Equal(t, 0.97, resp.Usage.Custom["confidence"])
	assert.Equal(t, 1, resp.Usage.InputAudioFiles)

	require.NotNil(t, got)
	assert.Equal(t, "/v1/listen", got.URL.Path)
	assert.Equal(t, "nova-3", got.URL.Query().Get("model"))
	assert.Equal(t, "en", got.URL.Query().Get("language"))
	assert.Equal(t, "true", got.URL.Query().Get("smart_format"))
	assert.Equal(t, []string{"tamesu"}, got.URL.Query()["keyterm"])
	assert.Equal(t, "Token dg-test", got.Header.Get("Authorization"))
	assert.Equal(t, "audio/wav", got.Header.Get("Content-Type"))
	assert.Equal(t, audio, body, "the audio bytes are the request body")
}

func TestErrors(t *testing.T) {
	g := setup(t)
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"err_code":"INVALID_AUTH"}`, http.StatusUnauthorized)
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("deepgram/nova-2"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	assert.ErrorContains(t, err, "401")
	assert.ErrorContains(t, err, "INVALID_AUTH")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("deepgram/nova-2"), ai.WithPrompt("no audio here"))
	assert.ErrorContains(t, err, "no audio media part")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("deepgram/nova-2"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "https://example.com/a.wav"))))
	assert.ErrorContains(t, err, "data URL")
}

func TestDecodePart(t *testing.T) {
	mime, data, err := decodePart(ai.NewMediaPart("", "data:audio/ogg;codecs=opus;base64,"+base64.StdEncoding.EncodeToString([]byte("x"))))
	require.NoError(t, err)
	assert.Equal(t, "audio/ogg", mime)
	assert.Equal(t, []byte("x"), data)
	mime, _, err = decodePart(ai.NewMediaPart("audio/L16;rate=16000", base64.StdEncoding.EncodeToString([]byte("yy"))))
	require.NoError(t, err)
	assert.Equal(t, "audio/L16;rate=16000", mime, "raw base64 keeps the part's content type")
	_, _, err = decodePart(ai.NewMediaPart("", "data:audio/wav,notbase64"))
	assert.Error(t, err)
}

func TestSynthesize(t *testing.T) {
	g := setup(t)
	pcm := make([]byte, 48000) // one second at 24 kHz, 16-bit
	var got *http.Request
	var body map[string]any
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "audio/l16")
		_, _ = w.Write(pcm)
	})
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("deepgram/aura-2"),
		ai.WithPrompt("Hello there"), ai.WithConfig(Config{Voice: "apollo"}))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/v1/speak", got.URL.Path)
	assert.Equal(t, "aura-2-apollo-en", got.URL.Query().Get("model"), "the bare voice completes the family model")
	assert.Equal(t, "linear16", got.URL.Query().Get("encoding"))
	assert.Equal(t, "24000", got.URL.Query().Get("sample_rate"))
	assert.Equal(t, "Token dg-test", got.Header.Get("Authorization"))
	assert.Equal(t, "application/json", got.Header.Get("Content-Type"))
	assert.Equal(t, map[string]any{"text": "Hello there"}, body)

	var media *ai.Part
	for _, p := range resp.Message.Content {
		if p.IsMedia() {
			media = p
		}
	}
	require.NotNil(t, media, "the answer is an audio media part")
	assert.Equal(t, "audio/L16;rate=24000", media.ContentType)
	assert.True(t, strings.HasPrefix(media.Text, "data:audio/L16;rate=24000;base64,"))
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(media.Text, "data:audio/L16;rate=24000;base64,"))
	require.NoError(t, err)
	assert.Len(t, data, 48000)
	assert.Equal(t, 11, resp.Usage.InputCharacters)
	assert.Equal(t, 1, resp.Usage.OutputAudioFiles)
	assert.InDelta(t, 1.0, resp.Usage.Custom["audio_seconds"], 0.001)

	// A full voice model with no voice config speaks as itself, at the rate asked.
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("deepgram/aura-asteria-en"), ai.WithPrompt("hi"), ai.WithConfig(Config{SampleRate: 16000}))
	require.NoError(t, err)
	assert.Equal(t, "aura-asteria-en", got.URL.Query().Get("model"))
	assert.Equal(t, "16000", got.URL.Query().Get("sample_rate"))

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("deepgram/aura-2"), ai.WithPrompt("   "))
	assert.ErrorContains(t, err, "no text to speak")
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"err_msg":"unknown model"}`, http.StatusBadRequest)
	})
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("deepgram/aura-2"), ai.WithPrompt("hola"))
	assert.ErrorContains(t, err, "400")
	assert.ErrorContains(t, err, "unknown model")
}

func TestVoiceModel(t *testing.T) {
	tests := []struct{ model, voice, want string }{
		{"aura-2", "", "aura-2-thalia-en"},
		{"aura-2", "apollo", "aura-2-apollo-en"},
		{"aura-2", "Apollo", "aura-2-apollo-en"},
		{"aura-2-thalia-en", "", "aura-2-thalia-en"},
		{"aura-2-thalia-en", "apollo", "aura-2-apollo-en"},
		{"aura-2-celeste-es", "nestor", "aura-2-nestor-es"},
		{"aura-2-thalia-en", "aura-2-celeste-es", "aura-2-celeste-es"},
		{"aura", "", "aura-thalia-en"},
		{"aura-asteria-en", "", "aura-asteria-en"},
		{"aura-asteria-en", "orion", "aura-orion-en"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, VoiceModel(tt.model, tt.voice), "%s + %q", tt.model, tt.voice)
	}
	assert.True(t, IsTTS("aura-2-thalia-en"))
	assert.False(t, IsTTS("nova-3"))
}
