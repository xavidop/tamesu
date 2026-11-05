package rime

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

var (
	g       *genkit.Genkit
	plugin  *Rime
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
	plugin = &Rime{APIKey: "rime-test", BaseURL: srv.URL, HTTPClient: srv.Client()}
	g = genkit.Init(context.Background(), genkit.WithPlugins(plugin))
	return g
}

func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	handler.Store(&h)
}

func mediaPart(t *testing.T, resp *ai.ModelResponse) *ai.Part {
	t.Helper()
	for _, p := range resp.Message.Content {
		if p.IsMedia() {
			return p
		}
	}
	require.FailNow(t, "the answer has no media part")
	return nil
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
		w.Header().Set("Content-Type", "audio/pcm")
		_, _ = w.Write(pcm)
	})
	speed := 1.2
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("rime/arcana"),
		ai.WithPrompt("Hello there"), ai.WithConfig(Config{Voice: "celeste", Language: "es-ES", Speed: &speed, ReduceLatency: true}))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/v1/rime-tts", got.URL.Path)
	assert.Equal(t, "Bearer rime-test", got.Header.Get("Authorization"))
	assert.Equal(t, "audio/pcm", got.Header.Get("Accept"))
	assert.Equal(t, "application/json", got.Header.Get("Content-Type"))
	assert.Equal(t, "celeste", body["speaker"])
	assert.Equal(t, "Hello there", body["text"])
	assert.Equal(t, "arcana", body["modelId"])
	assert.Equal(t, "spa", body["lang"], "es-ES maps to Rime's code")
	assert.Equal(t, float64(24000), body["samplingRate"])
	assert.Equal(t, 1.2, body["speedAlpha"])
	assert.Equal(t, true, body["reduceLatency"])

	media := mediaPart(t, resp)
	assert.Equal(t, "audio/L16;rate=24000", media.ContentType)
	assert.True(t, strings.HasPrefix(media.Text, "data:audio/L16;rate=24000;base64,"))
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(media.Text, "data:audio/L16;rate=24000;base64,"))
	require.NoError(t, err)
	assert.Len(t, data, 48000)
	assert.Equal(t, 11, resp.Usage.InputCharacters)
	assert.Equal(t, 1, resp.Usage.OutputAudioFiles)
	assert.InDelta(t, 1.0, resp.Usage.Custom["audio_seconds"], 0.001)
}

func TestDefaults(t *testing.T) {
	g := setup(t)
	var body map[string]any
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write(make([]byte, 32000)) // one second at 16 kHz
	})
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("rime/mistv2"), ai.WithPrompt("hola"))
	require.NoError(t, err)
	assert.Equal(t, "luna", body["speaker"])
	assert.Equal(t, "eng", body["lang"])
	assert.Equal(t, "mistv2", body["modelId"])
	assert.Equal(t, float64(24000), body["samplingRate"])
	_, hasSpeed := body["speedAlpha"]
	assert.False(t, hasSpeed, "speedAlpha is only sent when Speed is set")
	_, hasLatency := body["reduceLatency"]
	assert.False(t, hasLatency, "reduceLatency is only sent when set")
	assert.Equal(t, "audio/L16;rate=24000", mediaPart(t, resp).ContentType)

	resp, err = genkit.Generate(context.Background(), g, ai.WithModelName("rime/mist"), ai.WithPrompt("hola"), ai.WithConfig(Config{SampleRate: 16000}))
	require.NoError(t, err)
	assert.Equal(t, float64(16000), body["samplingRate"])
	assert.Equal(t, "audio/L16;rate=16000", mediaPart(t, resp).ContentType)
	assert.InDelta(t, 1.0, resp.Usage.Custom["audio_seconds"], 0.001)
}

func TestErrors(t *testing.T) {
	g := setup(t)
	called := false
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write(make([]byte, 100))
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("rime/arcana"), ai.WithPrompt("   "))
	assert.ErrorContains(t, err, "rime: the request has no text to speak")
	assert.False(t, called, "no request is made without text")

	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"invalid speaker"}`, http.StatusBadRequest)
	})
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("rime/arcana"), ai.WithPrompt("hola"), ai.WithConfig(Config{Voice: "nobody"}))
	require.Error(t, err)
	assert.ErrorContains(t, err, "rime: 400 Bad Request")
	assert.ErrorContains(t, err, `{"message":"invalid speaker"}`)
}

func TestNoAPIKey(t *testing.T) {
	t.Setenv("RIME_API_KEY", "")
	r := &Rime{}
	r.Init(context.Background())
	_, err := r.synthesize(context.Background(), "arcana", &ai.ModelRequest{Messages: []*ai.Message{ai.NewUserTextMessage("hola")}}, Config{})
	assert.EqualError(t, err, "rime: no API key (set RIME_API_KEY or Rime.APIKey)")
}

func TestLangCode(t *testing.T) {
	cases := map[string]string{
		"":      "eng",
		"en":    "eng",
		"en-US": "eng",
		"es-ES": "spa",
		"ES":    "spa",
		"fr":    "fra",
		"de-DE": "ger",
		"it":    "ita",
		"pt-BR": "por",
		"nl":    "nld",
		"ja":    "jpn",
		"ko":    "kor",
		"zh-CN": "cmn",
		"hi":    "hin",
		"ar":    "ara",
		"pl":    "pol",
		"ru":    "rus",
		"sv":    "swe",
		"tr":    "tur",
		"eng":   "eng",
		"spa":   "spa",
		"CMN":   "cmn",
	}
	for in, want := range cases {
		assert.Equal(t, want, langCode(in), "langCode(%q)", in)
	}
}
