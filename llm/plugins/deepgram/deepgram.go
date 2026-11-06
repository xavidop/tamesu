// Package deepgram is a Genkit model plugin for Deepgram speech: speech to
// text with the Nova models (deepgram/nova-3 takes an audio media part in a
// user message and answers with the transcript as text) and text to speech
// with the Aura voices (deepgram/aura-2-thalia-en speaks the request's text
// and answers with an audio media part, a data URL of 16-bit PCM). It is an
// ordinary Genkit plugin, usable in any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&deepgram.Deepgram{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("deepgram/nova-3"),
//	    ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", dataURL))))
//	resp, err = genkit.Generate(ctx, g, ai.WithModelName("deepgram/aura-2"),
//	    ai.WithPrompt("Hello there"), ai.WithConfig(deepgram.Config{Voice: "apollo"}))
//
// Transcription goes to the prerecorded endpoint (POST /v1/listen) with the
// audio as the body; the response's confidence and duration are reported in
// the usage's Custom map (confidence, audio_seconds). Speech goes to POST
// /v1/speak; an Aura voice is a model of its own (aura-2-<voice>-<lang>), so
// the Voice config, when set, picks the model that speaks.
//
// Every speech-to-text model (nova-3, nova-2, enhanced, base) also transcribes
// a live stream: OpenTranscription connects to the live endpoint (WebSocket
// /v1/listen) and the returned speechstream.Transcription takes 16-bit mono
// PCM as it is heard and hands back the transcript at Close, so the audio does
// not have to be held until the speaker stops. The Aura models return
// speechstream.ErrUnsupported.
package deepgram

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	"github.com/xavidop/tamesu/llm/plugins/speechstream"
)

// Provider is the plugin name and the prefix of its models.
const Provider = "deepgram"

// DefaultBaseURL is Deepgram's API.
const DefaultBaseURL = "https://api.deepgram.com"

// DefaultWSBaseURL is Deepgram's live (WebSocket) API, where
// OpenTranscription connects.
const DefaultWSBaseURL = "wss://api.deepgram.com"

// DefaultModels are the speech-to-text models defined when Models is empty.
var DefaultModels = []string{"nova-3", "nova-2", "enhanced", "base"}

// DefaultTTSModels are the text-to-speech models defined when Models is
// empty: the Aura-2 and Aura-1 families, where a bare family name takes its
// voice from the Voice config (DefaultVoice when none).
var DefaultTTSModels = []string{"aura-2", "aura-2-thalia-en", "aura-2-apollo-en", "aura", "aura-asteria-en"}

// DefaultVoice is the Aura voice used when a bare family model names none.
const DefaultVoice = "thalia"

// DefaultSampleRate is the PCM rate asked of the Aura models.
const DefaultSampleRate = 24000

// Deepgram is the plugin.
type Deepgram struct {
	// APIKey authenticates; DEEPGRAM_API_KEY when empty.
	APIKey string
	// BaseURL overrides the API endpoint (tests, proxies).
	BaseURL string
	// WSBaseURL overrides the live transcription endpoint (DefaultWSBaseURL);
	// tests point it at a ws:// server.
	WSBaseURL string
	// HTTPClient overrides the client (default: 60 s timeout).
	HTTPClient *http.Client
	// Models to define, without the provider prefix. Names starting with
	// "aura" are text to speech, the rest speech to text.
	Models []string
}

// Config is the per-request configuration (ai.WithConfig).
type Config struct {
	// Voice is an Aura voice: a full model name (aura-2-apollo-en) or a bare
	// voice name (apollo), completed with the family and language of the
	// model called. Text to speech.
	Voice string `json:"voice,omitempty"`
	// SampleRate of the PCM returned, in Hz (DefaultSampleRate). Text to
	// speech.
	SampleRate int `json:"sample_rate,omitempty"`
	// Language is a BCP-47 tag such as en or es-ES; omitted means Deepgram's
	// default for the model (or detection where supported).
	Language string `json:"language,omitempty"`
	// SmartFormat applies punctuation and formatting (default true).
	SmartFormat *bool `json:"smart_format,omitempty"`
	// Keywords boost recognition of the listed terms (nova-2 and older).
	Keywords []string `json:"keywords,omitempty"`
	// Keyterms boost recognition of the listed terms (nova-3).
	Keyterms []string `json:"keyterms,omitempty"`
	// Diarize labels speakers.
	Diarize bool `json:"diarize,omitempty"`
}

