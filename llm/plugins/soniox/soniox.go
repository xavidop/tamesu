// Package soniox is a Genkit model plugin for Soniox speech to text: models
// named soniox/<model> (stt-async-v6 by default) take an audio media part in
// a user message and answer with the transcript as text. It is an ordinary
// Genkit plugin, usable in any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&soniox.Soniox{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("soniox/stt-async-v6"),
//	    ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", dataURL))))
//
// A request uploads the audio (POST /v1/files), creates a transcription
// (POST /v1/transcriptions), polls it until it completes, reads the
// transcript (GET /v1/transcriptions/{id}/transcript) and deletes both the
// transcription and the file. The response's duration and mean token
// confidence are reported in the usage's Custom map (audio_seconds,
// confidence).
//
// Soniox has separate real-time models: stt-rt-v3 is the real-time model,
// stt-async-v6 the async one. The real-time models (IsStreaming: every name
// starting with stt-rt) transcribe a stream through OpenTranscription, the
// speechstream.Transcriber contract: it connects to
// {WSBaseURL}/transcribe-websocket, sends the JSON config as the first text
// frame, the 16-bit PCM as binary frames, an empty text frame as the end of
// the audio, and keeps the final tokens the server sends until it says
// finished. An async model returns speechstream.ErrUnsupported there. The
// Genkit action of a real-time model works too: it decodes the request's WAV
// (or audio/L16;rate=N) and plays it through the same stream, so
// soniox/stt-rt-v3 also transcribes a recorded utterance.
package soniox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"

	"github.com/xavidop/tamesu/llm/plugins/speechstream"
)

// Provider is the plugin name and the prefix of its models.
const Provider = "soniox"

// DefaultBaseURL is Soniox's API.
const DefaultBaseURL = "https://api.soniox.com"

// DefaultWSBaseURL is Soniox's real-time API; OpenTranscription connects to
// its /transcribe-websocket.
const DefaultWSBaseURL = "wss://stt-rt.soniox.com"

// DefaultModels are defined when Models is empty: the async models and the
// real-time one.
var DefaultModels = []string{"stt-async-v6", "stt-async-v5", "stt-rt-v3"}

// DefaultPollInterval is how often a transcription's status is read.
const DefaultPollInterval = 400 * time.Millisecond

// Soniox is the plugin.
type Soniox struct {
	// APIKey authenticates; SONIOX_API_KEY when empty.
	APIKey string
	// BaseURL overrides the API endpoint (tests, proxies).
	BaseURL string
	// WSBaseURL overrides the real-time endpoint (DefaultWSBaseURL; ws:// +
	// host in tests).
	WSBaseURL string
	// HTTPClient overrides the client (default: 60 s timeout per call). A
	// stream dials with a copy whose Timeout is zero, keeping its Transport.
	HTTPClient *http.Client
	// Models to define, without the provider prefix.
	Models []string
	// PollInterval between status reads (DefaultPollInterval when zero).
	PollInterval time.Duration
}

// Config is the per-request configuration (ai.WithConfig).
type Config struct {
	// Language is a hint, an ISO 639-1 code such as en or es (a locale is
	// cut to its language); it joins LanguageHints.
	Language string `json:"language,omitempty"`
	// LanguageHints are the languages expected in the audio; none means
	// automatic detection.
	LanguageHints []string `json:"language_hints,omitempty"`
	// Context helps with domain terms: free text passed as the context.
	Context string `json:"context,omitempty"`
	// LanguageIdentification labels each token with its language.
	LanguageIdentification bool `json:"language_identification,omitempty"`
}

// Name implements api.Plugin.
func (s *Soniox) Name() string { return Provider }

