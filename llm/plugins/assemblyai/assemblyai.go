// Package assemblyai is a Genkit model plugin for AssemblyAI speech to text:
// models named assemblyai/<model> (universal, slam-1, nano and best by
// default; the model is AssemblyAI's speech_model) take an audio media part
// in a user message and answer with the transcript as text. It is an ordinary
// Genkit plugin, usable in any Genkit Go app:
//
//	g := genkit.Init(ctx, genkit.WithPlugins(&assemblyai.AssemblyAI{APIKey: key}))
//	resp, err := genkit.Generate(ctx, g, ai.WithModelName("assemblyai/universal"),
//	    ai.WithMessages(ai.NewUserMessage(ai.NewMediaPart("audio/wav", dataURL))))
//
// A request uploads the audio (POST /v2/upload), creates a transcript
// (POST /v2/transcript) pointing at the upload, polls it
// (GET /v2/transcript/{id}) until it completes and deletes it
// (DELETE /v2/transcript/{id}), which removes the transcript's content from
// AssemblyAI. The response's audio duration and confidence are reported in
// the usage's Custom map (audio_seconds, confidence).
//
// The universal model also transcribes a live stream: OpenTranscription
// (speechstream.Transcriber) opens one Universal-Streaming v3 session
// (wss://streaming.assemblyai.com/v3/ws), the caller writes 16-bit PCM
// samples as they are heard and reads the transcript at Close. slam-1, nano
// and best have no streaming path and report speechstream.ErrUnsupported.
// Universal-Streaming takes no model or language parameter, so
// speechstream.Options.Language is ignored: as far as the author knows the
// stream is English-first with the provider's own detection.
package assemblyai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
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
const Provider = "assemblyai"

// DefaultBaseURL is AssemblyAI's API.
const DefaultBaseURL = "https://api.assemblyai.com"

// DefaultWSBaseURL is AssemblyAI's Universal-Streaming endpoint, which
// OpenTranscription connects to.
const DefaultWSBaseURL = "wss://streaming.assemblyai.com"

// DefaultModels are defined when Models is empty; they are AssemblyAI's
// speech_model values.
var DefaultModels = []string{"universal", "slam-1", "nano", "best"}

// DefaultPollInterval is how often a transcript's status is read.
const DefaultPollInterval = 400 * time.Millisecond

// AssemblyAI is the plugin.
type AssemblyAI struct {
	// APIKey authenticates; ASSEMBLYAI_API_KEY when empty.
	APIKey string
	// BaseURL overrides the API endpoint (tests, proxies).
	BaseURL string
	// WSBaseURL overrides the Universal-Streaming endpoint that
	// OpenTranscription dials (tests, proxies); DefaultWSBaseURL when empty.
	WSBaseURL string
	// HTTPClient overrides the client (default: 60 s timeout per call).
	HTTPClient *http.Client
	// Models to define, without the provider prefix.
	Models []string
	// PollInterval between status reads (DefaultPollInterval when zero).
	PollInterval time.Duration
}

// Config is the per-request configuration (ai.WithConfig).
type Config struct {
	// Language is the audio's language as a BCP-47 tag or a bare code (en,
	// en-GB, es-ES); it becomes AssemblyAI's language_code. Empty means
	// automatic language detection unless LanguageDetection is false.
	Language string `json:"language,omitempty"`
	// LanguageDetection toggles language_detection when Language is empty;
	// nil means on.
	LanguageDetection *bool `json:"language_detection,omitempty"`
	// Keyterms are domain terms to boost (keyterms_prompt; universal and
	// slam-1 honour them).
	Keyterms []string `json:"keyterms,omitempty"`
	// SpeakerLabels asks for speaker diarization (speaker_labels).
	SpeakerLabels bool `json:"speaker_labels,omitempty"`
}

// Name implements api.Plugin.
func (a *AssemblyAI) Name() string { return Provider }

