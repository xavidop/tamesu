package openaispeech

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
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
	// A plugin named "openai" with openai/ chat models is initialised too, as
	// Genkit's OpenAI plugin would be: both share the prefix and must coexist
	// in one registry.
	g = genkit.Init(context.Background(), genkit.WithPlugins(
		chatPlugin{},
		&OpenAISpeech{APIKey: "sk-test", BaseURL: srv.URL, HTTPClient: srv.Client()},
	))
	return g
}

// chatPlugin stands in for Genkit's OpenAI plugin: the name "openai" and a
// chat model under the same prefix.
type chatPlugin struct{}

func (chatPlugin) Name() string { return "openai" }

func (chatPlugin) Init(context.Context) []api.Action {
	return []api.Action{ai.NewModelAction("openai/gpt-4o", &ai.ModelOptions{Label: "OpenAI gpt-4o", Supports: &ai.ModelSupports{Multiturn: true}},
		func(ctx context.Context, req *ai.ModelRequest, _ struct{}, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return &ai.ModelResponse{Request: req, FinishReason: ai.FinishReasonStop, Message: ai.NewModelTextMessage("chat")}, nil
		})}
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
	speed := 1.2
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("openai/gpt-4o-mini-tts"),
		ai.WithPrompt("Hello there"), ai.WithConfig(Config{Voice: "nova", Instructions: "Speak warmly.", Speed: &speed}))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/v1/audio/speech", got.URL.Path)
	assert.Equal(t, "Bearer sk-test", got.Header.Get("Authorization"))
	assert.Equal(t, "application/json", got.Header.Get("Content-Type"))
	assert.Equal(t, "gpt-4o-mini-tts", body["model"])
	assert.Equal(t, "Hello there", body["input"])
	assert.Equal(t, "nova", body["voice"])
	assert.Equal(t, "pcm", body["response_format"])
	assert.Equal(t, "Speak warmly.", body["instructions"])
	assert.Equal(t, 1.2, body["speed"])

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

	// Defaults: alloy, pcm, no optional fields.
	body = nil
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("openai/tts-1"), ai.WithPrompt("hi"))
	require.NoError(t, err)
	assert.Equal(t, "alloy", body["voice"])
	assert.Equal(t, "tts-1", body["model"])
	_, hasSpeed := body["speed"]
	assert.False(t, hasSpeed)
	_, hasInstructions := body["instructions"]
	assert.False(t, hasInstructions)
}

