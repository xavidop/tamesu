package soniox

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	g = genkit.Init(context.Background(), genkit.WithPlugins(&Soniox{
		APIKey: "sx-test", BaseURL: srv.URL, WSBaseURL: wsURL(srv), HTTPClient: srv.Client(), PollInterval: 5 * time.Millisecond,
	}))
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

// wsURL is the ws:// address of an httptest server.
func wsURL(srv *httptest.Server) string { return "ws://" + strings.TrimPrefix(srv.URL, "http://") }

// fakeRT plays the real-time endpoint: it reads the config, counts the audio
// frames, answers a non-final token while the audio flows, and after the
// empty end frame sends two messages of final tokens, the last one finished,
// then closes. mode changes the script: "error" sends an error right after the
// config, "drop" hangs up after it, "hang" never finishes.
type fakeRT struct {
	mode string

	mu         sync.Mutex
	path       string
	auth       string
	config     map[string]any
	audioBytes int
	ended      bool
	served     chan struct{} // closed when the handler returns
}

func newFakeRT(mode string) *fakeRT { return &fakeRT{mode: mode, served: make(chan struct{})} }

func (f *fakeRT) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer close(f.served)
		f.mu.Lock()
		f.path = r.URL.RequestURI()
		f.auth = r.Header.Get("Authorization")
		f.mu.Unlock()
		c, err := websocket.Accept(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		typ, data, err := c.Read(ctx)
		if !assert.NoError(t, err) || !assert.Equal(t, websocket.MessageText, typ, "the config is a text frame") {
			return
		}
		var cfg map[string]any
		assert.NoError(t, json.Unmarshal(data, &cfg))
		f.mu.Lock()
		f.config = cfg
		f.mu.Unlock()
		say := func(msg string) bool { return assert.NoError(t, c.Write(ctx, websocket.MessageText, []byte(msg))) }
		switch f.mode {
		case "error":
			say(`{"error_code":401,"error_message":"Invalid API key"}`)
			return
		case "drop":
			return
		}
		partial := false
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				f.mu.Lock()
				f.audioBytes += len(data)
				f.mu.Unlock()
				if !partial {
					partial = true
					say(`{"tokens":[{"text":"Hel","start_ms":0,"end_ms":120,"confidence":0.3,"is_final":false}],"final_audio_proc_ms":0,"total_audio_proc_ms":120}`)
				}
				continue
			}
			if len(data) == 0 {
				f.mu.Lock()
				f.ended = true
				f.mu.Unlock()
				break
			}
		}
		if f.mode == "hang" {
			for {
				if _, _, err := c.Read(ctx); err != nil {
					return
				}
			}
		}
		say(`{"tokens":[{"text":"Hello,","start_ms":0,"end_ms":400,"confidence":0.9,"is_final":true},{"text":" I","start_ms":400,"end_ms":500,"confidence":0.5,"is_final":false}],"final_audio_proc_ms":400,"total_audio_proc_ms":500}`)
		say(`{"tokens":[{"text":" I need","start_ms":400,"end_ms":900,"confidence":0.8,"is_final":true},{"text":" a refund.","start_ms":900,"end_ms":1500,"confidence":1.0,"is_final":true}],"final_audio_proc_ms":1500,"total_audio_proc_ms":1500,"finished":true}`)
		_ = c.Close(websocket.StatusNormalClosure, "")
	}
}

// openStream starts a fake real-time server and a plugin pointed at it.
func openStream(t *testing.T, mode string) (*Soniox, *fakeRT) {
	t.Helper()
	rt := newFakeRT(mode)
	srv := httptest.NewServer(rt.handle(t))
	t.Cleanup(srv.Close)
	s := &Soniox{APIKey: "sx-test", WSBaseURL: wsURL(srv), HTTPClient: &http.Client{Timeout: 60 * time.Second}}
	return s, rt
}

// waitServed fails unless the server handler returned, which it does once the
// client's socket is closed.
func waitServed(t *testing.T, rt *fakeRT) {
	t.Helper()
	select {
	case <-rt.served:
	case <-time.After(5 * time.Second):
		t.Fatal("the server handler did not return: the socket was not closed")
	}
}

func tone(n int) []int16 {
	samples := make([]int16, n)
	for i := range samples {
		samples[i] = int16(i*37 - 4000)
	}
	return samples
}

func TestIsStreaming(t *testing.T) {
	assert.True(t, IsStreaming("stt-rt-v3"))
	assert.True(t, IsStreaming("stt-rt-preview"))
	assert.False(t, IsStreaming("stt-async-v6"))
	assert.False(t, IsStreaming("stt-async-v5"))
	assert.False(t, IsStreaming(""))
}