// Init implements api.Plugin.
func (a *AssemblyAI) Init(context.Context) []api.Action {
	if a.APIKey == "" {
		a.APIKey = os.Getenv("ASSEMBLYAI_API_KEY")
	}
	if a.BaseURL == "" {
		a.BaseURL = DefaultBaseURL
	}
	if a.WSBaseURL == "" {
		a.WSBaseURL = DefaultWSBaseURL
	}
	if a.HTTPClient == nil {
		a.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if a.PollInterval <= 0 {
		a.PollInterval = DefaultPollInterval
	}
	models := a.Models
	if len(models) == 0 {
		models = DefaultModels
	}
	var actions []api.Action
	for _, m := range models {
		actions = append(actions, a.model(m))
	}
	return actions
}

func (a *AssemblyAI) model(name string) api.Action {
	supports := &ai.ModelSupports{Media: true, Multiturn: false, Output: []string{"text"}}
	return ai.NewModelAction(Provider+"/"+name, &ai.ModelOptions{Label: "AssemblyAI " + name, Supports: supports},
		func(ctx context.Context, req *ai.ModelRequest, cfg Config, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return a.transcribe(ctx, name, req, cfg)
		})
}

// transcript is the part of a transcript's status tamesu reads.
type transcript struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	Text          string  `json:"text"`
	Confidence    float64 `json:"confidence"`
	AudioDuration float64 `json:"audio_duration"`
	LanguageCode  string  `json:"language_code"`
	Error         string  `json:"error"`
}

func (a *AssemblyAI) transcribe(ctx context.Context, model string, req *ai.ModelRequest, cfg Config) (*ai.ModelResponse, error) {
	if a.APIKey == "" {
		return nil, errors.New("assemblyai: no API key (set ASSEMBLYAI_API_KEY or AssemblyAI.APIKey)")
	}
	_, data, err := audioPart(req)
	if err != nil {
		return nil, err
	}
	uploadURL, err := a.upload(ctx, data)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"audio_url": uploadURL, "speech_model": model}
	if cfg.Language != "" {
		body["language_code"] = languageCode(cfg.Language)
	} else if cfg.LanguageDetection == nil || *cfg.LanguageDetection {
		body["language_detection"] = true
	}
	if len(cfg.Keyterms) > 0 {
		body["keyterms_prompt"] = cfg.Keyterms
	}
	if cfg.SpeakerLabels {
		body["speaker_labels"] = true
	}
	payload, _ := json.Marshal(body)
	var tr transcript
	if err := a.call(ctx, http.MethodPost, "/v2/transcript", "application/json", bytes.NewReader(payload), &tr); err != nil {
		return nil, err
	}
	if tr.ID == "" {
		return nil, errors.New("assemblyai: the transcript has no id")
	}
	// The transcript is deleted whatever happens, which removes its content
	// from AssemblyAI.
	path := "/v2/transcript/" + url.PathEscape(tr.ID)
	defer func() { _ = a.call(context.WithoutCancel(ctx), http.MethodDelete, path, "", nil, nil) }()
	for tr.Status != "completed" {
		if tr.Status == "error" {
			return nil, fmt.Errorf("assemblyai: transcription failed: %s", strings.TrimSpace(tr.Error))
		}
		timer := time.NewTimer(a.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if err := a.call(ctx, http.MethodGet, path, "", nil, &tr); err != nil {
			return nil, err
		}
	}
	text := strings.TrimSpace(tr.Text)
	return &ai.ModelResponse{
		Request:      req,
		FinishReason: ai.FinishReasonStop,
		Message:      ai.NewModelTextMessage(text),
		Usage: &ai.GenerationUsage{
			InputAudioFiles:  1,
			OutputCharacters: len(text),
			Custom:           map[string]float64{"audio_seconds": tr.AudioDuration, "confidence": tr.Confidence},
		},
	}, nil
}