// Init implements api.Plugin.
func (s *Soniox) Init(context.Context) []api.Action {
	if s.APIKey == "" {
		s.APIKey = os.Getenv("SONIOX_API_KEY")
	}
	if s.BaseURL == "" {
		s.BaseURL = DefaultBaseURL
	}
	if s.WSBaseURL == "" {
		s.WSBaseURL = DefaultWSBaseURL
	}
	if s.HTTPClient == nil {
		s.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if s.PollInterval <= 0 {
		s.PollInterval = DefaultPollInterval
	}
	models := s.Models
	if len(models) == 0 {
		models = DefaultModels
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, s.model(m))
	}
	return actions
}

func (s *Soniox) model(name string) api.Action {
	supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Soniox " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return s.transcribe(ctx, name, req, cfg)
		})
}

// transcription is the part of a transcription's status tamesu reads.
type transcription struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	AudioDurationMS int    `json:"audio_duration_ms"`
	ErrorType       string `json:"error_type"`
	ErrorMessage    string `json:"error_message"`
}

// transcript is the finished text with its tokens.
type transcript struct {
	Text   string `json:"text"`
	Tokens []struct {
		Text       string  `json:"text"`
		Confidence float64 `json:"confidence"`
	} `json:"tokens"`
}

func (s *Soniox) transcribe(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if s.APIKey == "" {
		return nil, errors.New("soniox: no API key (set SONIOX_API_KEY or Soniox.APIKey)")
	}
	mime, data, err := audioPart(req)
	if err != nil {
		return nil, err
	}
	if IsStreaming(model) {
		return s.transcribeStream(ctx, model, req, cfg, mime, data)
	}
	fileID, err := s.upload(ctx, mime, data)
	if err != nil {
		return nil, err
	}
	// The file and the transcription are deleted whatever happens; the
	// transcription first, since a processing one keeps its file.
	defer func() { _ = s.delete(context.WithoutCancel(ctx), "/v1/files/"+url.PathEscape(fileID)) }()
	body := map[string]any{"file_id": fileID, "model": model}
	hints := append([]string(nil), cfg.LanguageHints...)
	if lang, _, _ := strings.Cut(cfg.Language, "-"); lang != "" {
		hints = append(hints, strings.ToLower(lang))
	}
	if len(hints) > 0 {
		body["language_hints"] = hints
	}
	if cfg.Context != "" {
		body["context"] = cfg.Context
	}
	if cfg.LanguageIdentification {
		body["enable_language_identification"] = true
	}
	payload, _ := json.Marshal(body)
	var tr transcription
	if err := s.call(ctx, http.MethodPost, "/v1/transcriptions", "application/json", bytes.NewReader(payload), &tr); err != nil {
		return nil, err
	}
	if tr.ID == "" {
		return nil, errors.New("soniox: the transcription has no id")
	}
	defer func() { _ = s.delete(context.WithoutCancel(ctx), "/v1/transcriptions/"+url.PathEscape(tr.ID)) }()
	for tr.Status != "completed" {
		if tr.Status == "error" {
			return nil, fmt.Errorf("soniox: transcription failed: %s %s", tr.ErrorType, strings.TrimSpace(tr.ErrorMessage))
		}
		timer := time.NewTimer(s.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if err := s.call(ctx, http.MethodGet, "/v1/transcriptions/"+url.PathEscape(tr.ID), "", nil, &tr); err != nil {
			return nil, err
		}
	}
	var out transcript
	if err := s.call(ctx, http.MethodGet, "/v1/transcriptions/"+url.PathEscape(tr.ID)+"/transcript", "", nil, &out); err != nil {
		return nil, err
	}
	text := out.Text
	confidence := 0.0
	if len(out.Tokens) > 0 {
		var b strings.Builder
		for _, t := range out.Tokens {
			b.WriteString(t.Text)
			confidence += t.Confidence
		}
		confidence /= float64(len(out.Tokens))
		if text == "" {
			text = b.String()
		}
	}
	text = strings.TrimSpace(text)
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(text),
			Custom:           map[string]float64{"audio_seconds": float64(tr.AudioDurationMS) / 1000, "confidence": confidence},
		},
	}, nil
}