// Name implements api.Plugin.
func (d *Deepgram) Name() string { return Provider }

// Init implements api.Plugin.
func (d *Deepgram) Init(context.Context) []api.Action {
	if d.APIKey == "" {
		d.APIKey = os.Getenv("DEEPGRAM_API_KEY")
	}
	if d.BaseURL == "" {
		d.BaseURL = DefaultBaseURL
	}
	if d.WSBaseURL == "" {
		d.WSBaseURL = DefaultWSBaseURL
	}
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	models := d.Models
	if len(models) == 0 {
		models = append(append([]string(nil), DefaultModels...), DefaultTTSModels...)
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, d.model(m))
	}
	return actions
}

// IsTTS reports whether a model name (without the prefix) speaks.
func IsTTS(model string) bool { return strings.HasPrefix(model, "aura") }

func (d *Deepgram) model(name string) api.Action {
	if IsTTS(name) {
		supports := &ai.ModelSupports{Media: false, Multiturn: false, Output: []string{"media"}}
		return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Deepgram " + name, Supports: supports},
			func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
				return d.synthesize(ctx, name, req, cfg)
			})
	}
	supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "Deepgram " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return d.transcribe(ctx, name, req, cfg)
		})
}

// response is the part of Deepgram's prerecorded response tamesu reads.
type response struct {
	Metadata struct {
		Duration float64 `json:"duration"`
	} `json:"metadata"`
	Results struct {
		Channels []struct {
			Alternatives []struct {
				Transcript string  `json:"transcript"`
				Confidence float64 `json:"confidence"`
			} `json:"alternatives"`
		} `json:"channels"`
	} `json:"results"`
}

func (d *Deepgram) transcribe(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if d.APIKey == "" {
		return nil, errors.New("deepgram: no API key (set DEEPGRAM_API_KEY or Deepgram.APIKey)")
	}
	mime, data, err := audioPart(req)
	if err != nil {
		return nil, err
	}
	q := url.Values{"model": {model}}
	if cfg.Language != "" {
		q.Set("language", cfg.Language)
	}
	if cfg.SmartFormat == nil || *cfg.SmartFormat {
		q.Set("smart_format", "true")
	}
	for _, k := range cfg.Keywords {
		q.Add("keywords", k)
	}
	for _, k := range cfg.Keyterms {
		q.Add("keyterm", k)
	}
	if cfg.Diarize {
		q.Set("diarize", "true")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.BaseURL, "/")+"/v1/listen?"+q.Encode(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Token "+d.APIKey)
	httpReq.Header.Set("Content-Type", mime)
	res, err := d.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("deepgram: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("deepgram: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("deepgram: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	var out response
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("deepgram: decoding the response: %w", err)
	}
	text, confidence := "", 0.0
	if len(out.Results.Channels) > 0 && len(out.Results.Channels[0].Alternatives) > 0 {
		alt := out.Results.Channels[0].Alternatives[0]
		text, confidence = alt.Transcript, alt.Confidence
	}
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(text),
			Custom:           map[string]float64{"audio_seconds": out.Metadata.Duration, "confidence": confidence},
		},
	}, nil
}

func (d *Deepgram) synthesize(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if d.APIKey == "" {
		return nil, errors.New("deepgram: no API key (set DEEPGRAM_API_KEY or Deepgram.APIKey)")
	}
	text := promptText(req)
	if text == "" {
		return nil, errors.New("deepgram: the request has no text to speak")
	}
	rate := cfg.SampleRate
	if rate <= 0 {
		rate = DefaultSampleRate
	}
	q := url.Values{"model": {VoiceModel(model, cfg.Voice)}, "encoding": {"linear16"}, "sample_rate": {strconv.Itoa(rate)}}
	payload, _ := json.Marshal(map[string]string{"text": text})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.BaseURL, "/")+"/v1/speak?"+q.Encode(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Token "+d.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := d.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("deepgram: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("deepgram: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("deepgram: %s: %s", res.Status, strings.TrimSpace(string(data)))
	}
	mime := fmt.Sprintf("audio/L16;rate=%d", rate)
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelMessage(ai.NewMediaPart(mime, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data))),
		Usage: &ai.GenerationUsage{
			InputCharacters:  len(text),
			OutputAudioFiles: 1,
			Custom:           map[string]float64{"audio_seconds": float64(len(data)) / 2 / float64(rate)},
		},
	}, nil
}