// upload sends the raw audio and returns the URL AssemblyAI stores it at.
func (a *AssemblyAI) upload(ctx context.Context, data []byte) (string, error) {
	var out struct {
		UploadURL string `json:"upload_url"`
	}
	if err := a.call(ctx, http.MethodPost, "/v2/upload", "application/octet-stream", bytes.NewReader(data), &out); err != nil {
		return "", err
	}
	if out.UploadURL == "" {
		return "", errors.New("assemblyai: the upload returned no upload_url")
	}
	return out.UploadURL, nil
}

// call makes one API request and decodes a JSON response into out (nil for
// none).
func (a *AssemblyAI) call(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	// AssemblyAI takes the raw key, without a Bearer prefix.
	req.Header.Set("Authorization", a.APIKey)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := a.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("assemblyai: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("assemblyai: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("assemblyai: %s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("assemblyai: decoding the response of %s %s: %w", method, path, err)
	}
	return nil
}

// languageCode maps a BCP-47 tag to AssemblyAI's language_code: lower case,
// with the English regional variants AssemblyAI distinguishes (en_uk, en_us,
// en_au); any other locale is cut to its language and a bare code passes
// through.
func languageCode(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	switch tag {
	case "en-gb":
		return "en_uk"
	case "en-us":
		return "en_us"
	case "en-au":
		return "en_au"
	}
	lang, _, _ := strings.Cut(tag, "-")
	return lang
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
	return "", nil, errors.New("assemblyai: the request has no audio media part")
}

func decodePart(p *ai.Part) (string, []byte, error) {
	s := p.Text
	mime := p.ContentType
	if strings.HasPrefix(s, "data:") {
		meta, payload, ok := strings.Cut(s[len("data:"):], ",")
		if !ok {
			return "", nil, errors.New("assemblyai: malformed data URL")
		}
		if m, _, _ := strings.Cut(meta, ";"); m != "" {
			mime = m
		}
		if !strings.Contains(meta, "base64") {
			return "", nil, errors.New("assemblyai: data URL must be base64")
		}
		s = payload
	} else if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return "", nil, errors.New("assemblyai: media URLs are not fetched; pass the audio as a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", nil, fmt.Errorf("assemblyai: decoding the audio: %w", err)
	}
	if mime == "" {
		mime = "audio/wav"
	}
	return mime, data, nil
}

// Streaming: Universal-Streaming v3 over a WebSocket.

// AssemblyAI wants 50 ms to 1000 ms of audio per binary frame, so Write
// buffers samples and sends whenever at least minFrame is pending, at most
// maxFrame at a time.
const (
	minFrame = 100 * time.Millisecond
	maxFrame = time.Second
)

var _ speechstream.Transcriber = (*AssemblyAI)(nil)

// IsStreaming reports whether model (without the provider prefix) transcribes
// a stream: only universal does, through Universal-Streaming v3.
func IsStreaming(model string) bool { return model == "universal" }

// OpenTranscription implements speechstream.Transcriber: it connects one
// Universal-Streaming v3 session for 16-bit mono PCM at opts.Rate. It returns
// speechstream.ErrUnsupported, before any network call, for a model other
// than universal. opts.Language is ignored, the stream has no language
// parameter.
func (a *AssemblyAI) OpenTranscription(ctx context.Context, opts speechstream.Options) (speechstream.Transcription, error) {
	if !IsStreaming(opts.Model) {
		return nil, speechstream.ErrUnsupported
	}
	if a.APIKey == "" {
		return nil, errors.New("assemblyai: no API key (set ASSEMBLYAI_API_KEY or AssemblyAI.APIKey)")
	}
	if opts.Rate <= 0 {
		return nil, fmt.Errorf("assemblyai: invalid sample rate %d", opts.Rate)
	}
	base := a.WSBaseURL
	if base == "" {
		base = DefaultWSBaseURL
	}
	q := url.Values{"sample_rate": {strconv.Itoa(opts.Rate)}, "encoding": {"pcm_s16le"}, "format_turns": {"true"}}
	u := strings.TrimRight(base, "/") + "/v3/ws?" + q.Encode()
	hdr := http.Header{}
	// The raw key, as for the REST API.
	hdr.Set("Authorization", a.APIKey)
	// The plugin's client has a per-call timeout that would cut the
	// long-lived socket; the dial keeps its transport without it.
	client := http.Client{}
	if a.HTTPClient != nil {
		client = *a.HTTPClient
	}
	client.Timeout = 0
	conn, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: &client, HTTPHeader: hdr})
	if err != nil {
		return nil, fmt.Errorf("assemblyai: %w", dialError(err, res))
	}
	conn.SetReadLimit(8 << 20)
	bytesPer := func(d time.Duration) int { return 2 * int(d*time.Duration(opts.Rate)/time.Second) }
	readCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	t := &transcription{
		conn:     conn,
		stop:     stop,
		rate:     opts.Rate,
		minBytes: bytesPer(minFrame),
		maxBytes: bytesPer(maxFrame),
		turns:    map[int]turn{},
		done:     make(chan struct{}),
	}
	go t.read(readCtx)
	return t, nil
}

