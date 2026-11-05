package speechmatics

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
	g = genkit.Init(context.Background(), genkit.WithPlugins(&Speechmatics{APIKey: "sm-test", BaseURL: srv.URL, HTTPClient: srv.Client(), PollInterval: time.Millisecond}))
	return g
}

func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	handler.Store(&h)
}

// fakeAPI plays Speechmatics: a job that is done on its second status read,
// a json-v2 transcript, and the delete.
type fakeAPI struct {
	mu         sync.Mutex
	calls      []string
	audio      []byte
	filename   string
	config     map[string]any
	format     string
	polls      int
	rejectWith string // a rejected job instead of done
}

func (f *fakeAPI) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		assert.Equal(t, "Bearer sm-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v2/jobs":
			require.NoError(t, r.ParseMultipartForm(1<<20))
			fh, hdr, err := r.FormFile("data_file")
			require.NoError(t, err)
			f.audio, _ = io.ReadAll(fh)
			f.filename = hdr.Filename
			f.config = nil
			_ = json.Unmarshal([]byte(r.FormValue("config")), &f.config)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"job-1"}`))
		case "GET /v2/jobs/job-1":
			f.polls++
			switch {
			case f.polls < 2:
				_, _ = w.Write([]byte(`{"job":{"id":"job-1","status":"running","created_at":"2026-11-05T10:00:00Z","data_name":"audio.wav"}}`))
			case f.rejectWith != "":
				_, _ = w.Write([]byte(`{"job":{"id":"job-1","status":"rejected","errors":[{"message":"` + f.rejectWith + `","timestamp":"2026-11-05T10:00:01Z"}]}}`))
			default:
				_, _ = w.Write([]byte(`{"job":{"id":"job-1","status":"done","duration":2.5,"data_name":"audio.wav"}}`))
			}
		case "GET /v2/jobs/job-1/transcript":
			f.format = r.URL.Query().Get("format")
			_, _ = w.Write([]byte(`{"format":"2.9","job":{"id":"job-1","duration":2.5},"results":[
				{"type":"word","start_time":0.1,"end_time":0.5,"alternatives":[{"content":"Hello","confidence":0.9,"language":"en"}]},
				{"type":"punctuation","start_time":0.5,"end_time":0.5,"alternatives":[{"content":",","confidence":1.0}]},
				{"type":"word","start_time":0.6,"end_time":0.7,"alternatives":[{"content":"I","confidence":1.0}]},
				{"type":"word","start_time":0.7,"end_time":0.9,"alternatives":[{"content":"need","confidence":1.0}]},
				{"type":"word","start_time":0.9,"end_time":1.0,"alternatives":[{"content":"a","confidence":1.0}]},
				{"type":"word","start_time":1.0,"end_time":1.4,"alternatives":[{"content":"refund","confidence":0.9}]},
				{"type":"punctuation","start_time":1.4,"end_time":1.4,"alternatives":[{"content":".","confidence":1.0}]}
			]}`))
		case "DELETE /v2/jobs/job-1":
			_, _ = w.Write([]byte(`{"id":"job-1","status":"deleted"}`))
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
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("speechmatics/enhanced"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(audio)))),
		ai.WithConfig(Config{Language: "en-GB", Diarize: true}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", resp.Text())
	assert.Equal(t, 2.5, resp.Usage.Custom["audio_seconds"])
	assert.InDelta(t, 0.96, resp.Usage.Custom["confidence"], 0.0001, "the mean word confidence, punctuation excluded")
	assert.Equal(t, 1, resp.Usage.InputAudioFiles)
	assert.Equal(t, len("Hello, I need a refund."), resp.Usage.OutputCharacters)

	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, audio, api.audio, "the audio is the data_file part")
	assert.Equal(t, "audio.wav", api.filename)
	assert.Equal(t, map[string]any{
		"type": "transcription",
		"transcription_config": map[string]any{
			"language":        "en",
			"operating_point": "enhanced",
			"output_locale":   "en-GB",
			"diarization":     "speaker",
		},
	}, api.config)
	assert.Equal(t, "json-v2", api.format)
	assert.Equal(t, []string{
		"POST /v2/jobs",
		"GET /v2/jobs/job-1", "GET /v2/jobs/job-1",
		"GET /v2/jobs/job-1/transcript",
		"DELETE /v2/jobs/job-1",
	}, api.calls, "create, poll until done, read, clean up")
}

func TestDefaultConfig(t *testing.T) {
	g := setup(t)
	api := &fakeAPI{}
	serve(t, api.handle(t))
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("speechmatics/standard"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/mpeg", "data:audio/mpeg;base64,AAAA"))))
	require.NoError(t, err)
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Equal(t, "audio.mp3", api.filename)
	assert.Equal(t, map[string]any{
		"type": "transcription",
		"transcription_config": map[string]any{
			"language":        "en",
			"operating_point": "standard",
		},
	}, api.config, "no language means en; neither output_locale nor diarization is sent")
}

func TestRejectedJob(t *testing.T) {
	g := setup(t)
	api := &fakeAPI{rejectWith: "File is not a supported audio format"}
	serve(t, api.handle(t))
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("speechmatics/enhanced"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "job rejected")
	assert.Contains(t, err.Error(), "File is not a supported audio format")
	api.mu.Lock()
	defer api.mu.Unlock()
	assert.Contains(t, api.calls, "DELETE /v2/jobs/job-1", "the job is deleted even when it is rejected")
	assert.NotContains(t, api.calls, "GET /v2/jobs/job-1/transcript")
}

func TestHTTPErrorsAndInput(t *testing.T) {
	g := setup(t)
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":401,"error":"Permission Denied","detail":"Invalid API key"}`, http.StatusUnauthorized)
	})
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("speechmatics/standard"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,AAAA"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "POST /v2/jobs")
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "Invalid API key")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("speechmatics/enhanced"), ai.WithPrompt("no audio"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no audio media part")

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("speechmatics/enhanced"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "https://example.com/call.wav"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data URL")
}

func TestNoKey(t *testing.T) {
	s := &Speechmatics{HTTPClient: http.DefaultClient}
	t.Setenv("SPEECHMATICS_API_KEY", "")
	assert.Len(t, s.Init(context.Background()), len(DefaultModels))
	assert.Equal(t, DefaultPollInterval, s.PollInterval)
	_, err := s.transcribe(context.Background(), "enhanced", &ai.ModelRequest{}, Config{})
	assert.ErrorContains(t, err, "no API key")
}

func TestLanguageCode(t *testing.T) {
	for in, want := range map[string][2]string{
		"":      {"en", ""},
		"en":    {"en", ""},
		"EN":    {"en", ""},
		"en-GB": {"en", "en-GB"},
		"en-gb": {"en", "en-GB"},
		"en-US": {"en", "en-US"},
		"en-AU": {"en", "en-AU"},
		"es-ES": {"es", ""},
		"pt-BR": {"pt", ""},
		"fr":    {"fr", ""},
		"cmn":   {"cmn", ""},
		"auto":  {"auto", ""},
	} {
		code, locale := languageCode(in)
		assert.Equal(t, want, [2]string{code, locale}, in)
	}
}

func TestAssemble(t *testing.T) {
	word := func(content string, confidence, end float64) result {
		r := result{Type: "word", EndTime: end}
		r.Alternatives = append(r.Alternatives, struct {
			Content    string  `json:"content"`
			Confidence float64 `json:"confidence"`
		}{content, confidence})
		return r
	}
	punct := func(content string) result {
		r := word(content, 1, 0)
		r.Type = "punctuation"
		return r
	}
	text, confidence, end := assemble([]result{
		word("Hello", 0.8, 0.5), punct(","), word("world", 1.0, 1.2), punct("!"),
		{Type: "word"}, // no alternatives: skipped
	})
	assert.Equal(t, "Hello, world!", text)
	assert.InDelta(t, 0.9, confidence, 0.0001, "punctuation does not count")
	assert.Equal(t, 1.2, end)

	text, confidence, end = assemble(nil)
	assert.Equal(t, "", text)
	assert.Equal(t, 0.0, confidence)
	assert.Equal(t, 0.0, end)

	text, _, _ = assemble([]result{punct("."), word("Yes", 1, 0.3)})
	assert.Equal(t, ". Yes", text, "a leading punctuation mark stands alone")
}
