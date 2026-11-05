package assemblyai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	g = genkit.Init(context.Background(), genkit.WithPlugins(&AssemblyAI{APIKey: "aai-test", BaseURL: srv.URL, HTTPClient: srv.Client(), PollInterval: time.Millisecond}))
	return g
}

func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	handler.Store(&h)
}

// fakeAPI plays AssemblyAI: an upload, a transcript that completes on its
// second status read, and the delete.
type fakeAPI struct {
	mu         sync.Mutex
	calls      []string
	upload     []byte
	uploadType string
	create     map[string]any
	polls      int
	failWith   string // an error status instead of completed
}

func (f *fakeAPI) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		assert.Equal(t, "aai-test", r.Header.Get("Authorization"), "the raw key, no Bearer prefix")
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v2/upload":
			f.upload, _ = io.ReadAll(r.Body)
			f.uploadType = r.Header.Get("Content-Type")
			_, _ = w.Write([]byte(`{"upload_url":"https://cdn.assemblyai.com/upload/abc123"}`))
		case "POST /v2/transcript":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &f.create)
			_, _ = w.Write([]byte(`{"id":"tr-1","status":"queued","speech_model":"universal"}`))
		case "GET /v2/transcript/tr-1":
			f.polls++
			switch {
			case f.polls < 2:
				_, _ = w.Write([]byte(`{"id":"tr-1","status":"processing","text":null}`))
			case f.failWith != "":
				_, _ = w.Write([]byte(`{"id":"tr-1","status":"error","error":"` + f.failWith + `"}`))
			default:
				_, _ = w.Write([]byte(`{"id":"tr-1","status":"completed","text":"Hello, I need a refund.","confidence":0.9512,"audio_duration":2.5,"language_code":"en_uk"}`))
			}
		case "DELETE /v2/transcript/tr-1":
			_, _ = w.Write([]byte(`{"id":"tr-1","status":"completed","text":"Deleted by user."}`))
		default:
			http.NotFound(w, r)
		}
	}
}

func TestTranscribe(t *testing.T) {
	g := setup(t)
	api := &fakeAPI{}
	serve(t, api.handle(t))
	audio := []byte("RIFF....WAVEfmt fake")
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("assemblyai/universal"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))),
		ai.WithConfig(Config{Language: "en-GB", Keyterms: []string{"refund", "Tamesu"}, SpeakerLabels: true}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", resp.Text())
	assert.Equal(t, 2.5, resp.Usage.Custom["audio_seconds"])
	assert.InDelta(t, 0.9512, resp.Usage.Custom["confidence"], 0.0001)
	assert.Equal(t, 1, resp.Usage.InputAudioFiles)
	assert.Equal(t, len("Hello, I need a refund."), resp.Usage.OutputCharacters)

	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, audio, api.upload, "the raw audio bytes are the upload body")
	assert.Equal(t, "application/octet-stream", api.uploadType)
	assert.Equal(t, map[string]any{
		"audio_url":       "https://cdn.assemblyai.com/upload/abc123",
		"speech_model":    "universal",
		"language_code":   "en_uk",
		"keyterms_prompt": []any{"refund", "Tamesu"},
		"speaker_labels":  true,
	}, api.create)
	assert.Equal(t, []string{
		"POST /v2/upload", "POST /v2/transcript",
		"GET /v2/transcript/tr-1", "GET /v2/transcript/tr-1",
		"DELETE /v2/transcript/tr-1",
	}, api.calls, "upload, create, poll until completed, clean up")
}

func TestLanguageDetection(t *testing.T) {
	g := setup(t)
	api := &fakeAPI{}
	serve(t, api.handle(t))
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("assemblyai/slam-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.NoError(t, err)
	api.mu.Lock()
	assert.Equal(t, map[string]any{
		"audio_url":          "https://cdn.assemblyai.com/upload/abc123",
		"speech_model":       "slam-1",
		"language_detection": true,
	}, api.create, "no language means detection")
	api.mu.Unlock()

	api = &fakeAPI{}
	serve(t, api.handle(t))
	off := false
	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("assemblyai/slam-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))),
		ai.WithConfig(Config{LanguageDetection: &off}))
	require.NoError(t, err)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, map[string]any{
		"audio_url":    "https://cdn.assemblyai.com/upload/abc123",
		"speech_model": "slam-1",
	}, api.create, "detection explicitly off sends neither field")
}

func TestTranscriptionError(t *testing.T) {
	g := setup(t)
	api := &fakeAPI{failWith: "Audio file is empty"}
	serve(t, api.handle(t))
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("assemblyai/universal"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transcription failed")
	assert.Contains(t, err.Error(), "Audio file is empty")
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Contains(t, api.calls, "DELETE /v2/transcript/tr-1", "the transcript is deleted even when it fails")
}

func TestHTTPErrorsAndInput(t *testing.T) {
	g := setup(t)
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Authentication error, API token missing/invalid"}`, http.StatusUnauthorized)
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("assemblyai/nano"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "POST /v2/upload")
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "API token missing/invalid")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("assemblyai/universal"), ai.WithPrompt("no audio"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no audio media part")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("assemblyai/universal"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "https://example.com/call.wav"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data URL")
}

func TestNoKey(t *testing.T) {
	a := &AssemblyAI{HTTPClient: http.DefaultClient}
	t.Setenv("ASSEMBLYAI_API_KEY", "")
	assert.Len(t, a.Init(context.Background()), len(DefaultModels))
	_, err := a.transcribe(context.Background(), "universal", &ai.ModelRequest{}, Config{})
	assert.ErrorContains(t, err, "no API key")
}

func TestLanguageCode(t *testing.T) {
	for in, want := range map[string]string{
		"en-GB": "en_uk",
		"en-US": "en_us",
		"en-AU": "en_au",
		"en-gb": "en_uk",
		"en-IN": "en",
		"es-ES": "es",
		"pt-BR": "pt",
		"en":    "en",
		"fr":    "fr",
		"DE":    "de",
		"":      "",
	} {
		assert.Equal(t, want, languageCode(in), in)
	}
}
