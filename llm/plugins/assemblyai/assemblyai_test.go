package assemblyai

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xavidop/tamesu/llm/plugins/speechstream"
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

// Streaming.

// fakeStream plays Universal-Streaming v3: Begin on connect, a partial Turn
// after the first audio frame, the final Turns and Termination after
// Terminate, then it waits for the client to close.
type fakeStream struct {
	errorAfterAudio string // send {"error":...} after the first audio frame
	dropAfterAudio  bool   // cut the connection after the first audio frame
	hangOnTerminate bool   // never answer Terminate

	mu          sync.Mutex
	path, query string
	auth        string
	frames      []int // byte length of each binary frame
	audio       []byte
	controls    []map[string]any
	clientClose websocket.StatusCode // how the client's side ended, -1 for no close frame
	done        chan struct{}
}

func (f *fakeStream) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer close(f.done)
		f.mu.Lock()
		f.path, f.query, f.auth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		f.mu.Unlock()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx := r.Context()
		write := func(s string) { _ = conn.Write(ctx, websocket.MessageText, []byte(s)) }
		write(`{"type":"Begin","id":"sess-1","expires_at":1762400000}`)
		frames := 0
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				f.mu.Lock()
				f.clientClose = websocket.CloseStatus(err)
				f.mu.Unlock()
				return
			}
			if typ == websocket.MessageBinary {
				f.mu.Lock()
				f.frames = append(f.frames, len(data))
				f.audio = append(f.audio, data...)
				f.mu.Unlock()
				frames++
				if frames != 1 {
					continue
				}
				switch {
				case f.errorAfterAudio != "":
					write(`{"error":"` + f.errorAfterAudio + `"}`)
				case f.dropAfterAudio:
					return
				default:
					write(`{"type":"Turn","turn_order":0,"turn_is_formatted":false,"end_of_turn":false,"transcript":"hello","words":[{"text":"hello","confidence":0.3,"word_is_final":false}]}`)
				}
				continue
			}
			var ctl map[string]any
			_ = json.Unmarshal(data, &ctl)
			f.mu.Lock()
			f.controls = append(f.controls, ctl)
			f.mu.Unlock()
			if ctl["type"] != "Terminate" || f.hangOnTerminate {
				continue
			}
			write(`{"type":"Turn","turn_order":0,"turn_is_formatted":false,"end_of_turn":true,"transcript":"hello there","end_of_turn_confidence":0.8,"words":[{"text":"hello","confidence":0.3,"word_is_final":true},{"text":"there","confidence":0.4,"word_is_final":true}]}`)
			write(`{"type":"Turn","turn_order":0,"turn_is_formatted":true,"end_of_turn":true,"transcript":"Hello there.","end_of_turn_confidence":0.8,"words":[{"text":"Hello","confidence":0.9,"word_is_final":true},{"text":"there.","confidence":1.0,"word_is_final":true}]}`)
			write(`{"type":"Turn","turn_order":1,"turn_is_formatted":false,"end_of_turn":true,"transcript":"i need a refund","end_of_turn_confidence":0.7,"words":[{"text":"i","confidence":0.5,"word_is_final":true}]}`)
			write(`{"type":"Turn","turn_order":1,"turn_is_formatted":true,"end_of_turn":true,"transcript":" I need a refund. ","end_of_turn_confidence":0.7,"words":[{"text":"I","confidence":0.8,"word_is_final":true},{"text":"need","confidence":0.9,"word_is_final":true},{"text":"a refund.","confidence":0.7,"word_is_final":true}]}`)
			write(`{"type":"Turn","turn_order":2,"turn_is_formatted":true,"end_of_turn":true,"transcript":"","end_of_turn_confidence":0.1,"words":[]}`)
			write(`{"type":"Termination","audio_duration_seconds":0.26,"session_duration_seconds":1.5}`)
		}
	}
}

// streamPlugin serves f over a WebSocket and returns an initialised plugin
// pointed at it; the plugin's HTTP client keeps its usual 60 s timeout, which
// the dial must not inherit.
func streamPlugin(t *testing.T, f *fakeStream) *AssemblyAI {
	t.Helper()
	f.done = make(chan struct{})
	f.clientClose = -1
	srv := httptest.NewServer(f.handle(t))
	t.Cleanup(srv.Close)
	a := &AssemblyAI{APIKey: "aai-test", WSBaseURL: "ws://" + srv.Listener.Addr().String(), HTTPClient: &http.Client{Timeout: 60 * time.Second, Transport: srv.Client().Transport}}
	a.Init(context.Background())
	return a
}

func waitServer(t *testing.T, f *fakeStream) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the server handler did not finish: the client left the socket open")
	}
}

// tone is 20 ms of 16 kHz samples.
func tone() []int16 {
	s := make([]int16, 320)
	for i := range s {
		s[i] = int16(i*101 - 16000)
	}
	return s
}