func (s *Soniox) upload(ctx context.Context, mime string, data []byte) (string, error) {
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	part, err := w.CreateFormFile("file", "audio"+extensionFor(mime))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := s.call(ctx, http.MethodPost, "/v1/files", w.FormDataContentType(), &form, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("soniox: the upload returned no file id")
	}
	return out.ID, nil
}

func (s *Soniox) delete(ctx context.Context, path string) error {
	return s.call(ctx, http.MethodDelete, path, "", nil, nil)
}

// call makes one API request and decodes a JSON response into out (nil for
// none).
func (s *Soniox) call(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := s.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("soniox: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("soniox: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("soniox: %s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("soniox: decoding the response of %s %s: %w", method, path, err)
	}
	return nil
}

func extensionFor(mime string) string {
	switch {
	case strings.Contains(mime, "wav"):
		return ".wav"
	case strings.Contains(mime, "mpeg") || strings.Contains(mime, "mp3"):
		return ".mp3"
	case strings.Contains(mime, "ogg"):
		return ".ogg"
	case strings.Contains(mime, "flac"):
		return ".flac"
	case strings.Contains(mime, "webm"):
		return ".webm"
	}
	return ".wav"
}

// audioPart finds the audio in the request: the last media part, as a data
// URL (data:<mime>;base64,<data>) or raw base64 with the part's content type.
func audioPart(req *ai.ModelRequest) (mime string, data []byte, err error) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		for _, p := range req.Messages[i].Content {
			if p.IsMedia() {
				return decodePart(p)
			}
		}
	}
	return "", nil, errors.New("soniox: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("soniox: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("soniox: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("soniox: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("soniox: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}

// IsStreaming says whether a model transcribes a stream: Soniox's real-time
// models are the ones named stt-rt-*; the async ones go through the files API.
func IsStreaming(model string) bool { return strings.HasPrefix(model, "stt-rt") }

var _ speechstream.Transcriber = (*Soniox)(nil)

// streamConfig is the first message of a real-time session.
type streamConfig struct {
	APIKey        string   `json:"api_key"`
	Model         string   `json:"model"`
	AudioFormat   string   `json:"audio_format"`
	SampleRate    int      `json:"sample_rate"`
	NumChannels   int      `json:"num_channels"`
	LanguageHints []string `json:"language_hints,omitempty"`
}

// streamMessage is what the real-time server sends: tokens, the finished
// flag on the last one, or an error.
type streamMessage struct {
	Tokens []struct {
		Text       string  `json:"text"`
		Confidence float64 `json:"confidence"`
		IsFinal    bool    `json:"is_final"`
	} `json:"tokens"`
	Finished     bool   `json:"finished"`
	ErrorCode    int    `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

// OpenTranscription implements speechstream.Transcriber: it connects to
// {WSBaseURL}/transcribe-websocket and sends the config (api_key, model,
// pcm_s16le, the rate, one channel, the language hint cut from opts.Language).
// An async model returns speechstream.ErrUnsupported before any network call.
func (s *Soniox) OpenTranscription(ctx context.Context, opts speechstream.Options) (speechstream.Transcription, error) {
	if !IsStreaming(opts.Model) {
		return nil, speechstream.ErrUnsupported
	}
	if s.APIKey == "" {
		return nil, errors.New("soniox: no API key (set SONIOX_API_KEY or Soniox.APIKey)")
	}
	if opts.Rate <= 0 {
		return nil, fmt.Errorf("soniox: invalid sample rate %d", opts.Rate)
	}
	cfg := streamConfig{APIKey: s.APIKey, Model: opts.Model, AudioFormat: "pcm_s16le", SampleRate: opts.Rate, NumChannels: 1}
	if lang, _, _ := strings.Cut(opts.Language, "-"); lang != "" {
		cfg.LanguageHints = []string{strings.ToLower(lang)}
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("soniox: %w", err)
	}
	base := s.WSBaseURL
	if base == "" {
		base = DefaultWSBaseURL
	}
	// An http.Client timeout would kill the long-lived socket; the dial
	// keeps the transport only.
	client := &http.Client{}
	if s.HTTPClient != nil {
		client.Transport = s.HTTPClient.Transport
	}
	conn, res, err := websocket.Dial(ctx, strings.TrimRight(base, "/")+"/transcribe-websocket", &websocket.DialOptions{HTTPClient: client})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("soniox: %w", err)
	}
	conn.SetReadLimit(8 << 20)
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		_ = conn.CloseNow()
		return nil, fmt.Errorf("soniox: %w", err)
	}
	t := &stream{conn: conn, rate: opts.Rate, done: make(chan struct{})}
	go t.read()
	return t, nil
}

// stream is one real-time session. Write and Close run on the caller's
// goroutine; read owns the socket reads and the transcript under mu.
type stream struct {
	conn    *websocket.Conn
	rate    int
	samples int64
	closed  bool
	done    chan struct{} // closed when read returns

	mu         sync.Mutex
	text       strings.Builder
	confidence float64 // sum over the final tokens
	finals     int
	finished   bool
	err        error
}

// read keeps the final tokens of every message until the server says
// finished, reports an error, or closes the socket.
func (t *stream) read() {
	defer close(t.done)
	for {
		typ, data, err := t.conn.Read(context.Background())
		if err != nil {
			t.fail(err)
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg streamMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.fail(fmt.Errorf("decoding a message: %w", err))
			return
		}
		if msg.ErrorMessage != "" || msg.ErrorCode != 0 {
			t.fail(fmt.Errorf("%s (error %d)", strings.TrimSpace(msg.ErrorMessage), msg.ErrorCode))
			return
		}
		t.mu.Lock()
		for _, tok := range msg.Tokens {
			if !tok.IsFinal {
				continue // replaced by a later final token
			}
			t.text.WriteString(tok.Text)
			t.confidence += tok.Confidence
			t.finals++
		}
		if msg.Finished {
			t.finished = true
		}
		t.mu.Unlock()
		if msg.Finished {
			return
		}
	}
}

// fail records the first error of the stream.
func (t *stream) fail(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err == nil {
		t.err = fmt.Errorf("soniox: %w", err)
	}
}

func (t *stream) failure() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// Write implements speechstream.Transcription: one binary frame of
// little-endian 16-bit PCM.
func (t *stream) Write(ctx context.Context, samples []int16) error {
	if err := t.failure(); err != nil {
		return err
	}
	if t.closed {
		return errors.New("soniox: the stream is closed")
	}
	if len(samples) == 0 {
		return nil
	}
	pcm := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	if err := t.conn.Write(ctx, websocket.MessageBinary, pcm); err != nil {
		t.fail(err)
		return t.failure()
	}
	t.samples += int64(len(samples))
	return nil
}

// Close implements speechstream.Transcription: it sends the empty text frame
// that ends the audio, waits (bounded by ctx) for the message with finished,
// then closes the socket. It releases the connection and the reader whatever
// happened before.
func (t *stream) Close(ctx context.Context) (speechstream.Result, error) {
	if t.closed {
		return t.result(), t.failure()
	}
	t.closed = true
	t.mu.Lock()
	ended := t.finished || t.err != nil
	t.mu.Unlock()
	if !ended {
		if err := t.conn.Write(ctx, websocket.MessageText, []byte{}); err != nil {
			t.fail(err)
			_ = t.conn.CloseNow()
		}
	}
	select {
	case <-t.done:
	case <-ctx.Done():
		t.fail(ctx.Err())
		_ = t.conn.CloseNow()
		<-t.done
	}
	// The reader has left the socket: a clean close after finished, an
	// immediate one after a failure (the server has usually hung up).
	if t.failure() != nil {
		_ = t.conn.CloseNow()
	} else if err := t.conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		_ = t.conn.CloseNow()
	}
	return t.result(), t.failure()
}

func (t *stream) result() speechstream.Result {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := speechstream.Result{Text: strings.TrimSpace(t.text.String()), AudioSeconds: float64(t.samples) / float64(t.rate)}
	if t.finals > 0 {
		r.Confidence = t.confidence / float64(t.finals)
	}
	return r
}

// streamChunk is how many samples a Genkit request writes per frame when it
// plays a recording through the real-time model.
const streamChunk = 8000

// transcribeStream is the Genkit path of a real-time model: the request's
// audio, a WAV or header-less audio/L16;rate=N PCM, is written whole to a
// stream and the transcript read at Close.
func (s *Soniox) transcribeStream(ctx context.Context, model string, req *ai.ModelRequest, cfg Config, mime string, data []byte) (*ai.ModelResponse, error) {
	pcm, rate, err := pcm16(mime, data)
	if err != nil {
		return nil, err
	}
	lang := cfg.Language
	if lang == "" && len(cfg.LanguageHints) > 0 {
		lang = cfg.LanguageHints[0]
	}
	t, err := s.OpenTranscription(ctx, speechstream.Options{Model: model, Rate: rate, Language: lang})
	if err != nil {
		return nil, err
	}
	samples := make([]int16, len(pcm)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	for len(samples) > 0 {
		n := min(len(samples), streamChunk)
		if err := t.Write(ctx, samples[:n]); err != nil {
			break // Close reports it
		}
		samples = samples[n:]
	}
	result, err := t.Close(ctx)
	if err != nil {
		return nil, err
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(result.Text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(result.Text),
			Custom:           map[string]float64{"audio_seconds": result.AudioSeconds, "confidence": result.Confidence},
		},
	}, nil
}

// pcm16 returns the 16-bit mono samples and rate of a WAV or of a
// header-less audio/L16;rate=N part, which is what a real-time model takes.
func pcm16(mime string, data []byte) ([]byte, int, error) {
	if rate := pcmRate(mime); rate > 0 {
		return data, rate, nil
	}
	pcm, rate, err := wavPCM(data)
	if err != nil {
		return nil, 0, err
	}
	return pcm, rate, nil
}

// pcmRate reads the rate of a header-less PCM content type (audio/L16;rate=N);
// 0 for anything else.
func pcmRate(mime string) int {
	mt, params, _ := strings.Cut(strings.ToLower(mime), ";")
	if strings.TrimSpace(mt) != "audio/l16" {
		return 0
	}
	for _, kv := range strings.Split(params, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		if k == "rate" {
			rate, _ := strconv.Atoi(strings.TrimSpace(v))
			return rate
		}
	}
	return 0
}

// wavPCM walks a WAV's RIFF chunks to its data chunk and returns the samples
// with the fmt chunk's rate; it only accepts 16-bit mono PCM, the format the
// real-time model is fed.
func wavPCM(data []byte) ([]byte, int, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, errors.New("soniox: the real-time model takes audio/wav or audio/L16;rate=N audio")
	}
	var rate, channels, bits int
	for pos := 12; pos+8 <= len(data); {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		switch id {
		case "fmt ":
			if pos+16 > len(data) {
				return nil, 0, errors.New("soniox: truncated WAV header")
			}
			channels = int(binary.LittleEndian.Uint16(data[pos+2 : pos+4]))
			rate = int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
			bits = int(binary.LittleEndian.Uint16(data[pos+14 : pos+16]))
		case "data":
			if rate <= 0 || channels != 1 || bits != 16 {
				return nil, 0, fmt.Errorf("soniox: the real-time model takes 16-bit mono PCM, not %d-bit %d-channel at %d Hz", bits, channels, rate)
			}
			if pos+size > len(data) {
				size = len(data) - pos
			}
			return data[pos : pos+size], rate, nil
		}
		pos += size + size%2
	}
	return nil, 0, errors.New("soniox: the WAV has no data chunk")
}