// dialError describes a failed handshake by the response's status and body
// when the server answered, else by the transport error.
func dialError(err error, res *http.Response) error {
	if res == nil || res.StatusCode == http.StatusSwitchingProtocols {
		return err
	}
	var body string
	if res.Body != nil {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		_ = res.Body.Close()
		body = strings.TrimSpace(string(b))
	}
	if body == "" {
		return fmt.Errorf("GET /v3/ws: %s", res.Status)
	}
	return fmt.Errorf("GET /v3/ws: %s: %s", res.Status, body)
}

// transcription is one Universal-Streaming session. Write and Close run on
// the caller's goroutine; read owns the socket reads on another and records
// what it hears under mu.
type transcription struct {
	conn               *websocket.Conn
	stop               context.CancelFunc
	rate               int
	minBytes, maxBytes int
	samples            int64  // written so far, for Result.AudioSeconds
	pending            []byte // PCM not yet sent
	closed             bool

	done chan struct{} // closed when read exits

	mu          sync.Mutex
	err         error // the first failure, with the provider prefix
	turns       map[int]turn
	terminating bool // Close has sent Terminate
	terminated  bool // the server has sent Termination
}

// turn is the kept Turn of one turn_order.
type turn struct {
	text        string
	formatted   bool
	confidences []float64 // of its words
}

// message is the part of a server message tamesu reads: Begin, Turn,
// Termination or an error.
type message struct {
	Type            string `json:"type"`
	Error           string `json:"error"`
	TurnOrder       int    `json:"turn_order"`
	TurnIsFormatted bool   `json:"turn_is_formatted"`
	EndOfTurn       bool   `json:"end_of_turn"`
	Transcript      string `json:"transcript"`
	Words           []struct {
		Confidence float64 `json:"confidence"`
	} `json:"words"`
}

// Write implements speechstream.Transcription: it encodes the samples as
// little-endian PCM and sends them in frames of minFrame to maxFrame of
// audio, keeping a remainder for the next Write or Close.
func (t *transcription) Write(ctx context.Context, samples []int16) error {
	if err := t.failure(); err != nil {
		return err
	}
	t.samples += int64(len(samples))
	for _, s := range samples {
		t.pending = binary.LittleEndian.AppendUint16(t.pending, uint16(s))
	}
	for len(t.pending) >= t.minBytes {
		n := min(len(t.pending), t.maxBytes)
		if err := t.send(ctx, t.pending[:n]); err != nil {
			return err
		}
		rest := copy(t.pending, t.pending[n:])
		t.pending = t.pending[:rest]
	}
	return nil
}