// VoiceModel is the Aura model that speaks with voice: voice itself when it
// is a full model name (aura-2-apollo-en), otherwise the bare voice name
// completed with model's family (aura-2 or aura) and language (the last
// segment of a full model name, en by default). An empty voice keeps a full
// model name and gives a bare family DefaultVoice.
func VoiceModel(model, voice string) string {
	voice = strings.ToLower(strings.TrimSpace(voice))
	if strings.HasPrefix(voice, "aura") {
		return voice
	}
	family, lang := "aura", "en"
	if strings.HasPrefix(model, "aura-2") {
		family = "aura-2"
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(model, family), "-")
	if voice == "" {
		if rest != "" {
			return model
		}
		voice = DefaultVoice
	}
	if i := strings.LastIndex(rest, "-"); i >= 0 {
		lang = rest[i+1:]
	}
	return family + "-" + voice + "-" + lang
}

func promptText(req *ai.ModelRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if m.Role == ai.RoleSystem {
			continue
		}
		for _, p := range m.Content {
			if p.IsText() {
				b.WriteString(p.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// audioPart finds the audio in the request: the last media part, as a data
// URL (data:<mime>;base64,<data>) or raw base64 with the part's content type.
func audioPart(req *ai.ModelRequest) (mime string, data []byte, err error) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		for _, p := range req.Messages[i].Content {
			if !p.IsMedia() {
				continue
			}
			return decodePart(p)
		}
	}
	return "", nil, errors.New("deepgram: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("deepgram: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("deepgram: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("deepgram: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("deepgram: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}

// IsStreaming reports whether a model name (without the prefix) transcribes a
// live stream through OpenTranscription: every speech-to-text model does, the
// Aura voices do not.
func IsStreaming(model string) bool { return !IsTTS(model) }

var _ speechstream.Transcriber = (*Deepgram)(nil)

// OpenTranscription implements speechstream.Transcriber: it connects to the
// live endpoint for one utterance of 16-bit mono PCM at opts.Rate. It returns
// speechstream.ErrUnsupported for an Aura model before touching the network.
func (d *Deepgram) OpenTranscription(ctx context.Context, opts speechstream.Options) (speechstream.Transcription, error) {
	if !IsStreaming(opts.Model) {
		return nil, speechstream.ErrUnsupported
	}
	apiKey := d.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("DEEPGRAM_API_KEY")
	}
	if apiKey == "" {
		return nil, errors.New("deepgram: no API key (set DEEPGRAM_API_KEY or Deepgram.APIKey)")
	}
	if opts.Rate <= 0 {
		return nil, fmt.Errorf("deepgram: sample rate %d is not positive", opts.Rate)
	}
	base := d.WSBaseURL
	if base == "" {
		base = DefaultWSBaseURL
	}
	q := url.Values{
		"model":           {opts.Model},
		"encoding":        {"linear16"},
		"sample_rate":     {strconv.Itoa(opts.Rate)},
		"channels":        {"1"},
		"punctuate":       {"true"},
		"smart_format":    {"true"},
		"interim_results": {"false"},
	}
	if opts.Language != "" {
		q.Set("language", opts.Language)
	}
	// The plugin's client has a 60 s timeout meant for one HTTP request; it
	// would kill the long-lived socket, so dial with a copy without it.
	client := &http.Client{}
	if d.HTTPClient != nil {
		c := *d.HTTPClient
		c.Timeout = 0
		client = &c
	}
	headers := http.Header{"Authorization": {"Token " + apiKey}}
	conn, res, err := websocket.Dial(ctx, strings.TrimRight(base, "/")+"/v1/listen?"+q.Encode(),
		&websocket.DialOptions{HTTPClient: client, HTTPHeader: headers})
	if err != nil {
		if res != nil && res.StatusCode/100 != 2 {
			// Dial has already read the body (up to 1 KiB) and left it readable.
			body := ""
			if res.Body != nil {
				b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
				_ = res.Body.Close()
				body = strings.TrimSpace(string(b))
			}
			return nil, fmt.Errorf("deepgram: %s: %s", res.Status, body)
		}
		return nil, fmt.Errorf("deepgram: %w", err)
	}
	conn.SetReadLimit(8 << 20)
	t := &transcription{conn: conn, rate: opts.Rate, done: make(chan struct{})}
	go t.read()
	return t, nil
}

// transcription is one live stream. Write and Close run on the caller's
// goroutine; read runs on its own and owns the socket reads.
type transcription struct {
	conn    *websocket.Conn
	rate    int
	samples int64
	// closing is set once Close has asked the server to finish, after which
	// a Metadata message or a normal closure ends the stream without error.
	closing atomic.Bool
	// done is closed when read exits; mu guards what read fills in.
	done  chan struct{}
	mu    sync.Mutex
	texts []string
	// confSum and finals give the mean confidence of the final results.
	confSum float64
	finals  int
	err     error
}

// liveMessage is the part of a Deepgram live message tamesu reads.
type liveMessage struct {
	Type    string `json:"type"`
	IsFinal bool   `json:"is_final"`
	Channel struct {
		Alternatives []struct {
			Transcript string  `json:"transcript"`
			Confidence float64 `json:"confidence"`
		} `json:"alternatives"`
	} `json:"channel"`
	Description string `json:"description"`
	Message     string `json:"message"`
}

// read receives the server's messages until Metadata (the last one after
// CloseStream), an Error message, or the socket closes.
func (t *transcription) read() {
	defer close(t.done)
	for {
		typ, data, err := t.conn.Read(context.Background())
		if err != nil {
			if t.closing.Load() && websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return
			}
			t.fail(fmt.Errorf("deepgram: %w", err))
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg liveMessage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "Results":
			if !msg.IsFinal || len(msg.Channel.Alternatives) == 0 {
				continue
			}
			alt := msg.Channel.Alternatives[0]
			if text := strings.TrimSpace(alt.Transcript); text != "" {
				t.mu.Lock()
				t.texts = append(t.texts, text)
				t.confSum += alt.Confidence
				t.finals++
				t.mu.Unlock()
			}
		case "Metadata":
			if t.closing.Load() {
				return
			}
		case "Error":
			desc := msg.Description
			if desc == "" {
				desc = msg.Message
			}
			if desc == "" {
				desc = strings.TrimSpace(string(data))
			}
			t.fail(fmt.Errorf("deepgram: %s", desc))
			return
		}
	}
}

// fail records the first error of the stream.
func (t *transcription) fail(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err == nil {
		t.err = err
	}
}

// failure is the recorded error, or an explanation when the reader has exited
// without one.
func (t *transcription) failure() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	return errors.New("deepgram: the stream is closed")
}

// Write implements speechstream.Transcription: one binary frame per call.
func (t *transcription) Write(ctx context.Context, samples []int16) error {
	select {
	case <-t.done:
		return t.failure()
	default:
	}
	pcm := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(s))
	}
	if err := t.conn.Write(ctx, websocket.MessageBinary, pcm); err != nil {
		t.fail(fmt.Errorf("deepgram: %w", err))
		_ = t.conn.CloseNow()
		<-t.done
		return t.failure()
	}
	t.samples += int64(len(samples))
	return nil
}

// Close implements speechstream.Transcription: it sends CloseStream, waits
// (bounded by ctx) for the final results and the Metadata message that ends
// the stream, and releases the socket whatever happened.
func (t *transcription) Close(ctx context.Context) (speechstream.Result, error) {
	defer func() { _ = t.conn.CloseNow() }()
	t.closing.Store(true)
	select {
	case <-t.done:
	default:
		if err := t.conn.Write(ctx, websocket.MessageText, []byte(`{"type":"CloseStream"}`)); err != nil {
			t.fail(fmt.Errorf("deepgram: %w", err))
			_ = t.conn.CloseNow()
		}
	}
	var err error
	select {
	case <-t.done:
	case <-ctx.Done():
		_ = t.conn.CloseNow()
		<-t.done
		err = fmt.Errorf("deepgram: %w", ctx.Err())
	}
	result := speechstream.Result{AudioSeconds: float64(t.samples) / float64(t.rate)}
	t.mu.Lock()
	result.Text = strings.TrimSpace(strings.Join(t.texts, " "))
	if t.finals > 0 {
		result.Confidence = t.confSum / float64(t.finals)
	}
	if err == nil {
		err = t.err
	}
	t.mu.Unlock()
	if err != nil {
		return result, err
	}
	_ = t.conn.Close(websocket.StatusNormalClosure, "")
	return result, nil
}
