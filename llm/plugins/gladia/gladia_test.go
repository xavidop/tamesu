package gladia

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
	g = genkit.Init(context.Background(), genkit.WithPlugins(&Gladia{APIKey: "gl-test", BaseURL: srv.URL, HTTPClient: srv.Client(), PollInterval: time.Millisecond}))
	return g
}

func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	handler.Store(&h)
}

// fakeAPI plays Gladia: an upload, a transcription that is done on its
// second status read, and the delete.
type fakeAPI struct {
	mu          sync.Mutex
	calls       []string
	upload      []byte
	uploadName  string
	create      map[string]any
	polls       int
	failWith    int  // an error status with this error_code instead of done
	noUtterance bool // a done result without utterances
	noDuration  bool // a done result without metadata.audio_duration
}

func (f *fakeAPI) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		assert.Equal(t, "gl-test", r.Header.Get("x-gladia-key"), "the key travels in x-gladia-key")
		assert.Empty(t, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v2/upload":
			require.NoError(t, r.ParseMultipartForm(1<<20))
			fh, hdr, err := r.FormFile("audio")
			require.NoError(t, err, "the multipart field is audio")
			f.upload, _ = io.ReadAll(fh)
			f.uploadName = hdr.Filename
			_, _ = w.Write([]byte(`{"audio_url":"https://api.gladia.io/file/abc123","audio_metadata":{"id":"abc123","filename":"audio.wav","extension":"wav","size":20,"audio_duration":2.5,"number_of_channels":1}}`))
		case "POST /v2/pre-recorded":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &f.create)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"tr-1","result_url":"https://api.gladia.io/v2/pre-recorded/tr-1"}`))
		case "GET /v2/pre-recorded/tr-1":
			f.polls++
			switch {
			case f.polls < 2:
				_, _ = w.Write([]byte(`{"id":"tr-1","status":"processing","error_code":null,"result":null}`))
			case f.failWith != 0:
				_, _ = w.Write([]byte(`{"id":"tr-1","status":"error","error_code":` + itoa(f.failWith) + `,"result":null}`))
			case f.noUtterance:
				_, _ = w.Write([]byte(`{"id":"tr-1","status":"done","error_code":null,"result":{"metadata":{"audio_duration":2.5},"transcription":{"full_transcript":"","utterances":[]}}}`))
			case f.noDuration:
				_, _ = w.Write([]byte(`{"id":"tr-1","status":"done","error_code":null,"result":{"metadata":{},"transcription":{"full_transcript":"Hello.","utterances":[{"text":"Hello.","start":0,"end":1,"confidence":0.8}]}}}`))
			default:
				_, _ = w.Write([]byte(`{"id":"tr-1","status":"done","error_code":null,"result":{"metadata":{"audio_duration":2.4},"transcription":{"full_transcript":" Hello, I need a refund. ","languages":["en"],"utterances":[{"text":"Hello,","start":0,"end":0.5,"confidence":0.9},{"text":"I need a refund.","start":0.6,"end":2.4,"confidence":1.0}]}}}`))
			}
		case "DELETE /v2/pre-recorded/tr-1":
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func TestTranscribe(t *testing.T) {
	g := setup(t)
	api := &fakeAPI{}
	serve(t, api.handle(t))
	audio := []byte("RIFF....WAVEfmt fake")
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("gladia/solaria-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))),
		ai.WithConfig(Config{Language: "en-GB", Diarize: true}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", resp.Text(), "the full transcript, trimmed")
	assert.Equal(t, 2.4, resp.Usage.Custom["audio_seconds"], "the result's metadata.audio_duration")
	assert.InDelta(t, 0.95, resp.Usage.Custom["confidence"], 0.001, "the mean utterance confidence")
	assert.Equal(t, 1, resp.Usage.InputAudioFiles)
	assert.Equal(t, len("Hello, I need a refund."), resp.Usage.OutputCharacters)

	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, audio, api.upload, "the audio is the multipart audio field")
	assert.Equal(t, "audio.wav", api.uploadName)
	assert.Equal(t, map[string]any{
		"audio_url":       "https://api.gladia.io/file/abc123",
		"diarization":     true,
		"language_config": map[string]any{"languages": []any{"en"}, "code_switching": false},
	}, api.create)
	assert.Equal(t, []string{
		"POST /v2/upload", "POST /v2/pre-recorded",
		"GET /v2/pre-recorded/tr-1", "GET /v2/pre-recorded/tr-1",
		"DELETE /v2/pre-recorded/tr-1",
	}, api.calls, "upload, create, poll until done, clean up")
}

func TestDetectLanguage(t *testing.T) {
	g := setup(t)
	api := &fakeAPI{}
	serve(t, api.handle(t))
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("gladia/solaria-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/mpeg", "data:audio/mpeg;base64,AAAA"))))
	require.NoError(t, err)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, map[string]any{
		"audio_url":   "https://api.gladia.io/file/abc123",
		"diarization": false,
	}, api.create, "no language means no language_config, so Gladia detects it")
	assert.Equal(t, "audio.mp3", api.uploadName, "the filename follows the media type")
}

func TestNoUtterancesAndDurationFallback(t *testing.T) {
	g := setup(t)
	api := &fakeAPI{noUtterance: true}
	serve(t, api.handle(t))
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("gladia/solaria-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.NoError(t, err)
	assert.Equal(t, "", resp.Text())
	assert.Equal(t, 2.5, resp.Usage.Custom["audio_seconds"])
	_, ok := resp.Usage.Custom["confidence"]
	assert.False(t, ok, "no utterances, no confidence")

	api = &fakeAPI{noDuration: true}
	serve(t, api.handle(t))
	resp, err = genkit.Generate(context.Background(), g, ai.WithModelName("gladia/solaria-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.NoError(t, err)
	assert.Equal(t, "Hello.", resp.Text())
	assert.Equal(t, 2.5, resp.Usage.Custom["audio_seconds"], "the upload's audio_duration when the result has none")
	assert.InDelta(t, 0.8, resp.Usage.Custom["confidence"], 0.001)
}

func TestTranscriptionError(t *testing.T) {
	g := setup(t)
	api := &fakeAPI{failWith: 422}
	serve(t, api.handle(t))
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("gladia/solaria-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transcription failed")
	assert.Contains(t, err.Error(), "422", "the error_code is reported")
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Contains(t, api.calls, "DELETE /v2/pre-recorded/tr-1", "the transcription is deleted even when it fails")
}

func TestHTTPErrorsAndInput(t *testing.T) {
	g := setup(t)
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Invalid API key","statusCode":401}`, http.StatusUnauthorized)
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("gladia/solaria-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "POST /v2/upload")
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "Invalid API key")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("gladia/solaria-1"), ai.WithPrompt("no audio"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no audio media part")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("gladia/solaria-1"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "https://example.com/call.wav"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data URL")
}

func TestNoKey(t *testing.T) {
	p := &Gladia{HTTPClient: http.DefaultClient}
	t.Setenv("GLADIA_API_KEY", "")
	assert.Len(t, p.Init(context.Background()), len(DefaultModels))
	assert.Equal(t, DefaultPollInterval, p.PollInterval)
	_, err := p.transcribe(context.Background(), &ai.ModelRequest{}, Config{})
	assert.ErrorContains(t, err, "no API key")
}

func TestLanguageCode(t *testing.T) {
	for in, want := range map[string]string{
		"en-GB": "en",
		"en-US": "en",
		"es-ES": "es",
		"pt-BR": "pt",
		"en":    "en",
		"fr":    "fr",
		"DE":    "de",
		" it ":  "it",
		"":      "",
	} {
		assert.Equal(t, want, languageCode(in), in)
	}
}