func TestOpenTranscription(t *testing.T) {
	f := &fakeStream{}
	a := streamPlugin(t, f)
	ctx := context.Background()
	tr, err := a.OpenTranscription(ctx, speechstream.Options{Model: "universal", Rate: 16000, Language: "en"})
	require.NoError(t, err)
	var want []byte
	for range 13 {
		require.NoError(t, tr.Write(ctx, tone()))
		for _, s := range tone() {
			want = binary.LittleEndian.AppendUint16(want, uint16(s))
		}
	}
	res, err := tr.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Hello there. I need a refund.", res.Text, "the formatted turns in order; the empty one is dropped")
	assert.InDelta(t, (0.9+1.0+0.8+0.9+0.7)/5, res.Confidence, 1e-9, "the mean of the kept turns' word confidences")
	assert.InDelta(t, 0.26, res.AudioSeconds, 1e-9, "13 writes of 20 ms")
	waitServer(t, f)

	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, "/v3/ws", f.path)
	assert.Equal(t, "encoding=pcm_s16le&format_turns=true&sample_rate=16000", f.query)
	assert.Equal(t, "aai-test", f.auth, "the raw key, no Bearer prefix")
	assert.Equal(t, []int{3200, 3200, 1920}, f.frames, "100 ms frames, the 60 ms remainder flushed at Close")
	assert.Equal(t, want, f.audio, "little-endian PCM, nothing lost")
	assert.Equal(t, []map[string]any{{"type": "Terminate"}}, f.controls)
	assert.Equal(t, websocket.StatusNormalClosure, f.clientClose, "the socket is closed after Close")
}

func TestWriteFrameBounds(t *testing.T) {
	f := &fakeStream{}
	a := streamPlugin(t, f)
	ctx := context.Background()
	tr, err := a.OpenTranscription(ctx, speechstream.Options{Model: "universal", Rate: 8000})
	require.NoError(t, err)
	require.NoError(t, tr.Write(ctx, make([]int16, 10000)), "1.25 s at once")
	require.NoError(t, tr.Write(ctx, make([]int16, 100)), "12.5 ms, below the smallest frame")
	res, err := tr.Close(ctx)
	require.NoError(t, err)
	assert.InDelta(t, 10100.0/8000, res.AudioSeconds, 1e-9)
	waitServer(t, f)
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, []int{16000, 4000, 200}, f.frames, "at most 1 s per frame, the rest when it reaches 100 ms, the remainder at Close")
}

func TestOpenTranscriptionUnsupported(t *testing.T) {
	f := &fakeStream{}
	a := streamPlugin(t, f)
	for _, m := range []string{"slam-1", "nano", "best", ""} {
		_, err := a.OpenTranscription(context.Background(), speechstream.Options{Model: m, Rate: 16000})
		assert.ErrorIs(t, err, speechstream.ErrUnsupported, m)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Empty(t, f.path, "nothing was dialled")
}

func TestOpenTranscriptionServerError(t *testing.T) {
	f := &fakeStream{errorAfterAudio: "Sample rate must be between 8000 and 48000"}
	a := streamPlugin(t, f)
	ctx := context.Background()
	tr, err := a.OpenTranscription(ctx, speechstream.Options{Model: "universal", Rate: 16000})
	require.NoError(t, err)
	for range 5 {
		_ = tr.Write(ctx, tone())
	}
	_, err = tr.Close(ctx)
	require.Error(t, err)
	assert.EqualError(t, err, "assemblyai: Sample rate must be between 8000 and 48000")
	waitServer(t, f)
}

func TestOpenTranscriptionAbruptClose(t *testing.T) {
	f := &fakeStream{dropAfterAudio: true}
	a := streamPlugin(t, f)
	ctx := context.Background()
	tr, err := a.OpenTranscription(ctx, speechstream.Options{Model: "universal", Rate: 16000})
	require.NoError(t, err)
	var werr error
	for range 100 {
		if werr = tr.Write(ctx, tone()); werr != nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	_, cerr := tr.Close(ctx)
	require.Error(t, cerr, "the socket died before Termination")
	assert.Contains(t, cerr.Error(), "assemblyai: ")
	if werr != nil {
		assert.Contains(t, werr.Error(), "assemblyai: ")
	}
	waitServer(t, f)
}

func TestOpenTranscriptionRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Authentication error, API token missing/invalid"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	a := &AssemblyAI{APIKey: "aai-test", WSBaseURL: "ws://" + srv.Listener.Addr().String(), HTTPClient: srv.Client()}
	a.Init(context.Background())
	_, err := a.OpenTranscription(context.Background(), speechstream.Options{Model: "universal", Rate: 16000})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "assemblyai: GET /v3/ws: 401")
	assert.Contains(t, err.Error(), "API token missing/invalid")

	a = &AssemblyAI{WSBaseURL: "ws://" + srv.Listener.Addr().String(), HTTPClient: srv.Client()}
	t.Setenv("ASSEMBLYAI_API_KEY", "")
	a.Init(context.Background())
	_, err = a.OpenTranscription(context.Background(), speechstream.Options{Model: "universal", Rate: 16000})
	assert.ErrorContains(t, err, "no API key")
	a.APIKey = "aai-test"
	_, err = a.OpenTranscription(context.Background(), speechstream.Options{Model: "universal"})
	assert.ErrorContains(t, err, "sample rate")
}

func TestCloseCancelled(t *testing.T) {
	f := &fakeStream{hangOnTerminate: true}
	a := streamPlugin(t, f)
	ctx := context.Background()
	tr, err := a.OpenTranscription(ctx, speechstream.Options{Model: "universal", Rate: 16000})
	require.NoError(t, err)
	require.NoError(t, tr.Write(ctx, tone()))
	cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := tr.Close(cctx)
	assert.Less(t, time.Since(start), 3*time.Second, "Close returns when its ctx ends")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "assemblyai: ")
	assert.InDelta(t, 0.02, res.AudioSeconds, 1e-9, "what was written is still reported")
	waitServer(t, f)
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, []int{640}, f.frames, "the 20 ms were flushed before Terminate")
	assert.Equal(t, []map[string]any{{"type": "Terminate"}}, f.controls)
}

func TestIsStreaming(t *testing.T) {
	assert.True(t, IsStreaming("universal"))
	for _, m := range []string{"slam-1", "nano", "best", "Universal", ""} {
		assert.False(t, IsStreaming(m), m)
	}
}