func TestOpenTranscriptionUnsupported(t *testing.T) {
	s := &Soniox{APIKey: "sx-test", WSBaseURL: "ws://127.0.0.1:1"}
	_, err := s.OpenTranscription(context.Background(), speechstream.Options{Model: "stt-async-v6", Rate: 16000})
	assert.ErrorIs(t, err, speechstream.ErrUnsupported, "an async model has no streaming path, before any dial")

	_, err = (&Soniox{WSBaseURL: "ws://127.0.0.1:1"}).OpenTranscription(context.Background(), speechstream.Options{Model: "stt-rt-v3", Rate: 16000})
	assert.ErrorContains(t, err, "no API key")
	_, err = s.OpenTranscription(context.Background(), speechstream.Options{Model: "stt-rt-v3"})
	assert.ErrorContains(t, err, "sample rate")
}

func TestStream(t *testing.T) {
	s, rt := openStream(t, "")
	ctx := context.Background()
	tr, err := s.OpenTranscription(ctx, speechstream.Options{Model: "stt-rt-v3", Rate: 16000, Language: "en-GB"})
	require.NoError(t, err)
	written := 0
	for _, n := range []int{1600, 3200, 1600, 0, 800} {
		require.NoError(t, tr.Write(ctx, tone(n)))
		written += n
	}
	result, err := tr.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", result.Text, "final tokens joined without separators, non-final ones dropped")
	assert.InDelta(t, 0.9, result.Confidence, 1e-9, "the mean of the final tokens' confidence")
	assert.InDelta(t, float64(written)/16000, result.AudioSeconds, 1e-9)

	waitServed(t, rt)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	assert.Equal(t, "/transcribe-websocket", rt.path)
	assert.Empty(t, rt.auth, "the key travels in the config, not a header")
	assert.Equal(t, map[string]any{
		"api_key": "sx-test", "model": "stt-rt-v3", "audio_format": "pcm_s16le",
		"sample_rate": float64(16000), "num_channels": float64(1), "language_hints": []any{"en"},
	}, rt.config)
	assert.Equal(t, written*2, rt.audioBytes, "every sample arrives as two bytes")
	assert.True(t, rt.ended, "the empty text frame ends the audio")
	assert.Error(t, tr.Write(ctx, tone(10)), "the stream is closed")
	_, err = tr.Close(ctx)
	assert.NoError(t, err, "a second Close is harmless")
}

func TestStreamNoLanguage(t *testing.T) {
	s, rt := openStream(t, "")
	ctx := context.Background()
	tr, err := s.OpenTranscription(ctx, speechstream.Options{Model: "stt-rt-v3", Rate: 8000})
	require.NoError(t, err)
	_, err = tr.Close(ctx)
	require.NoError(t, err)
	waitServed(t, rt)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	assert.NotContains(t, rt.config, "language_hints", "no hint when the language is empty")
	assert.Equal(t, float64(8000), rt.config["sample_rate"])
}

func TestStreamServerError(t *testing.T) {
	s, rt := openStream(t, "error")
	ctx := context.Background()
	tr, err := s.OpenTranscription(ctx, speechstream.Options{Model: "stt-rt-v3", Rate: 16000})
	require.NoError(t, err)
	waitServed(t, rt) // the error has been sent and the server has hung up
	result, err := tr.Close(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "soniox: Invalid API key")
	assert.Equal(t, "", result.Text)
	assert.Error(t, tr.Write(ctx, tone(10)), "a dead stream reports its error")
}

func TestStreamDropped(t *testing.T) {
	s, rt := openStream(t, "drop")
	ctx := context.Background()
	tr, err := s.OpenTranscription(ctx, speechstream.Options{Model: "stt-rt-v3", Rate: 16000})
	require.NoError(t, err)
	waitServed(t, rt)
	if err := tr.Write(ctx, tone(1600)); err != nil {
		assert.Contains(t, err.Error(), "soniox:")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := tr.Close(ctx)
		assert.Error(t, err, "the socket closed before finished")
		if err != nil {
			assert.Contains(t, err.Error(), "soniox:")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the server hung up")
	}
}

func TestStreamCloseCancelled(t *testing.T) {
	s, rt := openStream(t, "hang")
	ctx := context.Background()
	tr, err := s.OpenTranscription(ctx, speechstream.Options{Model: "stt-rt-v3", Rate: 16000})
	require.NoError(t, err)
	require.NoError(t, tr.Write(ctx, tone(1600)))
	closeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := tr.Close(closeCtx)
	assert.Less(t, time.Since(start), 3*time.Second, "Close returns promptly when its ctx ends")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), err.Error())
	assert.Contains(t, err.Error(), "soniox:")
	assert.InDelta(t, 0.1, result.AudioSeconds, 1e-9, "the audio written is still counted")
	waitServed(t, rt)
}