// Close implements speechstream.Transcription: it flushes the pending audio,
// sends Terminate, waits for Termination (or the socket to close) within ctx,
// assembles the transcript and releases the connection and the reader.
func (t *transcription) Close(ctx context.Context) (speechstream.Result, error) {
	res := speechstream.Result{AudioSeconds: float64(t.samples) / float64(t.rate)}
	if t.closed {
		return res, errors.New("assemblyai: the transcription is already closed")
	}
	t.closed = true
	defer t.stop()
	err := t.failure()
	if err == nil && len(t.pending) > 0 {
		err = t.send(ctx, t.pending)
		t.pending = nil
	}
	if err == nil {
		t.mu.Lock()
		t.terminating = true
		t.mu.Unlock()
		if werr := t.conn.Write(ctx, websocket.MessageText, []byte(`{"type":"Terminate"}`)); werr != nil {
			err = t.fail(werr)
		}
	}
	if err == nil {
		select {
		case <-t.done:
			err = t.failure()
		case <-ctx.Done():
			err = t.fail(fmt.Errorf("waiting for the final transcript: %w", ctx.Err()))
		}
	}
	if err == nil {
		_ = t.conn.Close(websocket.StatusNormalClosure, "")
	} else {
		// Also unblocks the reader, whatever it is waiting for.
		_ = t.conn.CloseNow()
	}
	<-t.done
	res.Text, res.Confidence = t.result()
	return res, err
}

func (t *transcription) send(ctx context.Context, pcm []byte) error {
	if err := t.conn.Write(ctx, websocket.MessageBinary, pcm); err != nil {
		return t.fail(err)
	}
	return nil
}

// fail records err as the stream's failure unless one is recorded already,
// and returns the recorded one.
func (t *transcription) fail(err error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err == nil {
		t.err = fmt.Errorf("assemblyai: %w", err)
	}
	return t.err
}

func (t *transcription) failure() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// read receives the server's messages until Termination, an error message or
// the socket closes.
func (t *transcription) read(ctx context.Context) {
	defer close(t.done)
	for {
		_, data, err := t.conn.Read(ctx)
		if err != nil {
			t.readFailed(err)
			return
		}
		var msg message
		if err := json.Unmarshal(data, &msg); err != nil {
			_ = t.fail(fmt.Errorf("decoding a message: %w", err))
			return
		}
		switch {
		case msg.Error != "" || msg.Type == "Error":
			// The server's own account beats whatever the transport said.
			if msg.Error == "" {
				msg.Error = "the server reported an error"
			}
			t.mu.Lock()
			t.err = errors.New("assemblyai: " + msg.Error)
			t.mu.Unlock()
			return
		case msg.Type == "Turn":
			t.keep(msg)
		case msg.Type == "Termination":
			t.mu.Lock()
			t.terminated = true
			t.mu.Unlock()
			return
		}
	}
}

// readFailed records a read error, except the end of a session that is over:
// after Termination, or a normal closure once Terminate was sent.
func (t *transcription) readFailed(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil || t.terminated || (t.terminating && websocket.CloseStatus(err) == websocket.StatusNormalClosure) {
		return
	}
	t.err = fmt.Errorf("assemblyai: %w", err)
}

// keep stores a Turn: per turn_order the latest formatted one (format_turns
// is on, so that is the final text), or an unformatted end of turn when no
// formatted one has arrived.
func (t *transcription) keep(msg message) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cur, ok := t.turns[msg.TurnOrder]
	if !msg.TurnIsFormatted && (!msg.EndOfTurn || (ok && cur.formatted)) {
		return
	}
	tr := turn{text: msg.Transcript, formatted: msg.TurnIsFormatted}
	for _, w := range msg.Words {
		tr.confidences = append(tr.confidences, w.Confidence)
	}
	t.turns[msg.TurnOrder] = tr
}

// result joins the kept turns in order and averages their words' confidence.
func (t *transcription) result() (string, float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var parts []string
	var sum float64
	var n int
	for _, o := range slices.Sorted(maps.Keys(t.turns)) {
		tr := t.turns[o]
		if s := strings.TrimSpace(tr.text); s != "" {
			parts = append(parts, s)
		}
		for _, c := range tr.confidences {
			sum += c
			n++
		}
	}
	var conf float64
	if n > 0 {
		conf = sum / float64(n)
	}
	return strings.Join(parts, " "), conf
}
