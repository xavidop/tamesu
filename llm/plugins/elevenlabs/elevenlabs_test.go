package elevenlabs

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
	plugin  *ElevenLabs
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
	plugin = &ElevenLabs{APIKey: "xi-test", BaseURL: srv.URL, HTTPClient: srv.Client()}
	g = genkit.Init(context.Background(), genkit.WithPlugins(plugin))
	return g
}

func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	handler.Store(&h)
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
	speed := 1.1
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("elevenlabs/eleven_flash_v2_5"),
		ai.WithPrompt("Hello there"), ai.WithConfig(Config{Voice: "Adam", Language: "en", Speed: &speed}))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "/v1/text-to-speech/pNInz6obpgDQGcFmaJgB", got.URL.Path, "the premade name resolves without a lookup")
	assert.Equal(t, "pcm_24000", got.URL.Query().Get("output_format"))
	assert.Equal(t, "xi-test", got.Header.Get("xi-api-key"))
	assert.Equal(t, "Hello there", body["text"])
	assert.Equal(t, "eleven_flash_v2_5", body["model_id"])
	assert.Equal(t, "en", body["language_code"])
	assert.Equal(t, map[string]any{"speed": 1.1}, body["voice_settings"])

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
	assert.InDelta(t, 1.0, resp.Usage.Custom["audio_seconds"], 0.001)
}

func TestVoiceLookupAndErrors(t *testing.T) {
	g := setup(t)
	var paths []string
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/voices" {
			_, _ = w.Write([]byte(`{"voices":[{"voice_id":"AAAAAAAAAAAAAAAAAAAA","name":"Marta Clone"}]}`))
			return
		}
		_, _ = w.Write(make([]byte, 100))
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("elevenlabs/eleven_v3"), ai.WithPrompt("hola"), ai.WithConfig(Config{Voice: "marta clone"}))
	require.NoError(t, err)
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("elevenlabs/eleven_v3"), ai.WithPrompt("hola"), ai.WithConfig(Config{Voice: "Marta Clone"}))
	require.NoError(t, err)
	assert.Equal(t, []string{"/v1/voices", "/v1/text-to-speech/AAAAAAAAAAAAAAAAAAAA", "/v1/text-to-speech/AAAAAAAAAAAAAAAAAAAA"}, paths, "the library is listed once")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("elevenlabs/eleven_v3"), ai.WithPrompt("hola"), ai.WithConfig(Config{Voice: "Nobody"}))
	assert.ErrorContains(t, err, `unknown voice "Nobody"`)
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("elevenlabs/eleven_v3"), ai.WithPrompt("   "))
	assert.ErrorContains(t, err, "no text to speak")

	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"quota"}`, http.StatusPaymentRequired)
	})
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("elevenlabs/eleven_v3"), ai.WithPrompt("hola"))
	assert.ErrorContains(t, err, "402")
}

func TestMimeFor(t *testing.T) {
	mime, rate := mimeFor("pcm_16000")
	assert.Equal(t, "audio/L16;rate=16000", mime)
	assert.Equal(t, 16000, rate)
	mime, rate = mimeFor("mp3_44100_128")
	assert.Equal(t, "audio/mpeg", mime)
	assert.Equal(t, 0, rate)
}

func TestTranscribe(t *testing.T) {
	g := setup(t)
	audio := []byte("RIFF....WAVEfmt fake")
	var got *http.Request
	var fields map[string]string
	var file []byte
	var filename string
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		require.NoError(t, r.ParseMultipartForm(1<<20))
		fields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
		f, hdr, err := r.FormFile("file")
		require.NoError(t, err)
		filename = hdr.Filename
		file, _ = io.ReadAll(f)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"language_code":"en","language_probability":0.99,"text":"Hello, I need a refund.","words":[` +
			`{"text":"Hello,","start":0.1,"end":0.5,"type":"word","logprob":0},` +
			`{"text":" ","start":0.5,"end":0.6,"type":"spacing","logprob":0},` +
			`{"text":"I need a refund.","start":0.6,"end":2.5,"type":"word","logprob":-0.6931471805599453}]}`))
	})
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("elevenlabs/scribe_v1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))),
		ai.WithConfig(Config{Language: "en-GB", Diarize: true}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", resp.Text())
	assert.Equal(t, 1, resp.Usage.InputAudioFiles)
	assert.Equal(t, 2.5, resp.Usage.Custom["audio_seconds"], "the last word's end")
	assert.InDelta(t, 0.75, resp.Usage.Custom["confidence"], 1e-9, "the mean word probability, spacing left out")

	require.NotNil(t, got)
	assert.Equal(t, "/v1/speech-to-text", got.URL.Path)
	assert.Equal(t, "xi-test", got.Header.Get("xi-api-key"))
	assert.True(t, strings.HasPrefix(got.Header.Get("Content-Type"), "multipart/form-data"))
	assert.Equal(t, "scribe_v1", fields["model_id"])
	assert.Equal(t, "en", fields["language_code"], "the locale is cut to its ISO 639-1 code")
	assert.Equal(t, "false", fields["tag_audio_events"])
	assert.Equal(t, "true", fields["diarize"])
	assert.Equal(t, "audio.wav", filename)
	assert.Equal(t, audio, file, "the audio bytes are the uploaded file")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("elevenlabs/scribe_v1"), ai.WithPrompt("no audio here"))
	assert.ErrorContains(t, err, "no audio media part")
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("elevenlabs/scribe_v1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "https://example.com/a.wav"))))
	assert.ErrorContains(t, err, "data URL")
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"invalid_api_key"}`, http.StatusUnauthorized)
	})
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("elevenlabs/scribe_v2"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	assert.ErrorContains(t, err, "401")
	assert.ErrorContains(t, err, "invalid_api_key")
	assert.True(t, IsSTT("scribe_v1"))
	assert.False(t, IsSTT("eleven_v3"))
	assert.Equal(t, ".mp3", extensionFor("audio/mpeg"))
	assert.Equal(t, ".wav", extensionFor("audio/L16;rate=16000"))
}