func TestTranscribe(t *testing.T) {
	g := setup(t)
	audio := wav(16000, 32000) // one second
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
		_, _ = w.Write([]byte(`{"text":"Hello, I need a refund.","usage":{"type":"tokens","input_tokens":14,"output_tokens":9,"total_tokens":23}}`))
	})
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("openai/gpt-4o-transcribe"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))),
		ai.WithConfig(Config{Language: "en-GB", Prompt: "tamesu"}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", resp.Text())
	assert.Equal(t, 1, resp.Usage.InputAudioFiles)
	assert.InDelta(t, 1.0, resp.Usage.Custom["audio_seconds"], 0.001, "without a duration from the API, the WAV header gives it")

	require.NotNil(t, got)
	assert.Equal(t, "/v1/audio/transcriptions", got.URL.Path)
	assert.Equal(t, "Bearer sk-test", got.Header.Get("Authorization"))
	assert.True(t, strings.HasPrefix(got.Header.Get("Content-Type"), "multipart/form-data"))
	assert.Equal(t, "gpt-4o-transcribe", fields["model"])
	assert.Equal(t, "json", fields["response_format"])
	assert.Equal(t, "en", fields["language"], "the locale is cut to its ISO 639-1 code")
	assert.Equal(t, "tamesu", fields["prompt"])
	assert.Equal(t, "audio.wav", filename)
	assert.Equal(t, audio, file, "the audio bytes are the uploaded file")

	// whisper-1 asks for verbose_json and reads its duration.
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(1<<20))
		fields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
		_, _ = w.Write([]byte(`{"task":"transcribe","language":"english","duration":2.5,"text":" Our return window is 30 days. ","segments":[]}`))
	})
	resp, err = genkit.Generate(context.Background(), g, ai.WithModelName("openai/whisper-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))))
	require.NoError(t, err)
	assert.Equal(t, "verbose_json", fields["response_format"])
	_, hasLanguage := fields["language"]
	assert.False(t, hasLanguage)
	assert.Equal(t, "Our return window is 30 days.", resp.Text())
	assert.Equal(t, 2.5, resp.Usage.Custom["audio_seconds"])

	// A usage block with seconds (the duration type) is read too.
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"ok","usage":{"type":"duration","seconds":3}}`))
	})
	resp, err = genkit.Generate(context.Background(), g, ai.WithModelName("openai/gpt-4o-mini-transcribe"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.NoError(t, err)
	assert.Equal(t, 3.0, resp.Usage.Custom["audio_seconds"])
}

func TestErrors(t *testing.T) {
	g := setup(t)
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Incorrect API key provided"}}`, http.StatusUnauthorized)
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("openai/tts-1-hd"), ai.WithPrompt("hola"))
	assert.ErrorContains(t, err, "401")
	assert.ErrorContains(t, err, "Incorrect API key")
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("openai/whisper-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	assert.ErrorContains(t, err, "401")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("openai/tts-1"), ai.WithPrompt("   "))
	assert.ErrorContains(t, err, "no text to speak")
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("openai/whisper-1"), ai.WithPrompt("no audio here"))
	assert.ErrorContains(t, err, "no audio media part")
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("openai/whisper-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "https://example.com/a.wav"))))
	assert.ErrorContains(t, err, "data URL")
}

func TestHelpers(t *testing.T) {
	assert.True(t, IsSTT("gpt-4o-transcribe"))
	assert.True(t, IsSTT("gpt-4o-mini-transcribe"))
	assert.True(t, IsSTT("whisper-1"))
	assert.False(t, IsSTT("gpt-4o-mini-tts"))
	assert.False(t, IsSTT("tts-1-hd"))

	mime, rate := mimeFor("pcm")
	assert.Equal(t, "audio/L16;rate=24000", mime)
	assert.Equal(t, 24000, rate)
	mime, rate = mimeFor("mp3")
	assert.Equal(t, "audio/mpeg", mime)
	assert.Equal(t, 0, rate)

	assert.InDelta(t, 0.5, wavSeconds(wav(8000, 8000)), 1e-9, "8000 bytes of 16-bit mono at 8 kHz")
	assert.Equal(t, 0.0, wavSeconds([]byte("not a wav at all")))
	assert.Equal(t, 0.0, wavSeconds(nil))

	assert.Equal(t, ".wav", extensionFor("audio/wav"))
	assert.Equal(t, ".mp3", extensionFor("audio/mpeg"))
	assert.Equal(t, ".ogg", extensionFor("audio/ogg; codecs=opus"))
	assert.Equal(t, ".wav", extensionFor("audio/L16;rate=16000"), "raw PCM is sent as .wav, the closest OpenAI accepts")

	mime, data, err := decodePart(ai.NewMediaPart("", "data:audio/ogg;codecs=opus;base64,"+base64.StdEncoding.EncodeToString([]byte("x"))))
	require.NoError(t, err)
	assert.Equal(t, "audio/ogg", mime)
	assert.Equal(t, []byte("x"), data)
	_, _, err = decodePart(ai.NewMediaPart("", "data:audio/wav,notbase64"))
	assert.Error(t, err)
}

// wav builds a 16-bit mono WAV header over dataBytes zero bytes.
func wav(rate, dataBytes int) []byte {
	b := make([]byte, 44+dataBytes)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+dataBytes))
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1) // PCM
	binary.LittleEndian.PutUint16(b[22:], 1) // mono
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(dataBytes))
	return b
}
