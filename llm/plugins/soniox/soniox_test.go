package soniox

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
	g = genkit.Init(context.Background(), genkit.WithPlugins(&Soniox{APIKey: "sx-test", BaseURL: srv.URL, HTTPClient: srv.Client(), PollInterval: 5 * time.Millisecond}))
	return g
}

func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	handler.Store(&h)
}

// fakeAPI plays Soniox: an upload, a transcription that completes on its
// second status read, a transcript, and the two deletes.
type fakeAPI struct {
	mu       sync.Mutex
	calls    []string
	file     []byte
	create   map[string]any
	polls    int
	failWith string // an error status instead of completed
}

func (f *fakeAPI) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		assert.Equal(t, "Bearer sx-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/files":
			require.NoError(t, r.ParseMultipartForm(1<<20))
			fh, _, err := r.FormFile("file")
			require.NoError(t, err)
			f.file, _ = io.ReadAll(fh)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"file-1","filename":"audio.wav","size":20}`))
		case "POST /v1/transcriptions":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &f.create)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"tr-1","status":"queued","model":"stt-async-v6"}`))
		case "GET /v1/transcriptions/tr-1":
			f.polls++
			switch {
			case f.polls < 2:
				_, _ = w.Write([]byte(`{"id":"tr-1","status":"processing","audio_duration_ms":2500}`))
			case f.failWith != "":
				_, _ = w.Write([]byte(`{"id":"tr-1","status":"error","error_type":"` + f.failWith + `","error_message":"the audio is empty"}`))
			default:
				_, _ = w.Write([]byte(`{"id":"tr-1","status":"completed","audio_duration_ms":2500}`))
			}
		case "GET /v1/transcriptions/tr-1/transcript":
			_, _ = w.Write([]byte(`{"id":"tr-1","text":"Hello, I need a refund.","tokens":[{"text":"Hello,","confidence":0.9},{"text":" I need a refund.","confidence":1.0}]}`))
		case "DELETE /v1/transcriptions/tr-1", "DELETE /v1/files/file-1":
			w.WriteHeader(http.StatusNoContent)
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
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("soniox/stt-async-v6"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))),
		ai.WithConfig(Config{Language: "en-GB", Context: "returns at a shoe shop"}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", resp.Text())
	assert.Equal(t, 2.5, resp.Usage.Custom["audio_seconds"])
	assert.InDelta(t, 0.95, resp.Usage.Custom["confidence"], 0.001, "the mean token confidence")
	assert.Equal(t, 1, resp.Usage.InputAudioFiles)

	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, audio, api.file, "the audio is uploaded as a file")
	assert.Equal(t, map[string]any{"file_id": "file-1", "model": "stt-async-v6", "language_hints": []any{"en"}, "context": "returns at a shoe shop"}, api.create)
	assert.Equal(t, []string{
		"POST /v1/files", "POST /v1/transcriptions",
		"GET /v1/transcriptions/tr-1", "GET /v1/transcriptions/tr-1",
		"GET /v1/transcriptions/tr-1/transcript",
		"DELETE /v1/transcriptions/tr-1", "DELETE /v1/files/file-1",
	}, api.calls, "upload, create, poll until completed, read, clean up")
}

func TestTranscriptionError(t *testing.T) {
	g := setup(t)
	api := &fakeAPI{failWith: "invalid_audio"}
	serve(t, api.handle(t))
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("soniox/stt-async-v6"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_audio")
	assert.Contains(t, err.Error(), "the audio is empty")
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Contains(t, api.calls, "DELETE /v1/files/file-1", "the file is deleted even when the transcription fails")
	assert.Contains(t, api.calls, "DELETE /v1/transcriptions/tr-1")
}

func TestHTTPErrorsAndInput(t *testing.T) {
	g := setup(t)
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Invalid API key"}`, http.StatusUnauthorized)
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("soniox/stt-async-v5"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "POST /v1/files")
	assert.Contains(t, err.Error(), "401")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("soniox/stt-async-v6"), ai.WithPrompt("no audio"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no audio media part")
}

func TestNoKey(t *testing.T) {
	s := &Soniox{HTTPClient: http.DefaultClient}
	t.Setenv("SONIOX_API_KEY", "")
	assert.Len(t, s.Init(context.Background()), len(DefaultModels))
	_, err := s.transcribe(context.Background(), "stt-async-v6", &ai.ModelRequest{}, Config{})
	assert.ErrorContains(t, err, "no API key")
}