// wavBytes builds a 16-bit mono WAV.
func wavBytes(rate int, samples []int16) []byte {
	pcm := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	var b []byte
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(36+len(pcm)))
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1) // PCM
	b = binary.LittleEndian.AppendUint16(b, 1) // mono
	b = binary.LittleEndian.AppendUint32(b, uint32(rate))
	b = binary.LittleEndian.AppendUint32(b, uint32(rate*2))
	b = binary.LittleEndian.AppendUint16(b, 2)
	b = binary.LittleEndian.AppendUint16(b, 16)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(pcm)))
	return append(b, pcm...)
}

func TestGenerateRealtime(t *testing.T) {
	g := setup(t)
	rt := newFakeRT("")
	serve(t, rt.handle(t))
	samples := tone(24000) // 1.5 s at 16 kHz, so more than one frame
	wav := wavBytes(16000, samples)
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("soniox/stt-rt-v3"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(wav)))),
		ai.WithConfig(Config{Language: "es-ES"}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", resp.Text())
	assert.InDelta(t, 1.5, resp.Usage.Custom["audio_seconds"], 1e-9)
	assert.InDelta(t, 0.9, resp.Usage.Custom["confidence"], 1e-9)
	assert.Equal(t, 1, resp.Usage.InputAudioFiles)
	waitServed(t, rt)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	assert.Equal(t, "/transcribe-websocket", rt.path)
	assert.Equal(t, len(wav)-44, rt.audioBytes, "the WAV header is stripped, the samples all sent")
	assert.Equal(t, float64(16000), rt.config["sample_rate"], "the rate comes from the WAV header")
	assert.Equal(t, []any{"es"}, rt.config["language_hints"])
	assert.True(t, rt.ended)
}

func TestGenerateRealtimeL16(t *testing.T) {
	g := setup(t)
	rt := newFakeRT("")
	serve(t, rt.handle(t))
	pcm := wavBytes(8000, tone(800))[44:]
	resp, err := genkit.Generate(context.Background(), g, ai.WithModelName("soniox/stt-rt-v3"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/L16;rate=8000", base64.StdEncoding.EncodeToString(pcm)))),
		ai.WithConfig(Config{LanguageHints: []string{"fr", "en"}}))
	require.NoError(t, err)
	assert.Equal(t, "Hello, I need a refund.", resp.Text())
	assert.InDelta(t, 0.1, resp.Usage.Custom["audio_seconds"], 1e-9)
	waitServed(t, rt)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	assert.Equal(t, float64(8000), rt.config["sample_rate"], "the rate comes from the content type")
	assert.Equal(t, []any{"fr"}, rt.config["language_hints"], "the first hint stands in for the language")
	assert.Equal(t, len(pcm), rt.audioBytes)

	_, err = genkit.Generate(context.Background(), g, ai.WithModelName("soniox/stt-rt-v3"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/mpeg", "data:audio/mpeg;base64,AAAA"))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audio/wav or audio/L16", "a compressed recording has no streaming path")
}

func TestGenerateRealtimeError(t *testing.T) {
	g := setup(t)
	rt := newFakeRT("error")
	serve(t, rt.handle(t))
	_, err := genkit.Generate(context.Background(), g, ai.WithModelName("soniox/stt-rt-v3"),
		ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(wavBytes(16000, tone(1600)))))))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "soniox: Invalid API key")
	waitServed(t, rt)
}

func TestWAVPCM(t *testing.T) {
	wav := wavBytes(22050, tone(100))
	pcm, rate, err := wavPCM(wav)
	require.NoError(t, err)
	assert.Equal(t, 22050, rate)
	assert.Equal(t, wav[44:], pcm)

	_, _, err = wavPCM([]byte("ID3 not a wav"))
	assert.ErrorContains(t, err, "audio/wav or audio/L16")

	stereo := wavBytes(16000, tone(10))
	binary.LittleEndian.PutUint16(stereo[22:], 2)
	_, _, err = wavPCM(stereo)
	assert.ErrorContains(t, err, "16-bit mono")

	_, _, err = wavPCM(wav[:36])
	assert.ErrorContains(t, err, "no data chunk", "the fmt chunk alone")
	_, _, err = wavPCM(wav[:30])
	assert.ErrorContains(t, err, "truncated WAV header")

	assert.Equal(t, 16000, pcmRate("audio/L16;rate=16000"))
	assert.Equal(t, 8000, pcmRate("Audio/L16; rate=8000; channels=1"))
	assert.Equal(t, 0, pcmRate("audio/wav"))
}
